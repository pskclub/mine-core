package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

const openAPIGoldenPath = "testdata/openapi.golden.json"

// TestGenerateOpenAPI is the guard on the second output format. It runs the same
// fixture the collection golden runs, so a change to a heuristic that shows up
// in one file and not the other means the two views have drifted apart — which
// is the failure this pairing exists to catch.
func TestGenerateOpenAPI(t *testing.T) {
	got := generateFixtureOpenAPI(t)

	if *update {
		if err := os.WriteFile(openAPIGoldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(openAPIGoldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	if normalizeEOL(got) != normalizeEOL(want) {
		t.Errorf("document differs from %s; rerun with -update to accept\n--- got ---\n%s", openAPIGoldenPath, got)
	}
}

func generateFixtureOpenAPI(t *testing.T) []byte {
	t.Helper()

	doc := fixtureDocument(t)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(data, '\n')
}

func fixtureDocument(t *testing.T) *oasDocument {
	t.Helper()

	cfg := testConfig()
	repo := &repoInfo{root: fixtureRoot, moduleName: fixtureModule}
	files, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("fixture has no Go files")
	}

	res := newResolver(repo, files, cfg)
	routes := buildRoutes(repo, files, buildControllerMethodRegistry(repo, files, cfg), cfg)
	doc, err := buildOpenAPI(routes, res, cfg)
	if err != nil {
		t.Fatalf("build openapi: %v", err)
	}
	return doc
}

// TestOpenAPIServersPreferSameOrigin pins the ordering the docs panel depends
// on: a reader who presses "Send" without touching the server picker must hit
// the process that served the page, not whatever host the config names.
func TestOpenAPIServersPreferSameOrigin(t *testing.T) {
	doc := fixtureDocument(t)

	if len(doc.Servers) == 0 {
		t.Fatal("document states no servers; the panel would have nowhere to send a request")
	}
	if doc.Servers[0].URL != sameOriginServer {
		t.Errorf("first server is %q, want %q so the request follows the page",
			doc.Servers[0].URL, sameOriginServer)
	}
}

// TestOpenAPIOperationIDsAreUnique guards the collision fallback. Two modules
// naming a handler List is ordinary, and a duplicated operationId makes a
// generated client drop one of them without saying so.
func TestOpenAPIOperationIDsAreUnique(t *testing.T) {
	doc := fixtureDocument(t)

	seen := map[string]string{}
	for path, item := range doc.Paths {
		for _, op := range operationsOf(item) {
			if op.OperationID == "" {
				t.Errorf("%s has an operation with no operationId", path)
				continue
			}
			if other, taken := seen[op.OperationID]; taken {
				t.Errorf("operationId %q is used by both %s and %s", op.OperationID, other, path)
			}
			seen[op.OperationID] = path
		}
	}
}

// TestOpenAPIEveryOperationAnswers guards spec validity: responses is required,
// and a route whose handler could not be read still has to say that it replies.
func TestOpenAPIEveryOperationAnswers(t *testing.T) {
	doc := fixtureDocument(t)

	for path, item := range doc.Paths {
		for _, op := range operationsOf(item) {
			if len(op.Responses) == 0 {
				t.Errorf("%s %s states no responses, which is not a valid operation", op.Summary, path)
			}
		}
	}
}

// TestOpenAPIPathParametersAreRequired guards the one place OpenAPI has no
// opinion to fall back on: a path parameter that is not required describes a
// route the router would never match.
func TestOpenAPIPathParametersAreRequired(t *testing.T) {
	doc := fixtureDocument(t)

	found := false
	for path, item := range doc.Paths {
		for _, op := range operationsOf(item) {
			for _, param := range op.Parameters {
				if param.In != "path" {
					continue
				}
				found = true
				if !param.Required {
					t.Errorf("%s: path parameter %q is not required", path, param.Name)
				}
			}
		}
	}
	if !found {
		t.Fatal("fixture produced no path parameters, so this test proved nothing")
	}
}

// TestOpenAPIAuthenticatedRoutesCarrySecurity checks that a route behind the
// auth middleware asks for the token in the panel rather than silently
// answering 401 to everyone who tries it.
func TestOpenAPIAuthenticatedRoutesCarrySecurity(t *testing.T) {
	doc := fixtureDocument(t)

	if _, found := doc.Components.SecuritySchemes[bearerSchemeName]; !found {
		t.Fatalf("document declares no %q scheme", bearerSchemeName)
	}

	secured := 0
	for _, item := range doc.Paths {
		for _, op := range operationsOf(item) {
			if len(op.Security) == 0 {
				continue
			}
			secured++
			if _, found := op.Security[0][bearerSchemeName]; !found {
				t.Errorf("%s references an undeclared scheme", op.OperationID)
			}
			if _, found := op.Responses["401"]; !found {
				t.Errorf("%s requires a token but documents no 401", op.OperationID)
			}
		}
	}
	if secured == 0 {
		t.Fatal("fixture produced no authenticated routes, so this test proved nothing")
	}
}

// TestOpenAPIValidatorRulesReachTheSchema is the point of building the document
// from the same parse as the collection: what valid.New states in Go has to
// arrive as a constraint a reader — and a generated client — can see.
func TestOpenAPIValidatorRulesReachTheSchema(t *testing.T) {
	doc := fixtureDocument(t)

	body := jsonBodySchema(t, doc, "/users", func(item *oasPathItem) *oasOperation { return item.Post })

	email := body.Properties["email"]
	if email == nil {
		t.Fatalf("create-user body has no email property; got %v", propertyNames(body))
	}
	if email.Format != "email" {
		t.Errorf("email format is %q, want \"email\" — the Email() rule did not reach the schema", email.Format)
	}
	if !slices.Contains(body.Required, "email") {
		t.Errorf("email is not required; Required() did not reach the schema (required: %v)", body.Required)
	}

	password := body.Properties["password"]
	if password == nil {
		t.Fatalf("create-user body has no password property; got %v", propertyNames(body))
	}
	if password.MinLength == nil || password.MaxLength == nil {
		t.Fatal("password states no length bounds; Length() did not reach the schema")
	}
	if *password.MinLength != 8 {
		t.Errorf("password minLength is %d, want 8 as Length(8, 72) states", *password.MinLength)
	}
}

// TestOpenAPIEnumsFollowTheFieldType guards the one constraint that can make a
// schema unsatisfiable: an In() rule rendered as strings on an integer field
// describes a value no client can send.
func TestOpenAPIEnumsFollowTheFieldType(t *testing.T) {
	doc := fixtureDocument(t)

	checked := 0
	for _, item := range doc.Paths {
		for _, op := range operationsOf(item) {
			for _, param := range op.Parameters {
				if param.Schema == nil || len(param.Schema.Enum) == 0 {
					continue
				}
				checked++
				for _, value := range param.Schema.Enum {
					if _, isString := value.(string); isString != (param.Schema.Type == "string") {
						t.Errorf("%s: enum value %#v does not match type %q",
							param.Name, value, param.Schema.Type)
					}
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("fixture produced no enums, so this test proved nothing")
	}
}

// TestOpenAPIResolvesResponsesThroughTheReceiver is the guard on the shapes a
// handler is actually written in. Both of these used to render as `{}`, which a
// reader of the panel cannot tell apart from an endpoint that genuinely replies
// with an empty object — the worst kind of wrong, because it looks like an
// answer.
func TestOpenAPIResolvesResponsesThroughTheReceiver(t *testing.T) {
	doc := fixtureDocument(t)

	tests := []struct {
		path  string
		shape string
		want  []string
	}{
		{
			path:  "/users/me",
			shape: "a service held as a field on the controller (m.users.Find)",
			want:  []string{"id", "email", "full_name"},
		},
		{
			path:  "/users/list",
			shape: "a service assigned to a variable first (svc := …; svc.Pagination)",
			want:  []string{"items", "page", "limit", "total"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			item := doc.Paths[tt.path]
			if item == nil || item.Get == nil {
				t.Fatalf("document has no GET %s", tt.path)
			}
			media, found := item.Get.Responses["200"].Content[contentTypeJSON]
			if !found {
				t.Fatalf("GET %s documents no JSON reply", tt.path)
			}

			body, ok := media.Example.(map[string]any)
			if !ok {
				t.Fatalf("GET %s: example is %#v, not an object", tt.path, media.Example)
			}
			if len(body) == 0 {
				t.Fatalf("GET %s renders an empty example: %s did not resolve", tt.path, tt.shape)
			}
			for _, field := range tt.want {
				if _, found := body[field]; !found {
					t.Errorf("GET %s: example has no %q field (got %v)", tt.path, field, propertyNames(media.Schema))
				}
			}
			if len(media.Schema.Properties) == 0 {
				t.Errorf("GET %s: schema states no properties, so the panel shows a bare object", tt.path)
			}
		})
	}
}

// TestOpenAPITagsAreAllGrouped is the one that matters about x-tagGroups: a
// renderer that reads the extension shows *only* what the groups list, so a tag
// left out of every group takes its endpoints out of the sidebar with it. They
// are still in the document, still reachable, and completely invisible — which
// is the kind of bug nobody reports because it looks like the endpoint was
// never written.
func TestOpenAPITagsAreAllGrouped(t *testing.T) {
	doc := fixtureDocument(t)

	if len(doc.TagGroups) == 0 {
		t.Fatal("fixture produced no tag groups, so this test proved nothing")
	}

	grouped := map[string]int{}
	for _, group := range doc.TagGroups {
		if len(group.Tags) == 0 {
			t.Errorf("group %q lists no tags, which renders as an empty folder", group.Name)
		}
		for _, tag := range group.Tags {
			grouped[tag]++
		}
	}

	for _, tag := range doc.Tags {
		switch grouped[tag.Name] {
		case 1:
		case 0:
			t.Errorf("tag %q belongs to no group; its endpoints disappear from the sidebar", tag.Name)
		default:
			t.Errorf("tag %q is in %d groups; it renders once per group", tag.Name, grouped[tag.Name])
		}
	}

	declared := map[string]struct{}{}
	for _, tag := range doc.Tags {
		declared[tag.Name] = struct{}{}
	}
	for path, item := range doc.Paths {
		for _, op := range operationsOf(item) {
			for _, tag := range op.Tags {
				if _, found := declared[tag]; !found {
					t.Errorf("%s carries tag %q, which the document never declares", path, tag)
				}
			}
		}
	}
}

// TestOpenAPITagNesting pins where an operation lands, since the whole point of
// the grouping is that a reader finds a sub-resource without reading paths.
func TestOpenAPITagNesting(t *testing.T) {
	doc := fixtureDocument(t)

	tagOf := func(path, method string) string {
		t.Helper()
		item := doc.Paths[path]
		if item == nil {
			t.Fatalf("no path %s", path)
		}
		for _, op := range operationsOf(item) {
			if strings.HasPrefix(op.Summary, method+" ") {
				return op.Tags[0]
			}
		}
		t.Fatalf("no %s %s", method, path)
		return ""
	}

	// Two static segments below the resource is a sub-resource, and gets its
	// own entry under the resource's folder.
	assert := func(got, want string) {
		t.Helper()
		if got != want {
			t.Errorf("tag = %q, want %q", got, want)
		}
	}
	assert(tagOf("/users/{id}/sessions/active", "GET"), "sessions / active")
	// One segment below is still the resource itself — a folder holding a
	// single endpoint is noise.
	assert(tagOf("/users/search", "GET"), "user")

	group := map[string]string{}
	for _, g := range doc.TagGroups {
		for _, tag := range g.Tags {
			group[tag] = g.Name
		}
	}
	if group["sessions / active"] != "user" {
		t.Errorf("sub-resource sits in group %q, want it under %q", group["sessions / active"], "user")
	}
	if group["user"] != "user" {
		t.Errorf("the resource's own endpoints sit in group %q, want %q", group["user"], "user")
	}
}

// TestUnloadedRelationsAreNotInTheExample is the guard on the difference
// between what a model can hold and what an endpoint sends.
//
// A GORM relation is tagged `json:"...,omitempty"`, and an endpoint that does
// not preload it sends nothing for it — no key at all. Rendering it as a
// populated array documents a reply the server never sends, and whoever trusts
// the example writes a client that reads a field that is not there.
func TestUnloadedRelationsAreNotInTheExample(t *testing.T) {
	doc := fixtureDocument(t)

	item := doc.Paths["/users/{id}"]
	if item == nil || item.Get == nil {
		t.Fatal("fixture has no GET /users/{id}")
	}
	media, found := item.Get.Responses["200"].Content[contentTypeJSON]
	if !found {
		t.Fatal("GET /users/{id} documents no JSON reply")
	}
	body, ok := media.Example.(map[string]any)
	if !ok {
		t.Fatalf("example is %#v, not an object", media.Example)
	}

	address, ok := body["address"].(map[string]any)
	if !ok {
		t.Fatalf("example has no nested address to check; got %v", propertyNames(media.Schema))
	}
	province, ok := address["province"].(map[string]any)
	if !ok {
		t.Fatalf("address carries no province; got %v", address)
	}

	if _, found := province["addresses"]; found {
		t.Error(`province states "addresses", which is tagged omitempty and is absent ` +
			`from a reply that did not preload it`)
	}
	if _, found := province["districts"]; !found {
		t.Error(`province omits "districts", which carries no omitempty and is therefore ` +
			`in the reply whether or not it was loaded`)
	}
}

// TestOmitEmptyOnlyDropsWhatJSONDrops guards the rule itself. encoding/json
// applies omitempty to a nil pointer, an empty slice and an empty map, and to
// nothing else — dropping a scalar the handler may well have set would take
// real fields out of the document.
func TestOmitEmptyOnlyDropsWhatJSONDrops(t *testing.T) {
	tests := []struct {
		name   string
		goType string
		tag    string
		want   bool
	}{
		{"slice with omitempty", "[]Thing", `json:"rel,omitempty"`, true},
		{"map with omitempty", "map[string]Thing", `json:"rel,omitempty"`, true},
		{"pointer to struct with omitempty", "*Thing", `json:"rel,omitempty"`, true},
		{"slice without omitempty", "[]Thing", `json:"rel"`, false},
		{"pointer to scalar with omitempty", "*string", `json:"rel,omitempty"`, false},
		{"scalar with omitempty", "string", `json:"rel,omitempty"`, false},
		{"struct with omitempty", "Thing", `json:"rel,omitempty"`, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := parseOneField(t, tt.goType, tt.tag)
			if got := absentWhenEmpty(field, bodyTags); got != tt.want {
				t.Errorf("absentWhenEmpty(Rel %s `%s`) = %v, want %v",
					tt.goType, tt.tag, got, tt.want)
			}
		})
	}
}

// parseOneField builds a one-field struct and hands back that field, tag and
// all. The tag is written between backticks as real source does, since that is
// the only quoting the reader strips.
func parseOneField(t *testing.T, goType, tag string) structField {
	t.Helper()

	const tick = "`"
	src := "package p\ntype S struct {\n\tRel " + goType + " " + tick + tag + tick + "\n}\n"
	file, err := parser.ParseFile(token.NewFileSet(), "s.go", src, 0)
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}

	spec := file.Decls[0].(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
	fields := structFields(spec.Type.(*ast.StructType))
	if len(fields) != 1 {
		t.Fatalf("parsed %d fields from %q, want 1", len(fields), src)
	}
	return fields[0]
}

// TestMaxDepthBoundsTheDocument is the guard on the failure that made a real
// service's document 425 MB: refusing to expand a type already on the current
// path stops the recursion but not the growth, because a model graph with no
// cycle along any single path still has combinatorially many paths.
//
// It asserts the shape of the growth rather than a size, since the fixture is
// small: each extra level has to cost something and the cost has to stop when
// the cap says so.
func TestMaxDepthBoundsTheDocument(t *testing.T) {
	sizeAt := func(depth int) int {
		t.Helper()

		cfg := testConfig()
		cfg.MaxDepth = depth
		repo := &repoInfo{root: fixtureRoot, moduleName: fixtureModule}
		files, err := loadGoFiles(repo.root, cfg)
		if err != nil {
			t.Fatalf("load fixture: %v", err)
		}
		res := newResolver(repo, files, cfg)
		routes := buildRoutes(repo, files, buildControllerMethodRegistry(repo, files, cfg), cfg)
		doc, err := buildOpenAPI(routes, res, cfg)
		if err != nil {
			t.Fatalf("build openapi: %v", err)
		}
		data, err := json.Marshal(doc)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return len(data)
	}

	shallow, deep := sizeAt(1), sizeAt(4)
	if shallow >= deep {
		t.Errorf("depth 1 produced %d bytes and depth 4 produced %d; the cap is not being applied",
			shallow, deep)
	}
	// Past the fixture's own nesting the number stops mattering, which is what
	// says the walk is bounded by the models rather than running until it hits
	// the cap.
	if a, b := sizeAt(8), sizeAt(16); a != b {
		t.Errorf("depth 8 gave %d bytes and depth 16 gave %d; the walk is still growing "+
			"past the depth of the fixture's own models", a, b)
	}
}

// A stated max_depth of zero renders every nested object as {}, which reads as
// an API that returns nothing. It is refused rather than honoured.
func TestMaxDepthRejectsZero(t *testing.T) {
	for _, depth := range []int{0, -1} {
		cfg := DefaultConfig()
		cfg.MaxDepth = depth
		if err := cfg.validate(); err == nil {
			t.Errorf("max_depth %d was accepted; it documents every reply as empty", depth)
		}
	}

	cfg := DefaultConfig()
	if cfg.MaxDepth < 1 {
		t.Errorf("the default max_depth is %d, which validate would reject", cfg.MaxDepth)
	}
	if err := cfg.validate(); err != nil {
		t.Errorf("the defaults do not validate: %v", err)
	}
}

// TestOpenAPIFormatConfig covers the switch a project flips when it wants only
// one of the two files, since a run that writes the wrong one is only noticed
// when the panel serves a stale spec.
func TestOpenAPIFormatConfig(t *testing.T) {
	tests := []struct {
		format  string
		postman bool
		openAPI bool
		valid   bool
	}{
		{format: formatBoth, postman: true, openAPI: true, valid: true},
		{format: formatPostman, postman: true, openAPI: false, valid: true},
		{format: formatOpenAPI, postman: false, openAPI: true, valid: true},
		{format: "yaml", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.Format = tt.format

			err := cfg.validate()
			if tt.valid && err != nil {
				t.Fatalf("format %q rejected: %v", tt.format, err)
			}
			if !tt.valid {
				if err == nil {
					t.Fatalf("format %q accepted; an unknown format must fail before a run writes nothing", tt.format)
				}
				return
			}

			if cfg.writesPostman() != tt.postman {
				t.Errorf("writesPostman() = %v, want %v", cfg.writesPostman(), tt.postman)
			}
			if cfg.writesOpenAPI() != tt.openAPI {
				t.Errorf("writesOpenAPI() = %v, want %v", cfg.writesOpenAPI(), tt.openAPI)
			}
		})
	}
}

// TestOpenAPIPathSyntax covers the rewrite from echo's route syntax, which is
// the one transformation a reader would notice immediately if it were wrong.
func TestOpenAPIPathSyntax(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/notes", "/notes"},
		{"/notes/:id", "/notes/{id}"},
		{"/v1/users/:userID/notes/:id", "/v1/users/{userID}/notes/{id}"},
		{"", "/"},
	}
	for _, tt := range tests {
		if got := openAPIPath(tt.in); got != tt.want {
			t.Errorf("openAPIPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// --- helpers ---

func operationsOf(item *oasPathItem) []*oasOperation {
	all := []*oasOperation{item.Get, item.Post, item.Put, item.Patch, item.Delete, item.Head, item.Option}
	ops := make([]*oasOperation, 0, len(all))
	for _, op := range all {
		if op != nil {
			ops = append(ops, op)
		}
	}
	return ops
}

func jsonBodySchema(t *testing.T, doc *oasDocument, path string, pick func(*oasPathItem) *oasOperation) *oasSchema {
	t.Helper()

	item := doc.Paths[path]
	if item == nil {
		t.Fatalf("document has no path %s", path)
	}
	op := pick(item)
	if op == nil {
		t.Fatalf("path %s has no operation for the requested method", path)
	}
	if op.RequestBody == nil {
		t.Fatalf("%s states no request body", path)
	}
	media, found := op.RequestBody.Content[contentTypeJSON]
	if !found {
		t.Fatalf("%s has no %s body", path, contentTypeJSON)
	}
	if media.Schema == nil {
		t.Fatalf("%s body states no schema", path)
	}
	return media.Schema
}

func propertyNames(schema *oasSchema) []string {
	names := make([]string, 0, len(schema.Properties))
	for name := range schema.Properties {
		names = append(names, name)
	}
	return names
}
