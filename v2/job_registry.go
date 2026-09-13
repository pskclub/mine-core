package core

import (
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// JobRegistry holds the job definitions of a service. It is filled at startup
// and read by the runner and the scheduler.
type JobRegistry struct {
	mu   sync.RWMutex
	jobs map[string]*registeredJob
}

type registeredJob struct {
	def    JobDef
	fn     JobFunc
	params []ParamField
	paused bool
}

// JobInfo is everything an operator (or an admin UI) needs to know about a job
// without running it — including the shape of its parameters.
type JobInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Schedule is empty for a manual-only job.
	Schedule      string `json:"schedule,omitempty"`
	Queue         string `json:"queue"`
	MaxAttempts   int    `json:"max_attempts"`
	MaxConcurrent int    `json:"max_concurrent,omitempty"`
	Concurrency   string `json:"concurrency"`
	Replayable    bool   `json:"replayable"`
	Paused        bool   `json:"paused"`
	// Params describes what the job accepts. Empty means it takes none.
	Params []ParamField `json:"params,omitempty"`
}

// String renders a policy for display.
func (p ConcurrencyPolicy) String() string {
	switch p {
	case ConcurrencySkip:
		return "skip"
	case ConcurrencyReplace:
		return "replace"
	default:
		return "enqueue"
	}
}

// NewJobRegistry creates an empty registry.
func NewJobRegistry() *JobRegistry {
	return &JobRegistry{jobs: map[string]*registeredJob{}}
}

// Register adds a job with no parameters. Use RegisterJob for typed params.
func (r *JobRegistry) Register(def JobDef, fn JobFunc) IError {
	if def.Name == "" {
		return New(http.StatusBadRequest, "INVALID_JOB", "job name is required")
	}
	if fn == nil {
		return Newf(http.StatusBadRequest, "INVALID_JOB", "job %s has no handler", def.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.jobs[def.Name]; dup {
		return Newf(http.StatusConflict, "DUPLICATE_JOB", "job %s is already registered", def.Name)
	}
	r.jobs[def.Name] = &registeredJob{def: def, fn: fn, params: def.Params}
	return nil
}

// RegisterJob adds a job whose parameters are decoded into P and validated when
// P implements IValidate/IValidateContext. Scheduled runs get the zero value of
// P unless the trigger supplies params.
//
//	type ReportParams struct {
//	    Date string `json:"date"`
//	}
//
//	core.RegisterJob(reg, core.JobDef{Name: "report"},
//	    func(c core.ICronjobContext, p ReportParams) error { ... })
func RegisterJob[P any](r *JobRegistry, def JobDef, fn func(c ICronjobContext, params P) error) IError {
	if fn == nil {
		return Newf(http.StatusBadRequest, "INVALID_JOB", "job %s has no handler", def.Name)
	}
	derived := ParamSchemaOf[P]()
	if def.Params == nil {
		// the type may describe itself; otherwise publish what it alone reveals
		// (names and kinds)
		if described := describedParamSchema[P](); described != nil {
			def.Params = described
		} else {
			def.Params = derived
		}
	}
	if unknown := unknownParamNames(def.Params, derived); len(unknown) > 0 {
		// a declared schema that no longer matches the struct is worse than none:
		// the form would offer a field the job cannot receive
		return Newf(http.StatusBadRequest, "INVALID_JOB",
			"job %s declares parameters that its type does not have: %s",
			def.Name, strings.Join(unknown, ", "))
	}
	return r.Register(def, func(c ICronjobContext) error {
		p := newParams[P]()
		if err := c.Params(&p); err != nil {
			return err
		}
		return fn(c, p)
	})
}

// describedParamSchema asks the parameter type to describe itself, checking both
// the value and the pointer so the method may hang off either.
func describedParamSchema[P any]() []ParamField {
	p := newParams[P]()
	if d, ok := any(p).(IParamsDescriber); ok {
		return d.ParamSchema()
	}
	if d, ok := any(&p).(IParamsDescriber); ok {
		return d.ParamSchema()
	}
	return nil
}

// newParams builds the zero parameters. When P is a pointer type it is
// allocated rather than left nil, so a run with no params (a scheduled tick)
// still validates its defaults and the handler never receives a nil pointer.
func newParams[P any]() P {
	var p P
	rv := reflect.ValueOf(&p).Elem()
	if rv.Kind() == reflect.Pointer && rv.IsNil() {
		rv.Set(reflect.New(rv.Type().Elem()))
	}
	return p
}

// get returns the registered job, if any.
func (r *JobRegistry) get(name string) (*registeredJob, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	j, ok := r.jobs[name]
	return j, ok
}

// Def returns a job's definition.
func (r *JobRegistry) Def(name string) (JobDef, bool) {
	j, ok := r.get(name)
	if !ok {
		return JobDef{}, false
	}
	return j.def, true
}

// List returns every definition, ordered by name.
func (r *JobRegistry) List() []JobDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]JobDef, 0, len(r.jobs))
	for _, j := range r.jobs {
		out = append(out, j.def)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// Info returns the full description of every job, ordered by name — what an
// admin API serves so a UI can list the jobs and build a form for each one's
// parameters.
func (r *JobRegistry) Info() []JobInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]JobInfo, 0, len(r.jobs))
	for _, j := range r.jobs {
		info := JobInfo{
			Name:          j.def.Name,
			Description:   j.def.Description,
			Queue:         j.def.queue(),
			MaxAttempts:   j.def.maxAttempts(),
			MaxConcurrent: j.def.MaxConcurrent,
			Concurrency:   j.def.Concurrency.String(),
			Replayable:    j.def.replayable(),
			Paused:        j.paused,
			Params:        j.params,
		}
		if j.def.Schedule != nil {
			info.Schedule = j.def.Schedule.String()
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Name < out[k].Name })
	return out
}

// Params returns the parameter schema of a job (nil when it takes none).
func (r *JobRegistry) Params(name string) []ParamField {
	j, ok := r.get(name)
	if !ok {
		return nil
	}
	return j.params
}

// Pause stops a job from being scheduled or triggered. Runs already queued are
// left alone.
func (r *JobRegistry) Pause(name string) IError { return r.setPaused(name, true) }

// Resume undoes Pause.
func (r *JobRegistry) Resume(name string) IError { return r.setPaused(name, false) }

// IsPaused reports whether the job is paused.
func (r *JobRegistry) IsPaused(name string) bool {
	j, ok := r.get(name)
	return ok && j.paused
}

func (r *JobRegistry) setPaused(name string, paused bool) IError {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[name]
	if !ok {
		return Newf(http.StatusNotFound, "JOB_NOT_FOUND", "job %s is not registered", name)
	}
	j.paused = paused
	return nil
}
