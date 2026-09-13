package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigDefaults covers the case every service starts in: no config
// file, so the tool has to work on the conventions alone.
func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(t.TempDir(), "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Layout != layoutGrouped {
		t.Errorf("Layout = %q, want %q", cfg.Layout, layoutGrouped)
	}
	if cfg.Output != DefaultConfig().Output {
		t.Errorf("Output = %q, want the default", cfg.Output)
	}
}

// TestLoadConfigOverlaysDefaults is the promise that a project states only what
// it wants to change: a file naming two keys leaves the rest as they were.
func TestLoadConfigOverlaysDefaults(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, filepath.Join(root, configFileName), `{
		"name": "Wallet API",
		"base_url": "https://api.example.com"
	}`)

	cfg, err := loadConfig(root, "")
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if cfg.Name != "Wallet API" {
		t.Errorf("Name = %q, want Wallet API", cfg.Name)
	}
	if cfg.BaseURL != "https://api.example.com" {
		t.Errorf("BaseURL = %q, want the one from the file", cfg.BaseURL)
	}
	if len(cfg.AuthMiddlewares) != len(DefaultConfig().AuthMiddlewares) {
		t.Errorf("AuthMiddlewares = %v, want the defaults left alone", cfg.AuthMiddlewares)
	}
	if !cfg.isRouteFile("modules/note/note.http.go") || !cfg.isRouteFile("modules/note/note.module.go") {
		t.Errorf("route file suffixes = %v, want both defaults left alone", cfg.routeFileSuffixes())
	}
}

// TestRouteFileSuffixes covers the two spellings together: the list is the
// default, and a project that narrowed the scan with the older singular key
// must keep the narrower scan rather than silently getting the wider one back.
func TestRouteFileSuffixes(t *testing.T) {
	cfg := DefaultConfig()
	if got := cfg.routeFileSuffixes(); len(got) != 2 {
		t.Fatalf("default suffixes = %v, want .http.go and .module.go", got)
	}
	if cfg.isRouteFile("modules/note/note.service.go") {
		t.Error("a service file must not be scanned for routes: its client's Get is not a route")
	}

	cfg.RouteFileSuffix = ".routes.go"
	if cfg.isRouteFile("modules/note/note.http.go") {
		t.Error("the singular key is how a project narrows the scan; a list it never wrote must not widen it")
	}
	if !cfg.isRouteFile("modules/note/note.routes.go") {
		t.Error("the singular key must still select the file it names")
	}
}

// TestLoadConfigExplicitPathMustExist separates the two ways a path is arrived
// at: one the caller typed is a mistake when it is missing, the one looked for
// at the root is not.
func TestLoadConfigExplicitPathMustExist(t *testing.T) {
	root := t.TempDir()
	if _, err := loadConfig(root, filepath.Join(root, "nope.json")); err == nil {
		t.Error("missing explicit config accepted, want an error")
	}
	if _, err := loadConfig(root, ""); err != nil {
		t.Errorf("missing default config rejected: %v", err)
	}
}

func TestLoadConfigRejectsBrokenJSON(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, filepath.Join(root, configFileName), `{"name":`)

	if _, err := loadConfig(root, ""); err == nil {
		t.Error("broken config accepted, want an error")
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{name: "defaults", mutate: func(*Config) {}},
		{name: "flat layout", mutate: func(c *Config) { c.Layout = layoutFlat }},
		{name: "unknown layout", mutate: func(c *Config) { c.Layout = "tree" }, wantErr: true},
		{name: "empty output", mutate: func(c *Config) { c.Output = "" }, wantErr: true},
		{name: "absolute output", mutate: func(c *Config) { c.Output = absoluteOutput() }, wantErr: true},
		{name: "empty name", mutate: func(c *Config) { c.Name = " " }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(&cfg)
			if err := cfg.validate(); (err != nil) != tt.wantErr {
				t.Errorf("validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCollectionID covers the identity Postman files the collection under: two
// services generated side by side must not overwrite each other on import.
func TestCollectionID(t *testing.T) {
	tests := []struct {
		id     string
		module string
		want   string
	}{
		{id: "chosen-id", module: "example.com/wallet", want: "chosen-id"},
		{module: "example.com/acme/wallet", want: "wallet-generated"},
		// The module version suffix is part of the path, never the name.
		{module: "example.com/acme/wallet/v2", want: "wallet-generated"},
		{module: "", want: "postmangen-collection"},
	}

	for _, tt := range tests {
		cfg := DefaultConfig()
		cfg.ID = tt.id
		if got := cfg.collectionID(tt.module); got != tt.want {
			t.Errorf("collectionID(%q) with ID %q = %q, want %q", tt.module, tt.id, got, tt.want)
		}
	}
}

// TestVariablesCoverEveryCapturedToken is what keeps a second sign-in endpoint
// usable: the variable its script writes has to exist on the collection.
func TestVariablesCoverEveryCapturedToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.TokenCaptures = map[string]string{
		"/auth/login":       "authToken",
		"/admin/auth/login": "adminToken",
	}

	got := map[string]string{}
	for _, variable := range cfg.variables() {
		got[variable.Key] = variable.Value
	}

	if got["baseUrl"] != cfg.BaseURL {
		t.Errorf("baseUrl = %q, want %q", got["baseUrl"], cfg.BaseURL)
	}
	for _, key := range []string{"authToken", "adminToken"} {
		if _, found := got[key]; !found {
			t.Errorf("variable %q missing; got %v", key, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("got %d variables, want 3: %v", len(got), got)
	}
}

func TestIsHandlerTypeName(t *testing.T) {
	cfg := DefaultConfig()
	tests := []struct {
		name string
		want bool
	}{
		{"NoteHandler", true},
		{"UserController", true},
		{"UserService", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := cfg.isHandlerTypeName(tt.name); got != tt.want {
			t.Errorf("isHandlerTypeName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func writeConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// absoluteOutput is an absolute path on whichever platform the test runs.
func absoluteOutput() string {
	if abs, err := filepath.Abs("data/postman_collection.generated.json"); err == nil {
		return abs
	}
	return "/data/postman_collection.generated.json"
}
