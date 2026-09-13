package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// ILogger is the structured logger. Same name/verbs as v1 so callers do not
// relearn, but it is backed by the standard library's log/slog and uses
// slog-style structured arguments (msg, key, value, key, value, ...).
type ILogger interface {
	Debug(msg string, args ...any)
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
	Error(msg string, args ...any)
	// With returns a child logger with the given attributes pinned.
	With(args ...any) ILogger
	// Slog exposes the underlying *slog.Logger for advanced use.
	Slog() *slog.Logger
}

type logger struct {
	l *slog.Logger
	// ctx is the request/run context the logger is bound to. It is passed to
	// every record so context-aware handlers (trace correlation, Sentry
	// breadcrumbs) see the unit of work the line belongs to.
	ctx context.Context
	// source records where each line was written. When it is off the program
	// counter is never even captured: the cost belongs to the line, not to the
	// handler that would have rendered it.
	source bool
	// skip counts the frames between the call site and this logger, for an
	// ILogger that delegates to it. See callerSkip.
	skip int
	// appSource blames the application frame that caused the line rather than
	// the framework frame that wrote it. See blameApp.
	appSource bool
}

var _ ILogger = (*logger)(nil)

// NewLogger builds an ILogger from configuration: JSON output by default, a
// human-friendly text handler when LogSimple is set, at the configured level.
func NewLogger(env IENV) ILogger {
	return NewLoggerTo(os.Stdout, env)
}

// NewLoggerTo is NewLogger writing to a specific destination (used in tests).
func NewLoggerTo(w io.Writer, env IENV) ILogger {
	var level slog.Level
	if env != nil {
		level = parseLevel(env.Config().LogLevel)
	}
	source := wantSource(env)
	opts := &slog.HandlerOptions{Level: level, AddSource: source, ReplaceAttr: compactSource}

	var h slog.Handler
	if env != nil && env.Config().LogSimple {
		// Colour is for a person reading a terminal. JSON is for a machine, and
		// escape codes would corrupt it — so it is never coloured.
		h = newPrettyHandler(w, level, wantColor(w, env), source)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return &logger{l: slog.New(h), source: source}
}

// NewLoggerSimple returns a readable text logger at info level (no config
// needed), coloured when stdout is a terminal.
func NewLoggerSimple() ILogger {
	h := newPrettyHandler(os.Stdout, slog.LevelInfo, wantColor(os.Stdout, nil), true)
	return &logger{l: slog.New(h), source: true}
}

// wantSource decides whether lines carry the file and line that wrote them.
//
// On by default: the first question asked of a log line is where it came from,
// and a service that has to be redeployed to answer it has already lost the
// incident. LOG_SOURCE=false turns it off for a service logging hot enough to
// care about a runtime.Callers per line.
func wantSource(env IENV) bool {
	// read as a string first: an unset bool field is indistinguishable from an
	// explicit false, and this key defaults to true
	if env != nil && env.String("log_source") != "" {
		return env.Bool("log_source")
	}
	return true
}

func (g *logger) Debug(msg string, args ...any) { g.log(slog.LevelDebug, msg, args...) }
func (g *logger) Info(msg string, args ...any)  { g.log(slog.LevelInfo, msg, args...) }
func (g *logger) Warn(msg string, args ...any)  { g.log(slog.LevelWarn, msg, args...) }
func (g *logger) Error(msg string, args ...any) { g.log(slog.LevelError, msg, args...) }

func (g *logger) log(level slog.Level, msg string, args ...any) {
	ctx := g.context()
	if !g.l.Enabled(ctx, level) {
		return
	}
	// the record is assembled here rather than through slog.Logger.Log, which
	// would record a frame inside this file: what a reader wants is the line
	// that wrote the message. It also means no program counter is taken at all
	// when the source is switched off — slog.Logger.Log captures one regardless.
	var pc uintptr
	if g.source {
		if g.appSource {
			pc = appCaller()
		}
		if pc == 0 {
			var pcs [1]uintptr
			// skip runtime.Callers, this method, and the Debug/Info/Warn/Error above it
			runtime.Callers(3+g.skip, pcs[:])
			pc = pcs[0]
		}
	}
	r := slog.NewRecord(time.Now(), level, msg, pc)
	r.Add(args...)
	_ = g.l.Handler().Handle(ctx, r)
}

// callerSkip returns l attributing its lines n frames further up, for another
// ILogger that delegates to it. Without it a job's log line would be blamed on
// the framework file that tees it to the run log.
func callerSkip(l ILogger, n int) ILogger {
	base, ok := l.(*logger)
	if !ok {
		return l
	}
	out := *base
	out.skip += n
	return &out
}

// blameApp returns l attributing its lines to the application frame that caused
// them instead of to the framework frame that wrote them. It is for the loggers
// the framework runs on the application's behalf — the SQL a repository issued,
// the HTTP call a client made, the completion a handler asked for — where the
// writing frame is the same file and line for every service and every call, and
// so answers nothing.
//
// Lines the framework writes about itself (a server starting, a pool losing its
// primary) must not use it: there is no application frame behind those, and the
// framework's own file is the honest answer.
func blameApp(l ILogger) ILogger {
	base, ok := l.(*logger)
	if !ok {
		return l
	}
	out := *base
	out.appSource = true
	return &out
}

// machinery is every module this binary was built from: the framework, the
// drivers it wraps, and everything those pull in. What is left on a stack after
// removing these and the standard library is the service's own code.
//
// The application is identified by elimination rather than by matching the main
// module's import path, and the reason is `go run main.go`. Naming a *file*
// rather than a package builds the synthetic "command-line-arguments" package
// and leaves BuildInfo.Main.Path empty — so a service started that way, which is
// how they are started in development, would match nothing and every line would
// fall back. The dependency list is filled in either way.
//
// It is also why this is not a hand-written list of packages to skip: that has
// to grow a line for every driver the framework ever wraps, and the day one is
// missing it silently blames that driver's internals.
var machinery = readMachinery()

func readMachinery() map[string]struct{} {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	return machineryOf(bi.Deps)
}

func machineryOf(deps []*debug.Module) map[string]struct{} {
	out := make(map[string]struct{}, len(deps))
	for _, dep := range deps {
		// a module with no resolved version was built from source here rather
		// than downloaded, so it is not machinery. `go run main.go` is why this
		// matters: the file becomes its own main package and the service's real
		// module is demoted to a dependency, listed as "(devel)" — without this
		// the service's own code would be skipped as if it were a driver
		if dep != nil && dep.Version != "" && dep.Version != "(devel)" {
			out[dep.Path] = struct{}{}
		}
	}
	return out
}

// mainModule is the import path of the module that built this binary, when the
// build knows it. It only has to catch what the standard-library test cannot: a
// module whose path has no dot in its first element (`module backend`), which is
// otherwise indistinguishable from "net/http".
var mainModule = readMainModule()

func readMainModule() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	return bi.Main.Path
}

