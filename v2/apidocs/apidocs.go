// Package apidocs serves a service's own API reference from inside the service,
// as a page an engineer or an analyst can send requests from.
//
// It exists because the collection postmangen writes is only useful after
// somebody imports it: a file has to be regenerated, found, downloaded and
// loaded into a client, and the person who most needs it — the analyst checking
// whether staging does what the ticket says — is the one least likely to do all
// four. A page at a URL needs none of that, and it is always the API of the
// process actually running.
//
//	//go:embed data/openapi.generated.json
//	var spec []byte
//
//	srv := core.NewHTTPServer(app, nil)
//	apidocs.Mount(srv, apidocs.Options{Spec: spec})
//
// The document itself comes from `go tool postmangen`, which reads the routes
// and request structs the service already has, so the reference cannot describe
// an endpoint the service does not serve. Any other OpenAPI 3.x document works
// just as well — this package renders, it does not generate.
//
// Outside dev it refuses to mount without a guard, for the same reason devtools
// does: the reference names every endpoint, every field and every rule, which is
// a map of the attack surface written by the people who built it.
package apidocs

import (
	"bytes"
	"net/http"
	"os"
	"strings"

	scalargo "github.com/bdpiprava/scalar-go"
	"github.com/bdpiprava/scalar-go/model"
	"github.com/labstack/echo/v5"
	core "github.com/pskclub/mine-core/v2"
)

// DefaultPrefix is where the reference is mounted when Options.Prefix is empty.
// The underscore keeps it out of the way of any real API path, and matches
// devtools' "/_dev".
const DefaultPrefix = "/_docs"

// specPath is the sub-path the raw document is served at, relative to the mount
// prefix. It carries no extension because the document may be JSON or YAML and
// a URL that names the wrong one is worse than one that names neither.
const specPath = "/openapi"

// sameOriginServer is the server entry added to a document that names none. A
// reference with no server has no "Send" button at all, which turns the page
// back into the thing it replaced.
const sameOriginServer = "/"

// Scalar configuration keys this package sets by name because scalar-go exposes
// no option for them. They are Scalar's spelling, and a Scalar release that
// renames one makes the page ignore it rather than fail — so they are named here
// rather than written inline, where a rename would be a search across the file.
const (
	configOpenAllTags = "defaultOpenAllTags"
	configHideModels  = "hideModels"
)

// Options configures a mount.
type Options struct {
	// Prefix is the base path (default DefaultPrefix). The page is served there
	// and the raw document at "<prefix>/openapi".
	Prefix string

	// Spec is the OpenAPI document, in JSON or YAML. Embedding it is the usual
	// choice — the binary then carries the reference for exactly the code it
	// was built from, and there is no file to forget to deploy.
	//
	// One of Spec and SpecFile is required.
	Spec []byte

	// SpecFile is a path read once at mount time, for a deployment that ships
	// the document beside the binary. It is read at mount rather than per
	// request so a missing file fails at startup, where somebody sees it.
	SpecFile string

	// Title overrides the browser tab and page heading. Empty uses info.title
	// from the document, which is what the generator put there.
	Title string

	// Servers replaces the servers the document names. Use it when the document
	// was generated elsewhere and names a host this deployment is not — the
	// panel sends real requests, and the wrong base URL sends them somewhere
	// real.
	//
	// Empty leaves the document's own servers alone, except that a document
	// naming none gets the same origin as the page.
	Servers []Server

	// BasicAuth guards the page with a username and password the browser asks
	// for. See BasicAuth.
	//
	// It runs before Auth, and the two compose.
	BasicAuth *BasicAuth

	// Auth guards every route this mounts. It is ordinary echo middleware, so
	// whatever a service already uses to protect an admin area works here.
	//
	// One of this and BasicAuth is required outside dev: Mount fails rather than
	// publishing the API surface.
	Auth []echo.MiddlewareFunc

	// CDN is where the browser loads the Scalar bundle from. Empty uses
	// scalargo.DefaultCDN, which is jsdelivr.
	//
	// This is the one part of the page that is not served by the process, and on
	// a network with no egress it is the part that fails. A deployment behind a
	// firewall should host the bundle itself and name it here; the page says so
	// rather than rendering blank when the script never arrives.
	CDN string

	// DarkMode starts the page in dark mode. The reader can still toggle it.
	DarkMode bool

	// Reference renders the page as published documentation rather than as a
	// list of requests to try.
	//
	// The default is the second, because that is what this package is for: every
	// operation is listed in the sidebar at once and the schemas are left out of
	// it, so finding an endpoint is one click rather than opening the group it
	// lives in and scrolling past the models. Set this when the page is for
	// readers rather than for testers — an integration partner reading the API
	// wants the tags closed and the models listed.
	//
	// It changes nothing about what the page can do; both render the same
	// document and both send real requests.
	Reference bool

	// Scalar is passed straight to the renderer, after everything above. It is
	// the escape hatch for the options this struct does not name — theme,
	// layout, hidden clients, pre-filled credentials — without this package
	// having to grow a field per setting.
	//
	// Use ScalarConfig for a setting scalar-go itself does not expose.
	Scalar []scalargo.Option
}

