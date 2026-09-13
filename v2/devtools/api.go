package devtools

import (
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- overview ---

type overviewResponse struct {
	Service   string            `json:"service"`
	Env       string            `json:"env"`
	Prefix    string            `json:"prefix"`
	StartedAt time.Time         `json:"started_at"`
	UptimeS   int64             `json:"uptime_s"`
	Protected bool              `json:"protected"`
	Writable  bool              `json:"writable"`
	Tracing   bool              `json:"tracing"`
	Profiling bool              `json:"profiling"`
	Runtime   runtimeInfo       `json:"runtime"`
	Caps      []core.Capability `json:"capabilities"`
	Routes    int               `json:"routes"`
	Jobs      jobsWiring        `json:"jobs"`
	// Modules is how many modules this mount was given, -1 when it was given no
	// set at all — the tab is hidden on -1 rather than showing an empty list.
	Modules int `json:"modules"`
}

type runtimeInfo struct {
	Go         string `json:"go"`
	OS         string `json:"os"`
	Arch       string `json:"arch"`
	NumCPU     int    `json:"num_cpu"`
	GOMAXPROCS int    `json:"gomaxprocs"`
	Goroutines int    `json:"goroutines"`
	HeapMB     int64  `json:"heap_mb"`
	SysMB      int64  `json:"sys_mb"`
	GCCount    uint32 `json:"gc_count"`
}

// jobsWiring says which of the three job pieces this mount was given, so the UI
// can tell "this service has no jobs" apart from "whoever mounted devtools did
// not pass the store" — two very different bugs that look the same in a panel
// showing an empty list.
type jobsWiring struct {
	Registry bool `json:"registry"`
	Store    bool `json:"store"`
	Queue    bool `json:"queue"`
	Runner   bool `json:"runner"`
	// Registered is how many jobs the registry holds.
	Registered int `json:"registered"`
	// Queued is the queue depth, -1 when there is no queue to ask.
	Queued int `json:"queued"`
}

func (d *devtools) overview(c core.IHTTPContext) error {
	cfg := d.app.Config()
	return c.JSON(http.StatusOK, overviewResponse{
		Service:   cfg.Service,
		Env:       cfg.ENV,
		Prefix:    d.prefix,
		StartedAt: d.started,
		UptimeS:   int64(time.Since(d.started).Seconds()),
		Protected: d.opts.protected(),
		Writable:  d.opts.AllowWrite && d.opts.Runner != nil,
		Tracing:   d.opts.Trace != nil,
		Profiling: d.opts.Pprof,
		Runtime:   readRuntime(),
		Caps:      d.app.Capabilities(),
		Routes:    len(d.srv.Router().Routes()),
		Jobs:      d.jobsWiring(c),
		Modules:   d.moduleCount(),
	})
}

// readRuntime samples the process. ReadMemStats stops the world briefly, which
// is why the overview is a page somebody opens rather than something polled on a
// timer.
func readRuntime() runtimeInfo {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	const mb = 1 << 20
	return runtimeInfo{
		Go:         runtime.Version(),
		OS:         runtime.GOOS,
		Arch:       runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		Goroutines: runtime.NumGoroutine(),
		HeapMB:     int64(m.HeapAlloc / mb),
		SysMB:      int64(m.Sys / mb),
		GCCount:    m.NumGC,
	}
}

func (d *devtools) jobsWiring(c core.IHTTPContext) jobsWiring {
	registry := d.opts.registry()
	w := jobsWiring{
		Registry: registry != nil,
		Store:    d.opts.store() != nil,
		Queue:    d.opts.Queue != nil,
		Runner:   d.opts.Runner != nil,
		Queued:   -1,
	}
	if registry != nil {
		w.Registered = len(registry.List())
	}
	if d.opts.Queue != nil {
		if n, err := d.opts.Queue.Len(c); err == nil {
			w.Queued = n
		}
	}
	return w
}

// --- routes ---

type routeEntry struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	// Handler is the function serving the route. It is empty for a route
	// registered straight on the embedded Echo — middleware, static files, or a
	// third-party mount — which is itself worth seeing.
	Handler string `json:"handler,omitempty"`
	Name    string `json:"name,omitempty"`
}

func (d *devtools) routes(c core.IHTTPContext) error {
	names := d.srv.HandlerNames()
	all := d.srv.Router().Routes()
	out := make([]routeEntry, 0, len(all))
	for _, r := range all {
		out = append(out, routeEntry{
			Method:  r.Method,
			Path:    r.Path,
			Handler: names[r.Method+" "+r.Path],
			Name:    r.Name,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Method < out[j].Method
		}
		return out[i].Path < out[j].Path
	})
	return c.JSON(http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

// --- modules ---

// modules reports what each module attached to this process.
//
// A mount with no ModuleSet answers with mounted:false rather than an empty
// list, for the same reason jobsWiring exists: "this service has no modules"
// and "whoever mounted devtools did not pass the set" are different facts, and
// an empty list says the first when it may mean the second.
func (d *devtools) modules(c core.IHTTPContext) error {
	set := d.opts.Modules
	items := set.Report()
	if items == nil {
		items = []core.ModuleReport{}
	}
	return c.JSON(http.StatusOK, map[string]any{
		"mounted": set != nil,
		"items":   items,
		"total":   len(items),
	})
}

// moduleCount is how many modules this mount can see, or -1 when it was given
// no set — which is what hides the tab rather than showing an empty one.
func (d *devtools) moduleCount() int {
	if d.opts.Modules == nil {
		return -1
	}
	return len(d.opts.Modules.Report())
}

// --- config ---

type configEntry struct {
	Key string `json:"key"`
	// Value is empty for a secret, whatever was loaded for anything else.
	Value string `json:"value,omitempty"`
	// Secret says the value was withheld; Set says there was one to withhold.
	// Both are needed: "the password is wrong" and "there is no password" are
	// different problems, and the panel must be able to say which without ever
	// printing the password.
	Secret bool `json:"secret,omitempty"`
	Set    bool `json:"set"`
}

func (d *devtools) config(c core.IHTTPContext) error {
	all := d.app.ENV().All()
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]configEntry, 0, len(keys))
	for _, k := range keys {
		v := all[k]
		e := configEntry{Key: k, Set: v != ""}
		if isSecret(k) {
			e.Secret = true
		} else {
			e.Value = v
		}
		out = append(out, e)
	}
	return c.JSON(http.StatusOK, map[string]any{"items": out, "total": len(out)})
}

