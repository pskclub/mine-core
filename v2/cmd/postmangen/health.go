package main

import (
	"go/ast"
	"net/http"
	"slices"
)

// The health probes are the one pair of routes a service never writes: the
// framework registers both, at paths it fixes, from a single call. Read as
// syntax, that call is the only trace they exist — so without this the two
// endpoints every deployment depends on would be the two missing from its
// collection.
const (
	// healthRegistrar registers both probes at once.
	healthRegistrar = "RegisterHealthRoutes"
	// liveHandlerFunc and readyHandlerFunc are the same probes mounted by hand,
	// which a service that wants its own paths does instead.
	liveHandlerFunc  = "LiveHandler"
	readyHandlerFunc = "ReadyHandler"

	// The paths healthRegistrar fixes. They are not read off the call because
	// the call does not carry them.
	livePath  = "/healthz"
	readyPath = "/readyz"

	healthStatusUp   = "up"
	healthStatusDown = "down"
)

// healthRegistrarRoutes reads the registrar out of every file, not only the
// route files.
//
// This is the one registration a service does not write in a `.http.go`: the
// probes go on the server before the first module, which the standard template
// does in cmd/api.go. Reading it everywhere is safe where reading GET
// everywhere is not — GET is also a method of every HTTP client, while a call
// named RegisterHealthRoutes taking a router is nothing else.
func healthRegistrarRoutes(files []*goFile, cfg *Config) []routeInfo {
	var routes []routeInfo

	for _, gf := range files {
		for _, decl := range gf.file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}

			groups := map[string]*groupInfo{}
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				switch n := node.(type) {
				case *ast.AssignStmt:
					collectGroupVars(groups, n.Lhs, n.Rhs, cfg)
				case *ast.CallExpr:
					if probes, found := parseHealthRegistrarCall(gf, n, groups, cfg); found {
						routes = append(routes, probes...)
					}
				}
				return true
			})
		}
	}

	return routes
}

// appendHealthProbes adds the probes the registrar produced, minus any path the
// service already registered itself.
//
// Two calls to the registrar — a second entry point, an admin server — describe
// the same two endpoints, and a service that also mounts /healthz by hand meant
// its own handler. Either way the collection should hold one of each.
func appendHealthProbes(routes, probes []routeInfo) []routeInfo {
	for _, probe := range probes {
		taken := slices.ContainsFunc(routes, func(route routeInfo) bool {
			return route.Method == probe.Method && route.Path == probe.Path
		})
		if !taken {
			routes = append(routes, probe)
		}
	}
	return routes
}

// parseHealthRegistrarCall expands `core.RegisterHealthRoutes(e)` into the two
// routes it registers.
//
// The package qualifier is deliberately not checked. A project may import core
// under any name, and matching on the alias would drop the routes of every
// service that spells the import differently.
func parseHealthRegistrarCall(gf *goFile, call *ast.CallExpr, groups map[string]*groupInfo, cfg *Config) ([]routeInfo, bool) {
	if calledFuncName(call.Fun) != healthRegistrar || len(call.Args) == 0 {
		return nil, false
	}

	// The first argument is the router the probes are added to. Resolving it as
	// a group is what rejects a same-named call on something that is not one.
	group, ok := receiverGroup(groups, call.Args[0], cfg)
	if !ok {
		return nil, false
	}

	return []routeInfo{
		healthRoute(gf, group, livePath, liveExamples()),
		healthRoute(gf, group, readyPath, readyExamples()),
	}, true
}

// healthExamplesForHandler recognises a probe mounted by hand —
// `e.GET("/live", core.LiveHandler())` — so a service that chose its own paths
// documents the same replies as one that took the defaults.
func healthExamplesForHandler(arg ast.Expr) []staticExample {
	call, ok := arg.(*ast.CallExpr)
	if !ok {
		return nil
	}
	if callContainsName(call, handlerWrapper) && len(call.Args) > 0 {
		return healthExamplesForHandler(call.Args[0])
	}

	switch calledFuncName(call.Fun) {
	case liveHandlerFunc:
		return liveExamples()
	case readyHandlerFunc:
		return readyExamples()
	}
	return nil
}

func healthRoute(gf *goFile, group *groupInfo, path string, examples []staticExample) routeInfo {
	full := normalizeRoutePath(joinRoutePath(group.Prefix, path))
	return routeInfo{
		Method:         http.MethodGet,
		Path:           full,
		Folder:         folderNameFromRoute(full),
		Module:         moduleNameFromDir(gf.dirRel),
		HandlerPackage: gf.dirRel,
		PathParams:     pathParamsFromRoute(full),
		NeedsAuth:      group.NeedsAuth,
		Examples:       examples,
	}
}

// calledFuncName is the name a call names, with any package qualifier dropped.
func calledFuncName(fun ast.Expr) string {
	switch fn := fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

// healthReportJSON and healthCheckJSON mirror core.HealthReport and
// core.CheckResult, field for field and in the same order — the same reason
// errorBodyJSON exists: an example of a reply the framework writes should read
// exactly like that reply, not merely carry the same data.
type healthReportJSON struct {
	Status  string                     `json:"status"`
	Service string                     `json:"service,omitempty"`
	Checks  map[string]healthCheckJSON `json:"checks,omitempty"`
	TookMS  int64                      `json:"took_ms"`
}

type healthCheckJSON struct {
	Status   string `json:"status"`
	Critical bool   `json:"critical,omitempty"`
	Error    string `json:"error,omitempty"`
	TookMS   int64  `json:"took_ms"`
}

// liveExamples is the liveness reply, which has one shape on purpose: the probe
// touches no dependency, so the process either answers this or does not answer.
func liveExamples() []staticExample {
	body, ok := marshalSample(map[string]string{"status": healthStatusUp})
	if !ok {
		return nil
	}
	return []staticExample{{Code: http.StatusOK, Body: body}}
}

// readyExamples shows both answers that matter: the one that keeps the instance
// in the load balancer and the one that takes it out. The 503 is the reason the
// endpoint exists at all, so a collection showing only the 200 would document
// the half nobody has to handle.
//
// The checks are named "database" and "cache" because those are the names the
// App gives its own; a real report lists whatever that deployment configured.
func readyExamples() []staticExample {
	up, upOK := marshalSample(healthReportJSON{
		Status:  healthStatusUp,
		Service: sampleString,
		Checks: map[string]healthCheckJSON{
			"cache":    {Status: healthStatusUp, TookMS: 1},
			"database": {Status: healthStatusUp, Critical: true, TookMS: 2},
		},
		TookMS: 3,
	})
	down, downOK := marshalSample(healthReportJSON{
		Status:  healthStatusDown,
		Service: sampleString,
		Checks: map[string]healthCheckJSON{
			"cache": {Status: healthStatusUp, TookMS: 1},
			// The error text is only in the body outside production, which is
			// where a collection is pointed anyway.
			"database": {Status: healthStatusDown, Critical: true, Error: "context deadline exceeded", TookMS: 3000},
		},
		TookMS: 3000,
	})
	if !upOK || !downOK {
		return nil
	}

	return []staticExample{
		{Code: http.StatusOK, Body: up},
		{Code: http.StatusServiceUnavailable, Body: down},
	}
}
