package main

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/utils"
	"github.com/pskclub/mine-core/v2/valid"
)

// --- Example 2: parameters --------------------------------------------------
//
// Parameters are typed and validated on the way in, exactly like an HTTP
// payload. RegisterJob decodes the stored JSON into P and validates it before
// your handler ever sees it, so a bad trigger fails the run up front instead of
// blowing up halfway through — and the per-field violations are stored on
// JobRun.Error.Fields.

// ReportParams is the input of the "sales-report" job. Fields are pointers, as
// with request payloads, so "absent" and "empty" stay distinguishable. The
// struct stays a plain data type — nothing about the form lives in it.
type ReportParams struct {
	Date   *string  `json:"date"`
	Status *string  `json:"status,omitempty"`
	Email  *string  `json:"email,omitempty"`
	Limit  *int64   `json:"limit,omitempty"`
	Emails []string `json:"emails,omitempty"`
	Force  bool     `json:"force,omitempty"`
}

// reportParamsSchema is what the admin API publishes so a UI can build the "run
// now" form: the name, the shape, whether it is required, the allowed values and
// what each field means — declared in one place you can read top to bottom.
//
// Registration rejects a name that the struct does not have, so this cannot
// quietly drift out of sync with ReportParams.
func reportParamsSchema() []core.ParamField {
	return core.Params(
		core.DateParam("date").
			Label("Report date").
			Desc("Day to report on (defaults to yesterday)").
			Example("2026-07-01"),

		// an enum whose display text differs from the value sent
		core.StringParam("status").
			Options(
				core.Opt("draft", "ฉบับร่าง"),
				core.Opt("sent", "ส่งแล้ว"),
				core.Opt("paid", "ชำระแล้ว"),
			).
			Default("sent").
			Desc("Which invoices to include"),

		core.StringParam("email").
			Pattern(`^[^@]+@[^@]+$`).
			Desc("Notify this address when it is done").
			Example("ops@example.com"),

		// constraints the form can enforce before anything is submitted
		core.IntParam("limit").
			Between(1, 1000).
			Desc("Maximum rows").
			Example("500"),

		core.ArrayParam("emails", core.StringParam("")).
			Max(20).
			Desc("Extra recipients"),

		core.BoolParam("force").
			Desc("Rebuild even if the report already exists"),
	)
}

// Anything this framework does not model goes in Meta, and a kind of your own
// goes in CustomParam — the UI decides what to do with them. Wrap house
// conventions in a helper with With() so every job declares them the same way.
func tenantParam() *core.ParamSpec {
	return core.StringParam("tenant").
		Required().
		With(func(f *core.ParamField) {
			f.Pattern = "^t_[a-z0-9]+$"
			f.Description = "Tenant to run for"
		})
}

// PayoutParams describes itself, which is the only way to build a schema out of
// runtime state — allowed currencies from the database, a feature flag, whatever
// is not known at compile time.
type PayoutParams struct {
	Tenant *string `json:"tenant"`
	Amount *int64  `json:"amount"`
	Note   *string `json:"note,omitempty"`
}

// ParamSchema implements core.IParamsDescriber.
func (PayoutParams) ParamSchema() []core.ParamField {
	return core.Params(
		tenantParam(),
		core.CustomParam("amount", "currency").
			Meta("currency", "THB").
			Meta("widget", "money-input").
			Between(1, 1_000_000),
		core.StringParam("note").
			Multiline().
			Max(500).
			Meta("group", "audit"),
	)
}

func payoutRun(c core.ICronjobContext, p PayoutParams) error {
	c.Log().Info("paying out", "tenant", utils.ToNonPointerOr(p.Tenant, ""))
	return nil
}

// Valid implements core.IValidateContext. The run context *is* a core.IContext,
// so the whole valid package works here — including the DB-backed rules, which
// query through the same connections an HTTP handler would use.
func (p *ReportParams) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("date", p.Date).Required().Date("2006-01-02")
	v.Str("email", p.Email).Email().Exists("users", "email")
	v.Int("limit", p.Limit).Between(1, 1000)
	v.Arr("emails", p.Emails).Max(20)
	return v.Error()
}

func registerParamJobs(reg *core.JobRegistry) {
	// RegisterJob (not Register) gives the handler its params already decoded
	_ = core.RegisterJob(reg, core.JobDef{
		Name:        "sales-report",
		Description: "build the sales report for one day",
		Schedule:    core.Cron("30 1 * * *"),
		Timeout:     5 * time.Minute,
		MaxAttempts: 3,
		Params:      reportParamsSchema(),
	}, salesReport)

	// A parameter type can also describe itself — the only way to build a schema
	// from runtime state (values loaded from the database, a feature flag).
	_ = core.RegisterJob(reg, core.JobDef{
		Name:        "payout-run",
		Description: "schema declared by the parameter type",
	}, payoutRun)

	// A job that declares nothing still lists its parameters: the names and
	// kinds are read off the type. That is enough for a quick form, but only a
	// declared schema can say "required", "enum" or what a field means.
	_ = core.RegisterJob(reg, core.JobDef{
		Name:        "quick-report",
		Description: "same input, schema inferred from the type",
	}, salesReport)
}

func salesReport(c core.ICronjobContext, p *ReportParams) error {
	// A scheduled tick passes no parameters, so p holds its zero values — hence
	// "date" is validated but not Required: a job that is both scheduled *and*
	// parameterised has to have a sensible default for every field, otherwise
	// every scheduled run fails validation.
	date := utils.ToNonPointerOr(p.Date, time.Now().AddDate(0, 0, -1).Format("2006-01-02"))

	c.Log().Info("building sales report", "date", date, "force", p.Force)
	c.SetResult(map[string]any{"date": date, "rows": 42})
	return nil
}

// triggerReport is what a handler, a CLI command or an admin endpoint calls to
// run the job right now with specific parameters.
func triggerReport(ctx context.Context, runner *core.JobRunner, userID, date string) (*core.JobRun, core.IError) {
	return runner.Trigger(ctx, "sales-report",
		&ReportParams{Date: utils.ToPointer(date), Force: true},
		core.TriggerOptions{
			By: userID,
			// one run per day, however many times the button is clicked
			IdemKey: "sales-report:" + date,
		})
}

// triggerReportAndWait is the variant for a UI that shows the outcome
// immediately. Everything else about the run is identical — same queue, same
// status, same logs.
func triggerReportAndWait(ctx context.Context, runner *core.JobRunner, date string) (*core.JobRun, core.IError) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return runner.TriggerAndWait(ctx, "sales-report", &ReportParams{Date: utils.ToPointer(date)})
}

// invalidParams shows what a caller gets back when validation fails: the run is
// created, fails immediately, and JobRun.Error carries the per-field violations
// in the same shape the HTTP layer returns:
//
//	{
//	  "code": "INVALID_PARAMS",
//	  "fields": {
//	    "date":  {"code": "INVALID_DATE",  "message": "The date field must be a valid date"},
//	    "email": {"code": "NOT_EXISTS",    "message": "The email field does not exist"}
//	  }
//	}
func invalidParams(ctx context.Context, runner *core.JobRunner) {
	run, _ := runner.TriggerAndWait(ctx, "sales-report",
		&ReportParams{Date: utils.ToPointer("01-07-2026")})
	if run != nil && run.Error != nil {
		_ = run.Error.Fields // ready to hand straight back to the caller
	}
}
