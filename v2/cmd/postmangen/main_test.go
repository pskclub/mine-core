package main

import (
	"encoding/json"
	"flag"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// update rewrites the golden file instead of comparing against it:
//
//	go test ./cmd/generate_postman_collection -update
var update = flag.Bool("update", false, "rewrite the golden collection")

const (
	fixtureRoot   = "testdata/fixture"
	fixtureModule = "example.com/fixture"
	goldenPath    = "testdata/collection.golden.json"
)

// testConfig is the defaults, which is what a service laid out the way the
// standard template lays one out gets. The golden file is therefore also a
// statement about what the tool does with no configuration at all.
func testConfig() *Config {
	cfg := DefaultConfig()
	return &cfg
}

// TestGenerateCollection is the guard on the whole pipeline. Every heuristic the
// generator applies — group prefixes, inherited middleware, handlers passed as
// method values, request rules, embedded and generic types — shows up in the
// golden file, so changing one without meaning to fails here.
func TestGenerateCollection(t *testing.T) {
	got := generateFixtureCollection(t)

	if *update {
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to create it): %v", err)
	}
	// The golden file is compared by content, not by bytes: a checkout with
	// core.autocrlf on hands back CRLF for a file committed with LF, and the
	// collection this test builds is always LF. Without this the test can only
	// pass on the platform the golden file happened to be written on.
	if normalizeEOL(got) != normalizeEOL(want) {
		t.Errorf("collection differs from %s; rerun with -update to accept\n--- got ---\n%s", goldenPath, got)
	}
}

