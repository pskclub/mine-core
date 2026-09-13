package main

import (
	"encoding/json"
	"go/ast"
	"net/http"
	"strings"
	"testing"
)

// TestHealthRegistrarExpandsToBothProbes is the whole point of reading the call:
// a service that registers its probes the usual way writes neither path, so
// nothing else in the file says these two endpoints exist.
func TestHealthRegistrarExpandsToBothProbes(t *testing.T) {
	routes := healthRoutesIn(t, `package p

func NewHTTP(e *core.Server) {
	core.RegisterHealthRoutes(e)
}`)

	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.Method+" "+route.Path)
	}
	if want := "GET /healthz,GET /readyz"; strings.Join(got, ",") != want {
		t.Fatalf("routes = %v, want %s", got, want)
	}
	for _, route := range routes {
		if len(route.Examples) == 0 {
			t.Errorf("%s %s has no example reply; the body is the framework's own and is known without running it",
				route.Method, route.Path)
		}
	}
}

// TestHealthRegistrarIgnoresUnqualifiedImportName covers a project that imports
// core under its own alias, or dot-imports it: matching on the qualifier would
// drop the probes of every service that spells the import differently.
func TestHealthRegistrarIgnoresUnqualifiedImportName(t *testing.T) {
	for name, src := range map[string]string{
		"aliased": `package p

func NewHTTP(e *Server) {
	framework.RegisterHealthRoutes(e, framework.HealthOptions{})
}`,
		"dot imported": `package p

func NewHTTP(e *Server) {
	RegisterHealthRoutes(e)
}`,
	} {
		if routes := healthRoutesIn(t, src); len(routes) != 2 {
			t.Errorf("%s: got %d routes, want 2", name, len(routes))
		}
	}
}

// TestHealthRegistrarNeedsARouter guards the one thing that makes matching on a
// bare function name safe: the call has to be adding routes to something.
func TestHealthRegistrarNeedsARouter(t *testing.T) {
	for name, src := range map[string]string{
		"no argument": `package p

func NewHTTP(e *Server) {
	RegisterHealthRoutes()
}`,
		"not a router": `package p

func NewHTTP(e *Server) {
	RegisterHealthRoutes(cfg.Server)
}`,
	} {
		if routes := healthRoutesIn(t, src); len(routes) != 0 {
			t.Errorf("%s: got %d routes, want none", name, len(routes))
		}
	}
}

// TestHealthProbesYieldToTheService covers the two ways the same path arrives
// twice: a second entry point calling the registrar again, and a service that
// mounts a probe by hand. Either way the collection holds one of each, and the
// one the service wrote is the one it keeps.
func TestHealthProbesYieldToTheService(t *testing.T) {
	own := routeIn(t, `e.GET("/healthz", Healthz)`)
	probes := append(healthRoutesIn(t, `package p

func NewAPI(e *Server) {
	core.RegisterHealthRoutes(e)
}`), healthRoutesIn(t, `package p

func NewAdmin(e *Server) {
	core.RegisterHealthRoutes(e)
}`)...)

	routes := appendHealthProbes([]routeInfo{own}, probes)

	got := make([]string, 0, len(routes))
	for _, route := range routes {
		got = append(got, route.Method+" "+route.Path)
	}
	if want := "GET /healthz,GET /readyz"; strings.Join(got, ",") != want {
		t.Fatalf("routes = %v, want %s", got, want)
	}
	if routes[0].HandlerMethod != "Healthz" {
		t.Errorf("GET /healthz resolved to %q, want the handler the service wrote",
			routes[0].HandlerMethod)
	}
}

// TestHealthProbeExamples states what the saved replies say. The 503 is the
// answer that takes the instance out of rotation — the reason the endpoint
// exists, so an example set without it documents the half nobody has to handle.
func TestHealthProbeExamples(t *testing.T) {
	live := liveExamples()
	if len(live) != 1 || live[0].Code != http.StatusOK {
		t.Fatalf("liveExamples = %+v, want one 200", live)
	}
	if got := statusIn(t, live[0].Body); got != healthStatusUp {
		t.Errorf("liveness status = %q, want %q", got, healthStatusUp)
	}

	ready := readyExamples()
	if len(ready) != 2 {
		t.Fatalf("readyExamples = %+v, want a 200 and a 503", ready)
	}
	for i, want := range []struct {
		code   int
		status string
	}{
		{http.StatusOK, healthStatusUp},
		{http.StatusServiceUnavailable, healthStatusDown},
	} {
		if ready[i].Code != want.code {
			t.Errorf("example %d code = %d, want %d", i, ready[i].Code, want.code)
		}
		if got := statusIn(t, ready[i].Body); got != want.status {
			t.Errorf("example %d status = %q, want %q", i, got, want.status)
		}
	}
}

// TestHealthHandlerMountedByHand covers the service that chose its own paths:
// the handler is the framework's, so the reply is just as knowable as it is
// behind RegisterHealthRoutes.
func TestHealthHandlerMountedByHand(t *testing.T) {
	tests := map[string]struct {
		src       string
		wantCodes []int
	}{
		"liveness":                  {src: `e.GET("/live", core.LiveHandler())`, wantCodes: []int{200}},
		"readiness":                 {src: `e.GET("/ready", core.ReadyHandler(app))`, wantCodes: []int{200, 503}},
		"wrapped":                   {src: `e.GET("/live", core.WithHTTPContext(core.LiveHandler()))`, wantCodes: []int{200}},
		"a handler of this project": {src: `e.GET("/live", c.Live)`},
	}

	for name, tt := range tests {
		route := routeIn(t, tt.src)
		got := make([]int, 0, len(route.Examples))
		for _, example := range route.Examples {
			got = append(got, example.Code)
		}
		if len(got) != len(tt.wantCodes) {
			t.Errorf("%s: example codes = %v, want %v", name, got, tt.wantCodes)
			continue
		}
		for i, code := range got {
			if code != tt.wantCodes[i] {
				t.Errorf("%s: example codes = %v, want %v", name, got, tt.wantCodes)
				break
			}
		}
	}
}

// healthRoutesIn runs the registrar over every call in a function body, the way
// buildRoutes does.
func healthRoutesIn(t *testing.T, src string) []routeInfo {
	t.Helper()

	cfg := testConfig()
	groups := map[string]*groupInfo{}
	var routes []routeInfo

	ast.Inspect(parseFunc(t, src).Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if probes, found := parseHealthRegistrarCall(&goFile{}, call, groups, cfg); found {
			routes = append(routes, probes...)
		}
		return true
	})
	return routes
}

// routeIn parses one route registration written as a statement.
func routeIn(t *testing.T, stmt string) routeInfo {
	t.Helper()

	fn := parseFunc(t, "package p\n\nfunc NewHTTP(e *Server) {\n\t"+stmt+"\n}")
	call := fn.Body.List[0].(*ast.ExprStmt).X.(*ast.CallExpr)

	route, ok := parseRouteCall(&goFile{}, call, map[string]controllerRef{}, map[string]*groupInfo{}, testConfig())
	if !ok {
		t.Fatalf("%s is not a route", stmt)
	}
	return route
}

func statusIn(t *testing.T, body string) string {
	t.Helper()

	var decoded struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("example body is not JSON: %v\n%s", err, body)
	}
	return decoded.Status
}
