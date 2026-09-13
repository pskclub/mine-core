package core

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sync"
)

// ICronjobContext is the per-run context of a job. It keeps v1's name and its
// JobName() method, and adds everything a manually triggerable, parameterised
// run needs.
type ICronjobContext interface {
	IContext

	// JobName is the name the job was registered under.
	JobName() string
	// RunID identifies this single execution.
	RunID() string
	// Attempt is 1-based; attempt 2 is the first automatic retry.
	Attempt() int
	// Trigger reports what caused this run (schedule, manual, retry, replay…).
	Trigger() Trigger

	// Params decodes the run parameters into dest and validates them when dest
	// implements IValidateContext (preferred) or IValidate. Missing params leave
	// dest at its zero value.
	//
	// The run context *is* an IContext, so parameters validate with the same
	// builder — and the same DB-backed rules — as an HTTP request payload:
	//
	//	func (p *ReportParams) Valid(ctx core.IContext) core.IError {
	//	    v := valid.New(ctx)
	//	    v.Str("date", p.Date).Required().Date()
	//	    v.Str("code", p.Code).Exists("coupons", "code")
	//	    return v.Error()
	//	}
	//
	// Violations fail the run before the handler is called, and are stored on
	// JobRun.Error.Fields.
	Params(dest any) IError

	// Progress reports how far the run has got (0-100), visible to whoever is
	// watching the run.
	Progress(percent int, message string)
	// SetResult stores a summary of what the run produced.
	SetResult(v any)

	// Stopping is closed when the run has been asked to stop (cancel, timeout,
	// or shutdown). Long loops must check it — Go cannot kill a goroutine, so
	// cancellation is always cooperative.
	//
	//	for _, item := range items {
	//	    if c.IsStopping() { return c.Err() }
	//	    ...
	//	}
	Stopping() <-chan struct{}
	// IsStopping is a non-blocking Stopping() check.
	IsStopping() bool
}

// JobFunc is a job handler. The signature is unchanged from v1, so existing
// jobs keep working; the extra capabilities hang off the context.
type JobFunc func(c ICronjobContext) error

type jobContext struct {
	IContext
	runner *JobRunner
	log    ILogger

	name    string
	runID   string
	attempt int
	trigger Trigger
	params  json.RawMessage

	mu       sync.Mutex
	result   json.RawMessage
	progress int
	progMsg  string
}

var _ ICronjobContext = (*jobContext)(nil)

func (c *jobContext) JobName() string { return c.name }
func (c *jobContext) RunID() string   { return c.runID }
func (c *jobContext) Attempt() int    { return c.attempt }
func (c *jobContext) Trigger() Trigger {
	return c.trigger
}

// Log returns the run logger: it always writes to the application logger and,
// depending on the log policy, buffers or persists the line against this run.
func (c *jobContext) Log() ILogger { return c.log }

func (c *jobContext) Params(dest any) IError {
	if dest == nil {
		return New(http.StatusBadRequest, "INVALID_PARAMS", "params destination is nil")
	}
	if len(c.params) > 0 && string(c.params) != "null" {
		if err := json.Unmarshal(c.params, dest); err != nil {
			return New(http.StatusUnprocessableEntity, "INVALID_PARAMS", err.Error())
		}
	}
	return validateParams(c, dest)
}

// validateParams applies the payload's own validation. dest is always a pointer;
// when the job's parameter type is itself a pointer (RegisterJob[*P]) the
// interfaces live one level in, so unwrap before giving up — otherwise pointer
// params would silently skip validation.
func validateParams(c IContext, dest any) IError {
	switch v := dest.(type) {
	case IValidateContext:
		return v.Valid(c)
	case IValidate:
		return v.Valid()
	}
	rv := reflect.ValueOf(dest)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil
	}
	inner := rv.Elem()
	if inner.Kind() != reflect.Pointer || inner.IsNil() {
		return nil
	}
	switch v := inner.Interface().(type) {
	case IValidateContext:
		return v.Valid(c)
	case IValidate:
		return v.Valid()
	}
	return nil
}

func (c *jobContext) Progress(percent int, message string) {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	c.mu.Lock()
	c.progress, c.progMsg = percent, message
	c.mu.Unlock()
	if c.runner != nil {
		c.runner.reportProgress(c.runID, percent, message)
	}
}

func (c *jobContext) SetResult(v any) {
	if v == nil {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		c.log.Warn("job result is not serialisable", "err", err)
		return
	}
	c.mu.Lock()
	c.result = b
	c.mu.Unlock()
}

// takeResult returns whatever SetResult stored (nil when unused).
func (c *jobContext) takeResult() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.result
}

func (c *jobContext) Stopping() <-chan struct{} { return c.Done() }

func (c *jobContext) IsStopping() bool { return c.Err() != nil }