func normalizeEOL(data []byte) string {
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func generateFixtureCollection(t *testing.T) []byte {
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
	collection, err := buildCollection(routes, res, repo, cfg)
	if err != nil {
		t.Fatalf("build collection: %v", err)
	}

	data, err := json.MarshalIndent(collection, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(data, '\n')
}

// TestRoutesFromFixture states the route facts in the open, so a failure names
// what broke instead of pointing at a diff.
func TestRoutesFromFixture(t *testing.T) {
	cfg := testConfig()
	repo := &repoInfo{root: fixtureRoot, moduleName: fixtureModule}
	files, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	routes := buildRoutes(repo, files, buildControllerMethodRegistry(repo, files, cfg), cfg)

	byKey := map[string]routeInfo{}
	for _, route := range routes {
		byKey[route.Method+" "+route.Path] = route
	}

	tests := []struct {
		key            string
		wantAuth       bool
		wantPagination bool
		wantRequest    string
		wantParams     []string
		wantHandlerPkg string
	}{
		{key: "GET /users", wantAuth: true, wantPagination: true, wantRequest: "UserSearch",
			wantHandlerPkg: "modules/user"},
		{key: "GET /users/:id", wantAuth: true, wantParams: []string{"id"},
			wantHandlerPkg: "modules/user"},
		{key: "POST /users", wantAuth: true, wantRequest: "UserCreate",
			wantHandlerPkg: "modules/user"},
		// Bound through an alias declared in the module, which resolves to the
		// shared declaration and to its query parameters with it.
		{key: "GET /users/search", wantAuth: true, wantRequest: "UserFilter",
			wantHandlerPkg: "modules/user"},
		{key: "POST /users/import", wantAuth: true, wantRequest: "UserImport",
			wantHandlerPkg: "modules/user"},
		{key: "POST /users/feedback", wantAuth: true, wantHandlerPkg: "modules/user"},
		// A second controller in the same package, built with its dependency
		// passed in. What these two prove lives in the golden files: the reply
		// resolves through a field and through a local variable, neither of
		// which the route table itself can show.
		{key: "GET /users/me", wantAuth: true, wantHandlerPkg: "modules/user"},
		{key: "GET /users/list", wantAuth: true, wantPagination: true,
			wantHandlerPkg: "modules/user"},
		// Two static segments below the resource, which is what puts an endpoint
		// in a folder of its own rather than beside the rest.
		{key: "GET /users/:id/sessions/active", wantAuth: true, wantPagination: true,
			wantParams: []string{"id"}, wantHandlerPkg: "modules/user"},
		// The nested group composes both prefixes and inherits the middleware
		// applied to its parent.
		{key: "DELETE /users/admin/:id", wantAuth: true, wantParams: []string{"id"},
			wantHandlerPkg: "modules/user"},
		// Registered on the server itself, so no prefix and no middleware.
		{key: "GET /status", wantHandlerPkg: "modules/user"},
		// Both probes come from one core.RegisterHealthRoutes call in cmd/api.go,
		// which names neither a method nor a path — and is not a route file.
		{key: "GET /healthz", wantHandlerPkg: "cmd"},
		{key: "GET /readyz", wantHandlerPkg: "cmd"},
		// The same liveness handler mounted by hand, at this service's own path.
		{key: "GET /live", wantHandlerPkg: "modules/user"},
		// The style the project writes: the whole path on the route, the guard
		// applied per route rather than inherited from a group, and the
		// controller in a sub-package. Nothing carries a prefix here, so a
		// generator that only understood groups would drop both of these — and
		// one that read the handler's package off the routes file would find no
		// request struct and generate a body-less POST.
		{key: "GET /notes", wantAuth: true, wantPagination: true,
			wantHandlerPkg: "modules/note/handler"},
		{key: "POST /notes", wantAuth: true, wantRequest: "CreateRequest",
			wantHandlerPkg: "modules/note/handler"},
		// Nothing bound: the path parameter is stated only by the c.Param call
		// the handler makes.
		{key: "POST /notes/:id/attachments", wantAuth: true, wantParams: []string{"id"},
			wantHandlerPkg: "modules/note/handler"},
		{key: "GET /notes/search", wantHandlerPkg: "modules/note/handler"},
	}

	if len(routes) != len(tests) {
		t.Errorf("got %d routes, want %d: %v", len(routes), len(tests), keysOf(byKey))
	}

	for _, tt := range tests {
		route, found := byKey[tt.key]
		if !found {
			t.Errorf("route %q not found; got %v", tt.key, keysOf(byKey))
			continue
		}
		if route.NeedsAuth != tt.wantAuth {
			t.Errorf("%s: NeedsAuth = %v, want %v", tt.key, route.NeedsAuth, tt.wantAuth)
		}
		if route.UsesPagination != tt.wantPagination {
			t.Errorf("%s: UsesPagination = %v, want %v", tt.key, route.UsesPagination, tt.wantPagination)
		}
		gotRequest := ""
		if route.RequestType != nil {
			gotRequest = route.RequestType.TypeName
		}
		if gotRequest != tt.wantRequest {
			t.Errorf("%s: RequestType = %q, want %q", tt.key, gotRequest, tt.wantRequest)
		}
		if strings.Join(route.PathParams, ",") != strings.Join(tt.wantParams, ",") {
			t.Errorf("%s: PathParams = %v, want %v", tt.key, route.PathParams, tt.wantParams)
		}
		if route.HandlerPackage != tt.wantHandlerPkg {
			t.Errorf("%s: HandlerPackage = %q, want %q", tt.key, route.HandlerPackage, tt.wantHandlerPkg)
		}
	}
}

// TestSampleValuesFollowRules covers the promise that a generated body is one
// the server would accept: the value comes from the rule, not from a guess.
func TestSampleValuesFollowRules(t *testing.T) {
	repo := &repoInfo{root: fixtureRoot, moduleName: fixtureModule}
	cfg := testConfig()
	files, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	res := newResolver(repo, files, cfg)

	info := res.structAt(fixtureModule+"/requests", "UserCreate")
	if info == nil {
		t.Fatal("UserCreate not found in registry")
	}
	raw, ok := buildRequestBody(info, res)
	if !ok {
		t.Fatal("no request body built")
	}

	var body map[string]any
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}

	if got := body["email"]; got != sampleEmail {
		t.Errorf("email = %v, want %v", got, sampleEmail)
	}
	// In("ADMIN", "MEMBER") — the first option is one the server accepts.
	if got := body["role"]; got != "ADMIN" {
		t.Errorf("role = %v, want ADMIN", got)
	}
	// Length(minPasswordLength, 72): the minimum is a named constant, so this
	// also covers resolving it to 8.
	password, _ := body["password"].(string)
	if len(password) < 8 {
		t.Errorf("password = %q, shorter than the 8 the rule requires", password)
	}
}

// TestRequestSignals covers the inputs a handler states in code rather than in
// a struct tag. Everything here is a request the endpoint accepts and the
// generator would otherwise have documented as accepting nothing.
func TestRequestSignals(t *testing.T) {
	const src = `package p

func (h Handler) Upload(c Context) error {
	_, _ = c.FormFile("file")
	_, _ = c.Cookie("session_id")
	_ = c.Request().Header.Get("X-Api-Key")
	_ = c.FormValue("is_public")
	_ = c.FormValueOr("caption", "untitled")
	_ = c.QueryParam("q")
	_ = c.QueryParamOr("limit", "20")
	_ = c.Request().URL.Query().Get("tag")
	_ = c.Param("id")

	// Not request values: echo's per-request store, a header the server sends,
	// and a method of something that is not the context.
	_ = c.Get("user")
	c.Response().Header().Get("X-Trace")
	_ = other.QueryParam("nope")
	_ = other.Param("nope")

	return nil
}`

	signals := collectRequestSignals(parseFunc(t, src))

	if got := sampleNames(signals.Query); strings.Join(got, ",") != "limit,q,tag" {
		t.Errorf("Query = %v, want limit,q,tag", got)
	}
	if got := sampleNames(signals.Form); strings.Join(got, ",") != "caption,is_public" {
		t.Errorf("Form = %v, want caption,is_public", got)
	}
	if strings.Join(signals.Files, ",") != "file" {
		t.Errorf("Files = %v, want file", signals.Files)
	}
	if strings.Join(signals.Headers, ",") != "X-Api-Key" {
		t.Errorf("Headers = %v, want X-Api-Key", signals.Headers)
	}
	if strings.Join(signals.Cookies, ",") != "session_id" {
		t.Errorf("Cookies = %v, want session_id", signals.Cookies)
	}
	if strings.Join(signals.Params, ",") != "id" {
		t.Errorf("Params = %v, want id", signals.Params)
	}
	if !signals.Multipart {
		t.Error("Multipart = false; a FormFile makes the body multipart")
	}

	// The fallback an ...Or form states is a value the server itself would use.
	for _, tt := range []struct {
		samples    []namedSample
		name, want string
	}{
		{signals.Query, "limit", "20"},
		{signals.Form, "caption", "untitled"},
	} {
		if got := sampleByName(tt.samples, tt.name).sampleValue(); got != tt.want {
			t.Errorf("%s sample = %q, want %q", tt.name, got, tt.want)
		}
	}
	// No fallback stated: the name is all there is, and it is enough.
	if got := sampleByName(signals.Query, "q").sampleValue(); got != sampleString {
		t.Errorf("q sample = %q, want %q", got, sampleString)
	}
}

// TestRequestSignalsIgnoreNonHandlers guards the one thing that makes reading
// method names safe: a call only counts when it is made on the handler's own
// context.
func TestRequestSignalsIgnoreNonHandlers(t *testing.T) {
	const src = `package p

func (s service) Find(id string) error {
	_ = s.repo.Param("id")
	_ = s.repo.QueryParam("q")
	return nil
}`

	signals := collectRequestSignals(parseFunc(t, src))
	if len(signals.Params) != 0 || len(signals.Query) != 0 {
		t.Errorf("service method produced signals: %+v", signals)
	}
}

// TestAliasResolvesToDeclaration is the guard on a request bound through a name
// that stands for another type. Both spellings compile and bind identically, so
// both have to document the same fields — a lookup that stopped at struct
// declarations dropped every parameter such an endpoint takes.
func TestAliasResolvesToDeclaration(t *testing.T) {
	repo := &repoInfo{root: fixtureRoot, moduleName: fixtureModule}
	cfg := testConfig()
	files, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	res := newResolver(repo, files, cfg)

	info := res.structAt(fixtureModule+"/modules/user", "UserFilter")
	if info == nil {
		t.Fatal("UserFilter did not resolve; an alias has to be followed to what it names")
	}
	if info.TypeName != "UserSearch" {
		t.Errorf("resolved to %q, want UserSearch", info.TypeName)
	}

	// The rules live on the declaration, so they have to survive the hop too.
	queries := buildQueryParams(info, res, routeInfo{})
	if len(queries) != 2 {
		t.Fatalf("query params = %v, want keyword and status", queries)
	}
	if queries[1].Value != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE from its In rule", queries[1].Value)
	}
}

