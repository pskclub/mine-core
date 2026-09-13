package core

import (
	"context"
	"net/http"
	"reflect"
	"sort"
	"sync"
)

// IModule is one feature of a service, in a shape the framework can assemble.
//
// Without it the registration of a single feature is spread across the
// composition root by kind rather than by feature — routes in cmd/api.go, jobs
// in cmd/worker.go, consumers somewhere else — which is the layering the
// modules/ convention rejected everywhere else in a service. Worse, half of it
// can be forgotten silently: a route nobody registered is a 404 found in QA, a
// cron nobody armed is a job that simply never runs, and both compile.
//
// A module declares everything it attaches in one file. What a role actually
// uses is then the Runner's decision, not the module's: a process with no HTTP
// server never calls Routes, so one module list serves every role.
//
// The only thing required is a name — every other capability is an optional
// interface (IHTTPModule, IJobModule, ICronModule, IMQModule, IHealthModule,
// ILifecycleModule) implemented only by the modules that have one.
//
// Modules are compile-time: the composition root still names every one of them.
// That is deliberate — see NewModules.
type IModule interface {
	// Name identifies the module. It must match [a-z0-9][a-z0-9_-]* and be
	// unique within a ModuleSet: it prefixes the module's health checks and
	// keys its entry in the devtools report.
	Name() string
}

// IHTTPModule registers the module's routes.
//
// The signature is the one services already write by hand
// (func NewNoteHTTP(e *core.Server)), so an existing registration function
// becomes a method without changing a line of its body.
//
// It returns nothing because the calls inside it do not: e.GET and friends
// return an echo.RouteInfo, never an error.
type IHTTPModule interface {
	Routes(e *Server)
}

// IJobModule registers the module's job definitions — what may be triggered,
// not what is scheduled.
//
// Every role that triggers or runs a job needs its definition, including an API
// role that has no scheduler at all. Use ICronModule for the ones that also
// fire on a clock.
type IJobModule interface {
	Jobs(reg *JobRegistry) IError
}

// ICronModule arms the module's scheduled jobs. Only a role with a Scheduler
// (worker, all) gets this far.
//
// Scheduler.Add both registers and arms, so a job armed here must not also be
// registered in Jobs — that is a duplicate name, and it is reported as one.
type ICronModule interface {
	Cron(sc *Scheduler) IError
}

// IMQModule registers the module's queue consumers.
type IMQModule interface {
	Consumers(c IMQConsumer)
}

// IHealthModule reports dependencies the framework cannot see by itself — a
// partner API, a search index, a license server.
//
// Each check's Name is prefixed with the module's, so two modules that both
// call a check "api" stay two entries in the report rather than one silently
// overwriting the other.
type IHealthModule interface {
	HealthChecks() []HealthCheck
}

// ILifecycleModule is background work the module owns.
//
// Start must not block. Stop runs during the drain — after requests have
// finished, before any connection pool is closed — so a stopping module may
// still use the database.
type ILifecycleModule interface {
	Start(app *App) IError
	Stop(ctx context.Context) IError
}

// ModuleReport is what one module actually registered, captured as it was
// mounted.
//
// It answers the question that makes a module system worth having: not "what
// does this module claim to do" but "what did it attach to this process" — the
// cron that was never armed because this role has no scheduler is visible here
// and nowhere else.
type ModuleReport struct {
	Name string `json:"name"`
	// Declares is the optional module interfaces this module implements —
	// "routes", "jobs", "cron", "consumers", "health", "lifecycle" — fixed at
	// assembly time, because it is a property of the type rather than of the
	// mount.
	//
	// It is what gives every other field here a denominator. A module with no
	// routes and a module whose routes this role never mounted are the same
	// empty list, and only the second is a problem; the same goes for Started,
	// which is false both for a module that has no background work and for one
	// whose Start never ran.
	Declares []string `json:"declares,omitempty"`
	// Started says background work is running — meaningful only when Declares
	// contains "lifecycle".
	Started bool `json:"started"`
	// Routes are "METHOD /path", for the routes registered through the
	// framework's own helpers. See Server.HandlerNames for the exception.
	Routes []string `json:"routes,omitempty"`
	// Jobs are the job names registered without a schedule.
	Jobs []string `json:"jobs,omitempty"`
	// Cron are the job names armed on a schedule.
	Cron []string `json:"cron,omitempty"`
	// Queues are the queue names consumed.
	Queues []string `json:"queues,omitempty"`
	// Health are the check names, already prefixed with the module's name.
	Health []string `json:"health,omitempty"`
}