// frameworkPkg is this package's own import path. It has to be excluded
// explicitly because when mine-core's own tests run, mine-core *is* the main
// module and every framework frame would look like application code.
var frameworkPkg = reflect.TypeFor[logger]().PkgPath()

// appCaller returns the program counter of the innermost frame belonging to the
// application, or 0 when the stack holds none — a driver's background goroutine,
// a migration run at boot — so the caller can fall back to its own frame.
//
// A frame count cannot do this job: the driver's stack between the call and the
// log line is a different depth for every statement. Walking costs more than
// taking one program counter, but it is paid only on a line that is actually
// written, and LOG_SOURCE=false still switches the whole thing off.
//
// 32 frames is the search: everything between the log call and the application
// is framework and driver, which is a dozen or so even through gorm — a stack
// deeper than this has nothing of the service near enough to be the answer.
func appCaller() uintptr {
	if len(machinery) == 0 && mainModule == "" {
		// nothing to recognise the service by, either way round
		return 0
	}
	var pcs [32]uintptr
	// 3: runtime.Callers, this function, and logger.log which is the only caller
	n := runtime.Callers(3, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	for {
		frame, more := frames.Next()
		if isAppFrame(frame.Function) {
			return frame.PC
		}
		if !more {
			return 0
		}
	}
}

// isAppFrame reports whether a fully qualified function name is the service's
// own code.
func isAppFrame(function string) bool { return appFrameIn(function, machinery, mainModule) }

// appFrameIn takes what it eliminates against explicitly, so the rule can be
// exercised as a consuming service sees it — where mine-core is a dependency
// rather than the module being built.
func appFrameIn(function string, deps map[string]struct{}, module string) bool {
	pkg := pkgOf(function)
	switch {
	// a linked binary has exactly one package main, and it is the service's
	case pkg == "main":
		return true
	// mine-core is in deps for a consumer, but not when its own tests are what
	// is being built — so it is named rather than left to the list
	case underPkg(pkg, frameworkPkg):
		return false
	case underModules(pkg, deps):
		return false
	// a bare prefix, not a path boundary: the external test package of a package
	// at a module's root is "…/v2_test", and it is that module's own code
	case module != "" && strings.HasPrefix(pkg, module):
		return true
	default:
		// with no dependency list to eliminate against, everything unrecognised
		// would look like the service — including gorm. A test binary is built
		// that way (the toolchain records no modules for one), and there the
		// module prefix above is the only thing that can identify the service.
		return len(deps) > 0 && !isStdlibPkg(pkg)
	}
}

// pkgOf extracts the import path from a fully qualified function name:
// "gorm.io/gorm/callbacks.BuildQuerySQL" gives "gorm.io/gorm/callbacks", and
// "main.main" gives "main".
func pkgOf(function string) string {
	// an import path may itself contain dots ("gorm.io"), so the function name
	// begins at the first dot *after* the last slash
	start := strings.LastIndexByte(function, '/') + 1
	if i := strings.IndexByte(function[start:], '.'); i >= 0 {
		return function[:start+i]
	}
	return function
}

// underModules reports whether a package belongs to one of the modules, trying
// each parent path in turn: a module path is a prefix of its packages, and
// "gorm.io/gorm/callbacks" is the module "gorm.io/gorm".
func underModules(pkg string, modules map[string]struct{}) bool {
	for p := pkg; p != ""; {
		if _, ok := modules[p]; ok {
			return true
		}
		i := strings.LastIndexByte(p, '/')
		if i < 0 {
			return false
		}
		p = p[:i]
	}
	return false
}

// isStdlibPkg reports whether an import path is in the standard library. Every
// other path begins with a domain, which carries a dot; the standard library's
// never do.
func isStdlibPkg(pkg string) bool {
	first := pkg
	if i := strings.IndexByte(pkg, '/'); i >= 0 {
		first = pkg[:i]
	}
	return !strings.Contains(first, ".")
}

// underPkg reports whether an import path lives under another. What follows the
// prefix must begin a new path element, so that neither ".../v2_test" — the
// external test package — nor a neighbouring ".../v2foo" is mistaken for
// ".../v2" code.
func underPkg(pkg, prefix string) bool {
	if prefix == "" || !strings.HasPrefix(pkg, prefix) {
		return false
	}
	return len(pkg) == len(prefix) || pkg[len(prefix)] == '/'
}

// compactSource renders the call site as "dir/file.go:line". slog's own default
// is a three-field object holding an absolute path — which is long, repeated on
// every line, and describes the machine that built the binary rather than the
// service that is running.
func compactSource(groups []string, a slog.Attr) slog.Attr {
	if len(groups) != 0 || a.Key != slog.SourceKey {
		return a
	}
	src, ok := a.Value.Any().(*slog.Source)
	if !ok || src.File == "" {
		// no program counter was recorded: drop the key rather than print an
		// empty one
		return slog.Attr{}
	}
	return slog.String(slog.SourceKey, shortSource(src.File, src.Line))
}

// shortSource keeps the last directory for context ("repository/user.go:42"):
// bare file names collide across packages, and full paths bury the name.
func shortSource(file string, line int) string {
	// runtime reports slash-separated paths on every platform, so this is path,
	// not filepath
	dir, base := path.Split(file)
	return path.Join(path.Base(path.Clean(dir)), base) + ":" + strconv.Itoa(line)
}

func (g *logger) context() context.Context {
	if g.ctx == nil {
		return context.Background()
	}
	return g.ctx
}

func (g *logger) With(args ...any) ILogger {
	out := *g
	out.l = g.l.With(args...)
	return &out
}

func (g *logger) Slog() *slog.Logger { return g.l }

// withContext returns a logger bound to ctx: its records carry the request id
// and the trace id (when a Sentry transaction is running), and they reach the
// context's Sentry hub as breadcrumbs.
func (g *logger) withContext(ctx context.Context) ILogger {
	if ctx == nil {
		return g
	}
	copied := *g
	copied.ctx = ctx
	out := &copied
	attrs := make([]any, 0, 4)
	if rid, ok := ctx.Value(requestIDKey).(string); ok && rid != "" {
		attrs = append(attrs, slog.String("request_id", rid))
	}
	attrs = append(attrs, traceAttrs(ctx)...)
	if len(attrs) == 0 {
		return out
	}
	out.l = g.l.With(attrs...)
	return out
}

// jsonValue renders a composite value — a struct, a map, a slice — as compact
// JSON, and says whether it did.
//
// Logging a payload whole is a reasonable thing to want, and Go's own rendering
// of one drops every field name: `{u-1 a@b.co hunter2}` tells the reader
// nothing about which value is which. The JSON handler already marshals these
// properly; this is what lets the text output and Sentry agree with it.
//
// Anything that can render itself (an error, a Stringer, a time) is left alone:
// it has an opinion about how it reads, and JSON would throw it away.
func jsonValue(v slog.Value) (string, bool) {
	if v.Kind() != slog.KindAny {
		return "", false
	}
	value := v.Any()
	switch value.(type) {
	case nil, error, fmt.Stringer, []byte, json.RawMessage:
		return "", false
	}

	rv := reflect.ValueOf(value)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", false
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
	default:
		return "", false
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// boundTo returns l writing under ctx — its hub, its trace, its breadcrumbs —
// without pinning any attributes of its own. It is for the framework's own
// middleware, which already has the request id and the status in the fields it
// is about to log and would otherwise print them twice.
func boundTo(l ILogger, ctx context.Context) ILogger {
	base, ok := l.(*logger)
	if !ok || ctx == nil {
		return l
	}
	out := *base
	out.ctx = ctx
	return &out
}

// withRequestID returns ctx carrying the request id, so every log line and
// Sentry event of the request is correlated by it.
func withRequestID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey, id)
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// requestIDKey is the context key under which the HTTP layer stores the request id.
type ctxKeyRequestID struct{}

var requestIDKey = ctxKeyRequestID{}