// TestFormBodyShape covers a handler that binds a struct and takes a file: one
// multipart body carrying both, with each field under the name the form sends
// it as.
func TestFormBodyShape(t *testing.T) {
	repo := &repoInfo{root: fixtureRoot, moduleName: fixtureModule}
	cfg := testConfig()
	files, err := loadGoFiles(repo.root, cfg)
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	res := newResolver(repo, files, cfg)

	info := res.structAt(fixtureModule+"/requests", "UserImport")
	if info == nil {
		t.Fatal("UserImport not found in registry")
	}

	body, contentType := buildBody(info, res, requestSignals{Multipart: true, Files: []string{"file"}})
	if body == nil || body.Mode != bodyModeFormData {
		t.Fatalf("body = %+v, want a formdata body", body)
	}
	// Postman writes a multipart Content-Type itself: it is the only one that
	// knows the boundary.
	if contentType != "" {
		t.Errorf("Content-Type = %q, want it left to Postman", contentType)
	}

	got := map[string]postmanFormParam{}
	for _, param := range body.FormData {
		got[param.Key] = param
	}
	// `form:"mode"` beats `json:"import_mode"`: a form sends the form's name.
	if _, found := got["mode"]; !found {
		t.Errorf("form fields = %v, want one named mode", keysOfParams(got))
	}
	if _, found := got["import_mode"]; found {
		t.Error("field named by its json tag; a form body uses the form tag")
	}
	// A field with no form tag of its own still has the one name it states.
	if got["note"].Type != "text" {
		t.Errorf("note = %+v, want a text field", got["note"])
	}
	if got["file"].Type != "file" {
		t.Errorf("file = %+v, want a file field", got["file"])
	}
}