// ModuleSet is the assembled modules of a service, and the only thing that
// mounts them.
//
// Safe for concurrent use: mounting happens at boot on one goroutine, but
// Report is read by the devtools handler while the process serves.
type ModuleSet struct {
	mods []IModule

	mu     sync.Mutex
	report map[string]*ModuleReport
	// mounted remembers which targets each kind has already been mounted onto,
	// so mounting the same server twice is a no-op rather than a duplicate
	// route. See MountHTTP.
	mounted map[string][]any
}

// NewModules assembles the modules of a service, rejecting a duplicate or
// malformed name straight away.
//
//	mods, err := core.NewModules(
//	    home.New(),
//	    auth.New(usersFor),
//	    note.New(usersFor),
//	)
//
// There is deliberately no global registry and no init()-time registration:
// this call is the one place that knows every module, which is what lets a test
// assemble the real service minus one module, and what keeps a module's
// dependencies (the interfaces it declares and the composition root supplies)
// passable at all — an init function takes no arguments.
func NewModules(mods ...IModule) (*ModuleSet, IError) {
	s := &ModuleSet{
		mods:    make([]IModule, 0, len(mods)),
		report:  make(map[string]*ModuleReport, len(mods)),
		mounted: map[string][]any{},
	}
	for i, m := range mods {
		if m == nil {
			return nil, Newf(http.StatusBadRequest, "INVALID_MODULE", "module at position %d is nil", i)
		}
		name := m.Name()
		if !validModuleName(name) {
			return nil, Newf(http.StatusBadRequest, "INVALID_MODULE",
				"module name %q must match [a-z0-9][a-z0-9_-]*", name)
		}
		if _, dup := s.report[name]; dup {
			return nil, Newf(http.StatusConflict, "DUPLICATE_MODULE",
				"module %s is registered twice", name)
		}
		s.mods = append(s.mods, m)
		s.report[name] = &ModuleReport{Name: name, Declares: declaredKinds(m)}
	}
	return s, nil
}

// declaredKinds is which optional interfaces m implements, in the order the
// runner mounts them.
func declaredKinds(m IModule) []string {
	out := make([]string, 0, 6)
	if _, ok := m.(ILifecycleModule); ok {
		out = append(out, "lifecycle")
	}
	if _, ok := m.(IJobModule); ok {
		out = append(out, "jobs")
	}
	if _, ok := m.(ICronModule); ok {
		out = append(out, "cron")
	}
	if _, ok := m.(IHTTPModule); ok {
		out = append(out, "routes")
	}
	if _, ok := m.(IMQModule); ok {
		out = append(out, "consumers")
	}
	if _, ok := m.(IHealthModule); ok {
		out = append(out, "health")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// validModuleName keeps a name usable as a health-check prefix and a metric
// tag: lowercase, no dots (the prefix separator), no leading punctuation.
func validModuleName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case (r == '-' || r == '_') && i > 0:
		default:
			return false
		}
	}
	return true
}

// Names is every module's name, in the order they were declared.
func (s *ModuleSet) Names() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.mods))
	for _, m := range s.mods {
		out = append(out, m.Name())
	}
	return out
}

// Start starts every module that has background work of its own, in declaration
// order. The first failure stops the rest: a half-started service is worse than
// one that refuses to boot, because only the second is noticed at deploy time.
func (s *ModuleSet) Start(app *App) IError {
	if s == nil {
		return nil
	}
	for _, m := range s.mods {
		lm, ok := m.(ILifecycleModule)
		if !ok {
			continue
		}
		if err := lm.Start(app); err != nil {
			return Wrapf(err, "module %s: start", m.Name())
		}
		s.edit(m.Name(), func(r *ModuleReport) { r.Started = true })
	}
	return nil
}