// ScalarConfig sets one Scalar configuration key by name.
//
// scalar-go names an option per setting, and Scalar ships settings faster than
// the wrapper adopts them — "defaultOpenAllTags" is one this package needs and
// there is no WithDefaultOpenAllTags to call. Rather than wait on a release, or
// fork, this reaches the same map the named options write to.
//
//	apidocs.Mount(srv, apidocs.Options{
//	    Spec:   spec,
//	    Scalar: []scalargo.Option{apidocs.ScalarConfig("hideModels", false)},
//	})
//
// The key is Scalar's, not this package's: a misspelled one is ignored by the
// page rather than reported here, which is the price of not enumerating them.
func ScalarConfig(key string, value any) scalargo.Option {
	return func(o *scalargo.Options) {
		if o.Configurations == nil {
			o.Configurations = map[string]any{}
		}
		o.Configurations[key] = value
	}
}

// Server is one address the reference can send requests to.
type Server struct {
	URL         string
	Description string
}

// protected reports whether anything at all stands in front of the page.
func (o Options) protected() bool {
	return len(o.Auth) > 0 || o.BasicAuth != nil
}

// apidocs holds what the handlers serve. It is per-mount state, not package
// state: a process running two servers gets one of these each.
type apidocs struct {
	page []byte
	spec []byte
	// specType is the Content-Type the raw document is served under, decided
	// once from the bytes rather than from a file name that may not exist.
	specType string
}

// Mount registers the reference on srv and returns the error that stopped it,
// or nil.
//
// Everything that can fail — a missing document, an unreadable file, an
// unguarded mount outside dev — fails here rather than on the first request, so
// a broken reference is a failed deploy and not a page somebody finds later.
func Mount(srv *core.Server, opts Options) core.IError {
	if srv == nil {
		return core.New(http.StatusInternalServerError, "APIDOCS_NO_SERVER",
			"apidocs: a server is required")
	}
	app := srv.App()
	if app == nil {
		return core.New(http.StatusInternalServerError, "APIDOCS_NO_APP",
			"apidocs: the server has no app")
	}
	env := app.ENV()
	prefix := normalizePrefix(opts.Prefix)

	spec, ierr := loadSpec(opts)
	if ierr != nil {
		return ierr
	}

	// A BasicAuth with a user and no password is a lock with no key.
	if opts.BasicAuth != nil && opts.BasicAuth.Password == "" && env.String(EnvBasicAuthPassword) == "" {
		return core.New(http.StatusInternalServerError, "APIDOCS_NO_PASSWORD",
			"apidocs: BasicAuth needs a password — set it, or set APP_APIDOCS_PASSWORD")
	}
	basic := resolveBasicAuth(opts, env)

	// The guard is checked at startup, where somebody reads the refusal, rather
	// than on a request nobody makes until it is too late.
	if !env.IsDev() && len(opts.Auth) == 0 && basic == nil {
		return core.Newf(http.StatusForbidden, "APIDOCS_UNPROTECTED",
			"apidocs: refusing to mount at %s with APP_ENV=%s and no guard — "+
				"set APP_APIDOCS_PASSWORD, or pass Options.BasicAuth or Options.Auth",
			prefix, env.Config().ENV)
	}

	page, err := renderPage(opts, spec, prefix)
	if err != nil {
		return core.Newf(http.StatusInternalServerError, "APIDOCS_RENDER",
			"apidocs: cannot render the reference: %s", err.Error())
	}

	d := &apidocs{page: []byte(page), spec: spec, specType: specContentType(spec)}

	guards := opts.Auth
	if basic != nil {
		// Basic auth first: a wrong password should not reach a guard of the
		// service's own, which may do a database lookup per request.
		guards = append([]echo.MiddlewareFunc{basic.middleware()}, opts.Auth...)
	}

	g := srv.Group(prefix, guards...)
	// both spellings: a person types "/_docs", a link resolves against
	// "/_docs/", and neither should 404
	g.GET("", d.ui)
	g.GET("/", d.ui)
	g.GET(specPath, d.rawSpec)

	app.Log().Info("apidocs mounted",
		"prefix", prefix,
		"protected", opts.protected(),
		"basic_auth", basic != nil,
		"spec_bytes", len(spec))
	return nil
}