// TestFormBodyWithoutFileIsURLEncoded covers the other half: form values and no
// upload is a url-encoded body, which is what the handler will actually parse.
func TestFormBodyWithoutFileIsURLEncoded(t *testing.T) {
	signals := requestSignals{Form: []namedSample{{Name: "subject"}, {Name: "rating", Default: "5"}}}

	body, contentType := buildBody(nil, nil, signals)
	if body == nil || body.Mode != bodyModeURLEncoded {
		t.Fatalf("body = %+v, want a urlencoded body", body)
	}
	if contentType != contentTypeForm {
		t.Errorf("Content-Type = %q, want %q", contentType, contentTypeForm)
	}
	if len(body.URLEncoded) != 2 {
		t.Errorf("fields = %v, want subject and rating", body.URLEncoded)
	}
}

func TestSampleTextForName(t *testing.T) {
	tests := []struct{ name, want string }{
		{"limit", "10"},
		{"page", "1"},
		{"is_public", "true"},
		{"has_children", "true"},
		{"email", sampleEmail},
		{"q", sampleString},
	}
	for _, tt := range tests {
		if got := sampleTextForName(tt.name); got != tt.want {
			t.Errorf("sampleTextForName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestSortedHeadersKeepsOne covers a route that both requires a token and reads
// the header itself: sending Authorization twice is not what either meant.
func TestSortedHeadersKeepsOne(t *testing.T) {
	headers := sortedHeaders([]postmanHeader{
		{Key: "X-Api-Key", Value: "string"},
		{Key: "Authorization", Value: "Bearer {{authToken}}"},
		{Key: "Authorization", Value: "string"},
	})

	if len(headers) != 2 {
		t.Fatalf("headers = %v, want two", headers)
	}
	if headers[0].Key != "Authorization" || headers[0].Value != "Bearer {{authToken}}" {
		t.Errorf("headers[0] = %+v, want the bearer token kept", headers[0])
	}
}

func parseFunc(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), "handler.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return file.Decls[0].(*ast.FuncDecl)
}

func sampleNames(samples []namedSample) []string {
	names := make([]string, 0, len(samples))
	for _, sample := range samples {
		names = append(names, sample.Name)
	}
	return names
}

func sampleByName(samples []namedSample, name string) namedSample {
	for _, sample := range samples {
		if sample.Name == name {
			return sample
		}
	}
	return namedSample{}
}

func keysOfParams(m map[string]postmanFormParam) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func TestJoinRoutePath(t *testing.T) {
	tests := []struct{ prefix, path, want string }{
		{"", "", "/"},
		{"/users", "", "/users"},
		{"/users", "/:id", "/users/:id"},
		{"/users/", "/:id", "/users/:id"},
		{"", "/healthz", "/healthz"},
		{"/users", "bulk", "/users/bulk"},
	}
	for _, tt := range tests {
		if got := joinRoutePath(tt.prefix, tt.path); got != tt.want {
			t.Errorf("joinRoutePath(%q, %q) = %q, want %q", tt.prefix, tt.path, got, tt.want)
		}
	}
}

func TestSampleStringForName(t *testing.T) {
	tests := []struct{ name, want string }{
		{"email", sampleEmail},
		{"user_email", sampleEmail},
		{"full_name", "Jane Doe"},
		{"name", "Sample name"},
		{"id", sampleUUID},
		{"user_id", sampleUUID},
		{"whatever", sampleString},
	}
	for _, tt := range tests {
		if got := sampleStringForName(tt.name); got != tt.want {
			t.Errorf("sampleStringForName(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestStatusCode(t *testing.T) {
	tests := []struct {
		src      string
		want     int
		wantFine bool
	}{
		{src: "http.StatusOK", want: 200, wantFine: true},
		{src: "http.StatusNoContent", want: 204, wantFine: true},
		{src: "201", want: 201, wantFine: true},
		{src: "http.StatusTeapot", wantFine: false},
		{src: `"200"`, wantFine: false},
	}
	for _, tt := range tests {
		expr, err := parser.ParseExpr(tt.src)
		if err != nil {
			t.Fatalf("parse %q: %v", tt.src, err)
		}
		got, ok := statusCode(expr)
		if ok != tt.wantFine || (ok && got != tt.want) {
			t.Errorf("statusCode(%s) = %d, %v; want %d, %v", tt.src, got, ok, tt.want, tt.wantFine)
		}
	}
}

func TestEncodeQueryValue(t *testing.T) {
	tests := []struct{ in, want string }{
		{"plain", "plain"},
		{"a,b", "a,b"},
		{"{{baseUrl}}", "{{baseUrl}}"},
		{"a b", "a+b"},
		{"a&b", "a%26b"},
	}
	for _, tt := range tests {
		if got := encodeQueryValue(tt.in); got != tt.want {
			t.Errorf("encodeQueryValue(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestExtractRules(t *testing.T) {
	const src = `package p
func (r *T) Valid(v *V) {
	v.Str("email", r.Email).Required().Email()
	v.Str("role", r.Role).In("ADMIN", "MEMBER")
}`

	file, err := parser.ParseFile(token.NewFileSet(), "rules.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	fn := file.Decls[0].(*ast.FuncDecl)

	rules := extractRules(fn.Body, nil)
	email, found := rules["Email"]
	if !found {
		t.Fatalf("no rules for Email; got %v", keysOfRules(rules))
	}
	if email.ReportedName != "email" {
		t.Errorf("ReportedName = %q, want email", email.ReportedName)
	}
	if !email.has("Required") || !email.has("Email") {
		t.Errorf("Email rules = %v, want Required and Email", email.Rules)
	}
	if role := rules["Role"]; role == nil || !role.has("In") {
		t.Errorf("Role rules missing In: %v", role)
	}
}

func TestPackageNameFromImportPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"github.com/pskclub/mine-core/v2", "mine-core"},
		{"github.com/pskclub/mine-core/v2/valid", "valid"},
		{"strings", "strings"},
	}
	for _, tt := range tests {
		if got := packageNameFromImportPath(tt.in); got != tt.want {
			t.Errorf("packageNameFromImportPath(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestOutputPathIsCreated covers the directory being made on demand: the
// generator has to work in a checkout where data/ was never committed.
func TestOutputPathIsCreated(t *testing.T) {
	root := t.TempDir()
	output := DefaultConfig().Output
	if err := writeCollection(root, output, &postmanCollection{}); err != nil {
		t.Fatalf("writeCollection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(output))); err != nil {
		t.Errorf("collection not written: %v", err)
	}
}

func keysOf(m map[string]routeInfo) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func keysOfRules(m map[string]*fieldRules) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
