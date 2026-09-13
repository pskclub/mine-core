package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRoutesFromAModuleMethod is the guard on core.IModule: a module registers
// its routes in a method (`func (m *Module) Routes(e *core.Server)`), not in a
// package-level function, and it does so in a file named for the module rather
// than for HTTP.
//
// Both are things the scanner could have excluded — it walks only files ending
// in a route suffix, and a method is a different declaration from a function.
// A collection that quietly lost every route of every module is exactly the
// failure nobody notices until somebody cannot find an endpoint, so it is
// asserted rather than assumed.
func TestRoutesFromAModuleMethod(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "modules/note/note.module.go", `package note

import (
	"example.com/tmp/httpx"
	"example.com/tmp/middlewares"
)

type Module struct{}

func New() *Module { return &Module{} }

func (m *Module) Name() string { return "note" }

func (m *Module) Routes(e *httpx.Server) {
	c := &NoteHandler{}

	e.GET("/notes", c.List, middlewares.AuthRequire(e))
	e.POST("/notes/:id/publish", c.Publish, middlewares.AuthRequire(e))
}
`)
	// a handler in the same package, so the route resolves to a real method
	writeFixtureFile(t, root, "modules/note/note.handler.go", `package note

import "example.com/tmp/httpx"

type NoteHandler struct{}

func (h NoteHandler) List(c httpx.Context) error    { return nil }
func (h NoteHandler) Publish(c httpx.Context) error { return nil }
`)
	// a sibling file that is not a route file: its Get is a client call, and
	// picking it up is the reason the scan is restricted at all
	writeFixtureFile(t, root, "modules/note/note.service.go", `package note

func Fetch(client interface{ Get(string) error }) error { return client.Get("/upstream") }
`)

	cfg := testConfig()
	repo := &repoInfo{root: root, moduleName: "example.com/tmp"}
	files, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	routes := buildRoutes(repo, files, buildControllerMethodRegistry(repo, files, cfg), cfg)

	got := map[string]routeInfo{}
	for _, r := range routes {
		got[r.Method+" "+r.Path] = r
	}
	if len(got) != 2 {
		t.Fatalf("routes = %v, want exactly the two the module registered", keysOf(got))
	}
	if _, ok := got["GET /notes"]; !ok {
		t.Errorf("GET /notes missing: a module's routes must reach the collection")
	}
	publish, ok := got["POST /notes/:id/publish"]
	if !ok {
		t.Fatalf("POST /notes/:id/publish missing")
	}
	if !publish.NeedsAuth {
		t.Errorf("middleware on a module route is read the same as on any other")
	}
	if len(publish.PathParams) != 1 || publish.PathParams[0] != "id" {
		t.Errorf("PathParams = %v, want [id]", publish.PathParams)
	}
}

func writeFixtureFile(t *testing.T, root, rel, src string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}
