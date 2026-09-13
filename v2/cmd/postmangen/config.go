package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// configFileName is the file a project states its own settings in, read from the
// repository root. It is optional: without it every value below is a default
// that suits a service scaffolded from the standard template.
const configFileName = "postmangen.json"

// Config is what a project says about itself — the parts of a collection only
// the project can name, and the naming conventions its routes and handlers
// follow.
//
// What is deliberately not here: how a handler is wrapped (WithHTTPContext), how
// a request is bound and validated (Bind/BindWithValidate, the valid package's
// rule chains), how a page is read off the query (GetPageOptions) and what an
// error body looks like. Those are contracts of the framework itself, so they
// are fixed — a project that changed them would no longer be describing the
// server it runs.
type Config struct {
	// Name is what Postman shows the collection as.
	Name string `json:"name"`
	// ID is the collection's stable _postman_id. Left empty, it is derived from
	// the module name, which keeps re-imports of the same service updating one
	// collection rather than piling up copies.
	ID string `json:"id"`
	// Description is the blurb on the collection; the layout is appended to it.
	Description string `json:"description"`
	// BaseURL is the default value of the {{baseUrl}} collection variable.
	BaseURL string `json:"base_url"`
	// Output is where the collection is written, relative to the repository root.
	Output string `json:"output"`
	// Layout is "grouped" or "flat"; the -layout flag overrides it.
	Layout string `json:"layout"`

	// Format is which files a run writes: "postman", "openapi" or "both".
	//
	// Both by default. The OpenAPI document is what the apidocs panel serves, so
	// a project that mounts the panel needs it on every regeneration; leaving it
	// off by default would mean a panel that quietly documents last month's API
	// until somebody notices the flag.
	Format string `json:"format"`
	// OpenAPIOutput is where the OpenAPI document is written, relative to the
	// repository root.
	OpenAPIOutput string `json:"openapi_output"`
	// APIVersion is the info.version of the OpenAPI document. It describes the
	// API, not this tool, so a project that versions its API states it here.
	APIVersion string `json:"api_version"`

	// MaxDepth is how many objects deep a rendered body goes.
	//
	// It is a setting rather than a constant because output roughly doubles per
	// level and the right number depends on the model graph, which only the
	// project knows: a service whose models point at each other — a province
	// holding districts, a district holding its province — has no cycle along
	// any single path and expands combinatorially, while one with flat DTOs can
	// afford to go deeper than the default and read better for it.
	//
	// Raise it when a reply is cut off before it has said anything; lower it
	// when the generated file is too large to load.
	MaxDepth int `json:"max_depth"`

	// RouteFileSuffixes name the files routes are registered in. Only these are
	// scanned for route calls, so an HTTP client's own Get is never mistaken for
	// a route.
	//
	// Two by default: the ".http.go" a module has always written its routes in,
	// and the ".module.go" a core.IModule declares them in.
	RouteFileSuffixes []string `json:"route_file_suffixes"`

	// RouteFileSuffix is the single-suffix form, kept for a project that
	// narrowed it. Set, it replaces the list entirely.
	//
	// Deprecated: use RouteFileSuffixes.
	RouteFileSuffix string `json:"route_file_suffix"`
	// HandlerSuffixes are the endings of a type that holds route handlers, which
	// is how `c := &handler.NoteHandler{}` is recognised as a controller and
	// `svc := service.NewUserService(c)` is not.
	HandlerSuffixes []string `json:"handler_suffixes"`
	// AuthMiddlewares names the middleware a route or group applies when it
	// requires a token. Names are matched exactly.
	//
	// A middleware that lets an anonymous request through — AuthOptional and its
	// like — belongs nowhere near this list: a route carrying it needs no token
	// in the collection.
	AuthMiddlewares []string `json:"auth_middlewares"`

	// AuthVariable is the collection variable an authenticated request sends as
	// its bearer token.
	AuthVariable string `json:"auth_variable"`
	// TokenCaptures maps a sign-in route to the collection variable that stores
	// the token it returns, so the requests after it authenticate themselves. A
	// second sign-in endpoint belongs here with its own variable.
	TokenCaptures map[string]string `json:"token_captures"`

	// SkipDirs are never walked: they hold no routes and their contents (vendor
	// trees, fixtures) are expensive or unparsable.
	SkipDirs []string `json:"skip_dirs"`
}

// DefaultConfig is the configuration of a service laid out the way the standard
// template lays one out.
func DefaultConfig() Config {
	return Config{
		Name:        "API Collection (Generated)",
		Description: "Generated from registered Go routes and request structs.",
		BaseURL:     "http://localhost:3000",
		Output:      "data/postman_collection.generated.json",
		Layout:      layoutGrouped,

		Format:        formatBoth,
		OpenAPIOutput: "data/openapi.generated.json",
		APIVersion:    defaultAPIVersion,
		MaxDepth:      defaultMaxDepth,

		RouteFileSuffixes: []string{".http.go", ".module.go"},
		HandlerSuffixes:   []string{"Handler", "Controller"},
		// AuthRequire and AuthRole are what a module writes on each route
		// (`e.GET("/notes", c.List, middlewares.AuthRequire(e))`). The rest are
		// older spellings, kept so a service built from an earlier version of the
		// template still generates a usable collection.
		AuthMiddlewares: []string{
			"AuthRequire", "AuthRole",
			"Required", "RequireRole", "Auth", "AuthMiddleware",
		},

		AuthVariable:  "authToken",
		TokenCaptures: map[string]string{"/auth/login": "authToken"},

		SkipDirs: []string{".git", ".agent", ".agents", "node_modules", "vendor", "testdata"},
	}
}