// Stop stops the modules that started, in reverse declaration order — the
// mirror of Start, so a module that depends on one declared before it is gone
// before that one is.
//
// Every module is asked to stop even if one fails; the first error is returned.
func (s *ModuleSet) Stop(ctx context.Context) IError {
	if s == nil {
		return nil
	}
	var firstErr IError
	for i := len(s.mods) - 1; i >= 0; i-- {
		m := s.mods[i]
		lm, ok := m.(ILifecycleModule)
		if !ok || !s.started(m.Name()) {
			continue
		}
		if err := lm.Stop(ctx); err != nil && firstErr == nil {
			firstErr = Wrapf(err, "module %s: stop", m.Name())
		}
		s.edit(m.Name(), func(r *ModuleReport) { r.Started = false })
	}
	return firstErr
}

// MountHTTP registers every module's routes on e.
//
// Mounting the same server twice is a no-op. That is not defensive coding: a
// service registers routes in its own NewAPI (which is what its tests call, and
// they never build a Runner) and then hands the same set to RunModules, so the
// second mount is the normal case rather than a mistake. A different server —
// a second listener, a test's own — mounts as usual.
func (s *ModuleSet) MountHTTP(e *Server) {
	if s == nil || e == nil || !s.claim("http", e) {
		return
	}
	for _, m := range s.mods {
		hm, ok := m.(IHTTPModule)
		if !ok {
			continue
		}
		before := routeKeys(e)
		hm.Routes(e)
		added := addedKeys(before, routeKeys(e))
		s.edit(m.Name(), func(r *ModuleReport) { r.Routes = append(r.Routes, added...) })
	}
}

// MountJobs registers every module's job definitions on reg. Mounting the same
// registry twice is a no-op.
func (s *ModuleSet) MountJobs(reg *JobRegistry) IError {
	if s == nil || reg == nil || !s.claim("jobs", reg) {
		return nil
	}
	for _, m := range s.mods {
		jm, ok := m.(IJobModule)
		if !ok {
			continue
		}
		before := jobKeys(reg)
		if err := jm.Jobs(reg); err != nil {
			return Wrapf(err, "module %s: jobs", m.Name())
		}
		added := addedKeys(before, jobKeys(reg))
		s.edit(m.Name(), func(r *ModuleReport) { r.Jobs = append(r.Jobs, added...) })
	}
	return nil
}

// MountCron arms every module's scheduled jobs on sc. Mounting the same
// scheduler twice is a no-op.
func (s *ModuleSet) MountCron(sc *Scheduler) IError {
	if s == nil || sc == nil || sc.Runner() == nil || !s.claim("cron", sc) {
		return nil
	}
	reg := sc.Runner().Registry()
	for _, m := range s.mods {
		cm, ok := m.(ICronModule)
		if !ok {
			continue
		}
		before := jobKeys(reg)
		if err := cm.Cron(sc); err != nil {
			return Wrapf(err, "module %s: cron", m.Name())
		}
		added := addedKeys(before, jobKeys(reg))
		s.edit(m.Name(), func(r *ModuleReport) { r.Cron = append(r.Cron, added...) })
	}
	return nil
}

// MountConsumers registers every module's queue consumers on c. Mounting the
// same consumer twice is a no-op.
func (s *ModuleSet) MountConsumers(c IMQConsumer) {
	if s == nil || c == nil || !s.claim("mq", c) {
		return
	}
	for _, m := range s.mods {
		mm, ok := m.(IMQModule)
		if !ok {
			continue
		}
		// the consumer is handed to the module wrapped, because IMQConsumer has
		// no way to be asked what it is consuming: the queue names exist only in
		// the calls the module makes, so they are taken as it makes them
		rec := &recordingConsumer{IMQConsumer: c}
		mm.Consumers(rec)
		added := rec.queues
		s.edit(m.Name(), func(r *ModuleReport) { r.Queues = append(r.Queues, added...) })
	}
}

// HealthChecks is every module's checks, each one named "<module>.<check>".
//
// Pass them to the probe at the composition root:
//
//	core.RegisterHealthRoutes(e, core.HealthOptions{Checks: mods.HealthChecks()})
//
// They are deliberately not stored on the App: the probe would then depend on
// having been registered after the modules, and a probe reporting fewer checks
// than the service has says nothing about the fact that it is incomplete.
func (s *ModuleSet) HealthChecks() []HealthCheck {
	if s == nil {
		return nil
	}
	out := make([]HealthCheck, 0, len(s.mods))
	for _, m := range s.mods {
		hm, ok := m.(IHealthModule)
		if !ok {
			continue
		}
		checks := hm.HealthChecks()
		names := make([]string, 0, len(checks))
		for _, hc := range checks {
			hc.Name = moduleHealthName(m.Name(), hc.Name)
			names = append(names, hc.Name)
			out = append(out, hc)
		}
		// assigned, not appended: unlike the mount methods this one has no
		// claim, so calling it twice (a probe and a startup gate) must not
		// report every check twice
		s.edit(m.Name(), func(r *ModuleReport) { r.Health = names })
	}
	return out
}