// loadSpec is the document this mount serves, from wherever the caller put it.
func loadSpec(opts Options) ([]byte, core.IError) {
	if len(opts.Spec) > 0 {
		return opts.Spec, nil
	}
	if opts.SpecFile == "" {
		return nil, core.New(http.StatusInternalServerError, "APIDOCS_NO_SPEC",
			"apidocs: an OpenAPI document is required — set Options.Spec or Options.SpecFile")
	}

	data, err := os.ReadFile(opts.SpecFile)
	if err != nil {
		return nil, core.Newf(http.StatusInternalServerError, "APIDOCS_SPEC_UNREADABLE",
			"apidocs: cannot read %s: %s", opts.SpecFile, err.Error())
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, core.Newf(http.StatusInternalServerError, "APIDOCS_SPEC_EMPTY",
			"apidocs: %s is empty", opts.SpecFile)
	}
	return data, nil
}

// renderPage builds the whole page once. The document cannot change while the
// process runs, so rendering per request would spend the same work on every
// reader to produce the same bytes.
func renderPage(opts Options, spec []byte, prefix string) (string, error) {
	scalarOpts := []scalargo.Option{
		scalargo.WithSpecBytes(spec),
		scalargo.WithSpecModifier(opts.specModifier()),
		// The reader is here to try the endpoints, and typing the token once per
		// visit rather than once per request is the difference between a panel
		// that gets used and one that gets abandoned.
		scalargo.WithPersistAuth(true),
		scalargo.WithCustomBodyJS(fallbackScript(prefix)),
	}
	if !opts.Reference {
		// The sidebar as a request list: every operation visible without opening
		// its group, and nothing in it that cannot be sent. Whoever opened this
		// page came to try an endpoint, and the shortest path to one is not
		// through a table of contents.
		scalarOpts = append(scalarOpts,
			ScalarConfig(configOpenAllTags, true),
			ScalarConfig(configHideModels, true),
		)
	}
	if opts.CDN != "" {
		scalarOpts = append(scalarOpts, scalargo.WithCDN(opts.CDN))
	}
	if opts.DarkMode {
		scalarOpts = append(scalarOpts, scalargo.WithDarkMode())
	}
	if opts.Title != "" {
		scalarOpts = append(scalarOpts, scalargo.WithMetaDataOpts(scalargo.WithTitle(opts.Title)))
	}
	// The caller's options come last so they win over everything decided here.
	scalarOpts = append(scalarOpts, opts.Scalar...)

	return scalargo.NewV2(scalarOpts...)
}

// specModifier settles which servers the page offers before it is rendered.
func (o Options) specModifier() scalargo.SpecModifier {
	return func(spec *model.Spec) *model.Spec {
		if spec == nil {
			return spec
		}
		if len(o.Servers) > 0 {
			servers := make([]model.Server, 0, len(o.Servers))
			for _, server := range o.Servers {
				entry := model.Server{URL: server.URL}
				if server.Description != "" {
					description := server.Description
					entry.Description = &description
				}
				servers = append(servers, entry)
			}
			spec.Servers = servers
			return spec
		}
		if len(spec.Servers) == 0 {
			description := "Same origin as this page"
			spec.Servers = []model.Server{{URL: sameOriginServer, Description: &description}}
		}
		return spec
	}
}

// fallbackScript shows a notice when the Scalar bundle never arrived.
//
// A blank page reads as a broken service, and the reader — often the one person
// least able to investigate it — has nothing to go on. This says what actually
// happened, no route to the CDN, and links to the document, which is served by
// this process and therefore reachable whenever the page itself was.
func fallbackScript(prefix string) string {
	link := prefix + specPath
	return `setTimeout(function(){` +
		`var app=document.getElementById('app');` +
		`if(!app||app.childNodes.length){return;}` +
		`app.innerHTML='<div style="font:14px system-ui;padding:24px;line-height:1.7">' +` +
		`'<strong>The API reference could not load.</strong><br>' +` +
		`'The Scalar bundle is fetched from a CDN and this network appears not to reach it. ' +` +
		`'Host the bundle and set Options.CDN, or read the document directly at ' +` +
		`'<a href="` + link + `">` + link + `</a>.</div>';` +
		`},4000);`
}

// normalizePrefix makes any of "_docs", "/_docs" and "/_docs/" mean the same
// thing.
func normalizePrefix(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return DefaultPrefix
	}
	p = "/" + strings.Trim(p, "/")
	if p == "/" {
		return DefaultPrefix
	}
	return p
}

// specContentType reads the document's format off its first meaningful byte,
// which is the only thing that is true of a document handed over as bytes.
func specContentType(spec []byte) string {
	if bytes.HasPrefix(bytes.TrimSpace(spec), []byte("{")) {
		return echo.MIMEApplicationJSON
	}
	return "application/yaml; charset=utf-8"
}

// ui serves the page itself.
func (d *apidocs) ui(c core.IHTTPContext) error {
	return c.Blob(http.StatusOK, echo.MIMETextHTMLCharsetUTF8, d.page)
}

// rawSpec serves the document unchanged, for the tools that want a URL rather
// than a page: a client generator, an import into somebody's own editor, a diff
// against what the last deploy served.
func (d *apidocs) rawSpec(c core.IHTTPContext) error {
	return c.Blob(http.StatusOK, d.specType, d.spec)
}