// loadConfig reads the project's settings, falling back to the defaults for
// everything it does not state. A path given explicitly must exist; the one
// looked for at the repository root need not.
func loadConfig(root, path string) (Config, error) {
	cfg := DefaultConfig()

	explicit := path != ""
	if !explicit {
		path = filepath.Join(root, configFileName)
	}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist) && !explicit:
		return cfg, nil
	case err != nil:
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}

	// Unmarshalling over the defaults leaves an absent key at its default, so a
	// project states only what it wants to change.
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// routeFileSuffixes resolves the two fields: a project that set the singular
// one meant to narrow the scan, so it wins over a list it never wrote.
func (c *Config) routeFileSuffixes() []string {
	if c.RouteFileSuffix != "" {
		return []string{c.RouteFileSuffix}
	}
	return c.RouteFileSuffixes
}

// isRouteFile reports whether this file is scanned for route registrations.
func (c *Config) isRouteFile(relPath string) bool {
	for _, suffix := range c.routeFileSuffixes() {
		if suffix != "" && strings.HasSuffix(relPath, suffix) {
			return true
		}
	}
	return false
}

func (c *Config) validate() error {
	switch c.Layout {
	case layoutGrouped, layoutFlat:
	default:
		return fmt.Errorf("invalid layout %q: expected %q or %q", c.Layout, layoutGrouped, layoutFlat)
	}
	switch c.Format {
	case formatPostman, formatOpenAPI, formatBoth:
	default:
		return fmt.Errorf("invalid format %q: expected %q, %q or %q",
			c.Format, formatPostman, formatOpenAPI, formatBoth)
	}
	if strings.TrimSpace(c.Name) == "" {
		return errors.New("collection name is empty")
	}
	// The defaults seed MaxDepth, and an absent key keeps what it seeded — so a
	// value below one here was stated, and stating it renders every nested
	// object as {}, which reads as an API that returns nothing.
	if c.MaxDepth < 1 {
		return fmt.Errorf("max_depth %d must be at least 1", c.MaxDepth)
	}

	if c.writesPostman() {
		if err := validateOutput("output", c.Output); err != nil {
			return err
		}
	}
	if c.writesOpenAPI() {
		if err := validateOutput("openapi_output", c.OpenAPIOutput); err != nil {
			return err
		}
	}
	return nil
}

func validateOutput(name, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%s path is empty", name)
	}
	if filepath.IsAbs(path) {
		return fmt.Errorf("%s %q must be relative to the repository root", name, path)
	}
	return nil
}

func (c *Config) writesPostman() bool {
	return c.Format == formatPostman || c.Format == formatBoth
}

func (c *Config) writesOpenAPI() bool {
	return c.Format == formatOpenAPI || c.Format == formatBoth
}

// depth is how deep a body is rendered. Zero means the project left it unset,
// which is different from a project asking for zero — and asking for zero would
// render every reply as an empty object, so it is refused by validate rather
// than honoured here.
func (c *Config) depth() int {
	if c.MaxDepth <= 0 {
		return defaultMaxDepth
	}
	return c.MaxDepth
}

// apiVersion is what the document says the API is at. An empty value is a
// missing required field in OpenAPI rather than an omission, so it falls back
// rather than emitting a spec no validator accepts.
func (c *Config) apiVersion() string {
	if version := strings.TrimSpace(c.APIVersion); version != "" {
		return version
	}
	return defaultAPIVersion
}

// collectionID is the identity Postman files the collection under. A project
// that does not name one gets a name derived from its module, so two services
// generated side by side never overwrite each other on import.
func (c *Config) collectionID(moduleName string) string {
	if c.ID != "" {
		return c.ID
	}
	if moduleName == "" {
		return "postmangen-collection"
	}

	parts := strings.Split(moduleName, "/")
	name := parts[len(parts)-1]
	if len(parts) > 1 && isVersionSegment(name) {
		name = parts[len(parts)-2]
	}
	return name + "-generated"
}

// variables are the collection variables every request can reach: the base URL,
// the bearer token, and any further token a sign-in route captures.
func (c *Config) variables() []postmanVariable {
	names := map[string]struct{}{}
	if c.AuthVariable != "" {
		names[c.AuthVariable] = struct{}{}
	}
	for _, name := range c.TokenCaptures {
		if name != "" {
			names[name] = struct{}{}
		}
	}

	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)

	variables := []postmanVariable{{Key: "baseUrl", Value: c.BaseURL, Type: "string"}}
	for _, name := range sorted {
		variables = append(variables, postmanVariable{Key: name, Value: "", Type: "string"})
	}
	return variables
}

// isSkippedDir reports whether a directory is one the walk never enters.
func (c *Config) isSkippedDir(name string) bool {
	return slices.Contains(c.SkipDirs, name)
}

// isHandlerTypeName reports whether a type name is one that holds route
// handlers.
func (c *Config) isHandlerTypeName(typeName string) bool {
	for _, suffix := range c.HandlerSuffixes {
		if strings.HasSuffix(typeName, suffix) {
			return true
		}
	}
	return false
}