// Report is what each module registered, ordered by declaration.
func (s *ModuleSet) Report() []ModuleReport {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ModuleReport, 0, len(s.mods))
	for _, m := range s.mods {
		r := s.report[m.Name()]
		if r == nil {
			continue
		}
		clone := *r
		clone.Declares = append([]string(nil), r.Declares...)
		clone.Routes = append([]string(nil), r.Routes...)
		clone.Jobs = append([]string(nil), r.Jobs...)
		clone.Cron = append([]string(nil), r.Cron...)
		clone.Queues = append([]string(nil), r.Queues...)
		clone.Health = append([]string(nil), r.Health...)
		out = append(out, clone)
	}
	return out
}

// moduleHealthName prefixes a check with its module, following the same
// convention as healthName ("database.replica"). A module whose single check
// needs no name of its own is reported as the module itself.
func moduleHealthName(module, check string) string {
	if check == "" {
		return module
	}
	return module + "." + check
}

// implements reports whether any module satisfies the given optional interface,
// so the boot line can name the extension points this role has nowhere to put.
func (s *ModuleSet) implements(is func(IModule) bool) bool {
	if s == nil {
		return false
	}
	for _, m := range s.mods {
		if is(m) {
			return true
		}
	}
	return false
}

func (s *ModuleSet) edit(name string, fn func(*ModuleReport)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.report[name]; ok {
		fn(r)
	}
}

func (s *ModuleSet) started(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.report[name]
	return ok && r.Started
}

// claim records that kind has been mounted onto target, and reports whether
// this is the first time.
func (s *ModuleSet) claim(kind string, target any) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.mounted[kind] {
		if samePointer(t, target) {
			return false
		}
	}
	s.mounted[kind] = append(s.mounted[kind], target)
	return true
}

// samePointer compares two mount targets by identity. It goes through reflect
// rather than using == because the targets arrive as any: an implementation of
// IMQConsumer that is not a pointer would make == panic at boot, on a
// comparison whose only purpose is to skip work.
func samePointer(a, b any) bool {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	if va.Kind() != reflect.Pointer || vb.Kind() != reflect.Pointer {
		return false
	}
	return va.Type() == vb.Type() && va.Pointer() == vb.Pointer()
}

// routeKeys is the routing table as a set of "METHOD /path".
func routeKeys(e *Server) map[string]bool {
	out := map[string]bool{}
	if e == nil || e.Echo == nil || e.Router() == nil {
		return out
	}
	for _, ri := range e.Router().Routes() {
		out[ri.Method+" "+ri.Path] = true
	}
	return out
}

// jobKeys is the registry as a set of job names.
func jobKeys(reg *JobRegistry) map[string]bool {
	out := map[string]bool{}
	for _, def := range reg.List() {
		out[def.Name] = true
	}
	return out
}

// addedKeys is what after has and before did not, sorted — the routing table's
// own order is the router's business and not stable enough to report.
func addedKeys(before, after map[string]bool) []string {
	out := make([]string, 0, len(after)-len(before))
	for k := range after {
		if !before[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// recordingConsumer forwards to the real consumer and remembers which queues
// went through it. It returns itself from On/OnQueue so a chained registration
// keeps being recorded.
type recordingConsumer struct {
	IMQConsumer
	queues []string
}

func (r *recordingConsumer) On(queue string, h MQHandler) IMQConsumer {
	r.queues = append(r.queues, queue)
	r.IMQConsumer.On(queue, h)
	return r
}

func (r *recordingConsumer) OnQueue(cfg ConsumeQueue, h MQHandler) IMQConsumer {
	r.queues = append(r.queues, cfg.Queue.Name)
	r.IMQConsumer.OnQueue(cfg, h)
	return r
}