// secretKeyParts are the substrings that make a configuration key too dangerous
// to print. The list errs towards withholding: a connection string carries a
// password, a DSN carries a key, and a value wrongly hidden costs one look at
// the deployment while a value wrongly shown cannot be taken back.
var secretKeyParts = []string{
	"password", "secret", "token", "credential", "private",
	"api_key", "access_key", "secret_key", "dsn", "connection_string",
}

func isSecret(key string) bool {
	k := strings.ToLower(key)
	for _, part := range secretKeyParts {
		if strings.Contains(k, part) {
			return true
		}
	}
	return false
}

// --- health ---

func (d *devtools) health(c core.IHTTPContext) error {
	if d.opts.Health != nil {
		return c.JSON(http.StatusOK, core.CheckHealth(c, d.app, *d.opts.Health))
	}
	// details on: this endpoint is already behind the guard, and a probe that
	// says "down" without saying why is the one nobody can act on
	details := true
	return c.JSON(http.StatusOK, core.CheckHealth(c, d.app, core.HealthOptions{Details: &details}))
}

// --- jobs ---

// jobEntry is a registered job plus what only this process can say about it:
// when it fires next here.
type jobEntry struct {
	core.JobInfo
	// NextRun is empty when the job is manual, or when this process does not arm
	// the cron — an API replica whose worker runs elsewhere. Scheduled (on the
	// response) is what tells those two apart.
	NextRun *time.Time `json:"next_run,omitempty"`
	InS     int64      `json:"in_s,omitempty"`
}

func (d *devtools) jobs(c core.IHTTPContext) error {
	registry := d.opts.registry()
	if registry == nil {
		return errNoJobs("registry", "Options.Registry")
	}

	info := registry.Info()
	items := make([]jobEntry, 0, len(info))
	for _, j := range info {
		entry := jobEntry{JobInfo: j}
		if d.opts.Runner != nil {
			if next, ok := d.opts.Runner.NextRun(j.Name); ok {
				entry.NextRun = &next
				entry.InS = int64(time.Until(next).Seconds())
			}
		}
		items = append(items, entry)
	}

	return c.JSON(http.StatusOK, map[string]any{
		"items": items,
		"total": len(items),
		// "nothing is armed in this process" is a different answer from "this job
		// has no schedule", and the panel must not present them the same way.
		"scheduled": d.opts.Runner != nil && d.opts.Runner.Scheduled(),
	})
}

func (d *devtools) runs(c core.IHTTPContext) error {
	store := d.opts.store()
	if store == nil {
		return errNoJobs("store", "Options.Store")
	}
	page, err := store.List(c, jobRunFilter(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, page)
}

// jobRunFilter reads the list query. Unknown statuses are dropped rather than
// rejected: the filter is a panel control, and an empty result explains itself
// better than a 400 from a URL somebody edited by hand.
func jobRunFilter(c core.IHTTPContext) core.JobRunFilter {
	f := core.JobRunFilter{
		JobName: c.QueryParam("job"),
		Queue:   c.QueryParam("queue"),
		Page:    c.GetPageOptions(),
	}
	if t := c.QueryParam("trigger"); t != "" {
		f.Trigger = core.Trigger(t)
	}
	for _, s := range strings.Split(c.QueryParam("status"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			f.Statuses = append(f.Statuses, core.RunStatus(s))
		}
	}
	return f
}

func (d *devtools) run(c core.IHTTPContext) error {
	store := d.opts.store()
	if store == nil {
		return errNoJobs("store", "Options.Store")
	}
	run, err := store.Get(c, c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// DefaultLogLimit is how many log lines one request returns. The UI pages with
// ?after=<seq>, so a long run is read in pieces rather than held in memory whole.
const DefaultLogLimit = 500

func (d *devtools) runLogs(c core.IHTTPContext) error {
	store := d.opts.store()
	if store == nil {
		return errNoJobs("store", "Options.Store")
	}
	after, _ := strconv.ParseInt(c.QueryParam("after"), 10, 64)
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if limit <= 0 || limit > DefaultLogLimit {
		limit = DefaultLogLimit
	}
	logs, err := store.Logs(c, c.Param("id"), after, limit)
	if err != nil {
		return err
	}
	if logs == nil {
		logs = []core.JobLog{}
	}
	return c.JSON(http.StatusOK, map[string]any{"items": logs, "total": len(logs)})
}

// errNoJobs names the missing piece and where it comes from. "Not found" here
// almost never means the service has no jobs — it means this mount was not
// given the runner's registry or store — so the message says which.
func errNoJobs(what, option string) core.IError {
	return core.Newf(http.StatusNotFound, "DEVTOOLS_NO_JOB_"+strings.ToUpper(what),
		"devtools: no job %s — pass one as %s when mounting", what, option)
}
