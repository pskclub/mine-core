package main

import (
	"net/http"
	"regexp"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/valid"
)

// The vocabulary the whole example service shares.
const (
	statusDraft     = "draft"
	statusPublished = "published"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// --- Example 2: binding and validating in one call --------------------------
//
// One struct describes the whole request — path, query and body — and one
// method says what a valid one looks like. BindWithValidate runs both.
//
// The alternative, Bind and then check by hand in the handler, is checking that
// can be forgotten; and the endpoint that forgets it is never the one anybody
// thought to test. Keeping the rules on the request type also means the same
// payload validates identically when a job or a consumer builds it.

// codeSlugTaken is a code this service raises itself. Registering the message
// next to the constant means using the code requires importing this file, so
// the message is guaranteed to exist by the time a violation renders it.
const codeSlugTaken = "SLUG_TAKEN"

func init() {
	valid.SetMessage(codeSlugTaken, "The {field} field is already used by another article")
}

// createArticleRequest binds JSON and form bodies with the same struct: the two
// content types differ in the tag, not in the handler or the rules.
//
// The fields are pointers so "absent" and "sent as empty" stay different
// questions. With a plain string a PATCH cannot say "clear this field", because
// a cleared value and an omitted one arrive identically.
type createArticleRequest struct {
	Title  *string  `json:"title" form:"title"`
	Slug   *string  `json:"slug" form:"slug"`
	Body   *string  `json:"body" form:"body"`
	Status *string  `json:"status" form:"status"`
	Tags   []string `json:"tags" form:"tags"`
}

// Valid implements core.IValidateContext. It receives the request's IContext,
// so a rule may reach the database — but only for facts about *this payload*
// (is the slug free?), never for business rules that depend on rows changing
// under it. Those belong in the service; see 03_errors.go.
func (r *createArticleRequest) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)

	// Trim before the length rules, not after: " a " is a one-character title
	// and should fail Length, and the normalizer rewrites the bound value so the
	// handler below sees what was validated.
	v.Str("title", r.Title).Trim().Required().Length(3, 120)
	v.Str("slug", r.Slug).Trim().Lower().Required().Match(slugPattern)
	v.Str("body", r.Body).Required().Min(1)
	v.Str("status", r.Status).In(statusDraft, statusPublished)
	v.Arr("tags", r.Tags).Max(5)

	// Cross-field rules the typed builders cannot express: a condition that
	// reads two fields at once.
	v.When(deref(r.Status) == statusPublished, func(v *valid.Validator) {
		v.Must("body", "REQUIRED_WITH", deref(r.Body) != "")
	})

	return v.Error()
}

// updateArticleRequest mixes sources: the id comes from the path, the filter
// from the query string, the rest from the body. `json:"-"` on the path field
// stops a client from overriding it through the body — the two bind into the
// same struct, and the last writer would win.
type updateArticleRequest struct {
	ID     string  `param:"id" json:"-"`
	Notify bool    `query:"notify" json:"-"`
	Title  *string `json:"title"`
	Status *string `json:"status"`
}

func (r *updateArticleRequest) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("id", &r.ID).Required().UUID()
	v.Str("title", r.Title).Trim().Length(3, 120)
	v.Str("status", r.Status).In(statusDraft, statusPublished)

	return v.Error()
}

func mountArticleWrites(g *core.Group) {
	g.POST("/articles", createArticle)
	g.PUT("/articles/:id", updateArticle)
	g.POST("/articles/preview", previewArticle)
}

func createArticle(c core.IHTTPContext) error {
	req := &createArticleRequest{}
	if err := c.BindWithValidate(req); err != nil {
		// Return it, do not log it. The framework's error handler renders
		// {code, message, fields} with the right status and reports what
		// deserves reporting; logging here as well makes one incident look
		// like two in the search that finds it.
		return err
	}

	// A thin handler: bind, hand the validated request to the service, answer.
	// 201 and not 200 — the status is part of what the endpoint means.
	return c.JSON(http.StatusCreated, articleResponse{
		ID:     "0f7d0f5c-52b6-4c1b-9f5e-6f1c0f8f1a11",
		Title:  deref(req.Title),
		Status: firstNonEmpty(deref(req.Status), statusDraft),
	})
}

func updateArticle(c core.IHTTPContext) error {
	req := &updateArticleRequest{}
	if err := c.BindWithValidate(req); err != nil {
		return err
	}

	return c.JSON(http.StatusOK, articleResponse{
		ID:     req.ID,
		Title:  deref(req.Title),
		Status: firstNonEmpty(deref(req.Status), statusDraft),
	})
}

// previewArticle is what BindOnly is for: an endpoint that renders whatever it
// is given and has no notion of a valid article, so running the create rules
// here would reject drafts the editor is still writing.
//
// BindOnly is not "validation later" — it is "there is nothing to validate".
func previewArticle(c core.IHTTPContext) error {
	req := &createArticleRequest{}
	if err := c.BindOnly(req); err != nil {
		return err
	}

	return c.String(http.StatusOK, "# "+deref(req.Title)+"\n\n"+deref(req.Body))
}

// deref reads an optional field. Pointers cost this one helper and buy the
// difference between "not sent" and "sent empty" on every request type.
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
