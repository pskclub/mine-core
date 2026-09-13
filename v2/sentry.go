package core

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/getsentry/sentry-go/attribute"
)

// ---------------------------------------------------------------------------
// Public vocabulary
//
// The framework re-exports the handful of sentry-go types callers actually
// touch, so application code says core.LevelError / core.Breadcrumb and never
// has to import the SDK. The escape hatch (ISentry.Hub) is still there for the
// rare case that needs the raw client.
// ---------------------------------------------------------------------------

// Level is the severity of a captured event.
type Level = sentry.Level

const (
	LevelDebug   = sentry.LevelDebug
	LevelInfo    = sentry.LevelInfo
	LevelWarning = sentry.LevelWarning
	LevelError   = sentry.LevelError
	LevelFatal   = sentry.LevelFatal
)

// Breadcrumb is one step of the trail that led to an event. Breadcrumbs are
// recorded on the context's hub, so they are per-request/per-run — never shared
// between concurrent requests the way v1's global scope was.
type Breadcrumb struct {
	// Type is the rendering hint: "default", "http", "query", "navigation", "error".
	Type string
	// Category groups related crumbs, e.g. "http.client", "db", "mq".
	Category string
	Message  string
	Level    Level
	Data     map[string]any
}

// CheckInStatus is the state reported to a Sentry cron monitor.
type CheckInStatus = sentry.CheckInStatus

const (
	CheckInProgress = sentry.CheckInStatusInProgress
	CheckInOK       = sentry.CheckInStatusOK
	CheckInError    = sentry.CheckInStatusError
)

// CheckIn is one report to a Sentry cron monitor. The scheduler sends these
// automatically for jobs that have a Schedule (see SentryOptions.EnableCrons).
type CheckIn struct {
	// ID pairs a terminal check-in with the in-progress one that opened it.
	// Leave empty on the first report and pass back what CheckIn returned.
	ID       string
	Monitor  string
	Status   CheckInStatus
	Duration time.Duration
	// Schedule tells Sentry when the monitor is expected to run, so a missed run
	// raises an alert. Only cron expressions and intervals are understood.
	Schedule Schedule
	// MaxRuntime marks the run as failed if it does not finish in time.
	MaxRuntime time.Duration
	// CheckInMargin is the grace period before a late run counts as missed.
	CheckInMargin time.Duration
	// Timezone is the zone Schedule's hours are read in when the schedule does
	// not name one itself (CronIn does). Empty means UTC, as Sentry defaults.
	Timezone string
}

// ISpan is a unit of work on the performance timeline (a transaction when it is
// the outermost one). Finish it exactly once, passing the error the work ended
// with — or nil.
type ISpan interface {
	// Context returns the context carrying this span; pass it down so child work
	// attaches to it.
	Context() context.Context
	// TraceID is the id shared by every span, log line and Sentry event of this
	// unit of work.
	TraceID() string
	SetTag(key, value string)
	SetData(key string, value any)
	// Child starts a nested span (e.g. one query inside a request).
	Child(op, description string) ISpan
	// Finish closes the span, marking it failed when err is non-nil.
	Finish(err error)
	// Sentry exposes the underlying span (escape hatch); nil when disabled.
	Sentry() *sentry.Span
}

// ISentry is the error tracker. Like every other capability it is reached
// through the context — ctx.Sentry() returns a tracker already bound to the
// request/run, so captures land on that unit of work's own scope.
//
// It is never nil: with no DSN configured every method is a no-op, so callers
// never guard.
type ISentry interface {
	// Enabled reports whether events actually leave the process.
	Enabled() bool
	// WithContext binds the tracker to ctx (its hub, user, tags and breadcrumbs).
	WithContext(ctx context.Context) ISentry

	// CaptureError reports err with everything the bound context knows. Returns
	// the Sentry event id ("" when nothing was sent).
	CaptureError(err error, opts ...CaptureOption) string
	// CaptureMessage reports a standalone message.
	CaptureMessage(msg string, level Level, opts ...CaptureOption) string
	// Recover reports a recovered panic value at fatal level.
	Recover(panicValue any, opts ...CaptureOption) string

	// Breadcrumb records a step on the bound context's trail.
	Breadcrumb(b Breadcrumb)
	// SetTag pins an indexed tag on every later event of this context.
	SetTag(key, value string)
	// SetContextData pins a named block of structured data on this context.
	SetContextData(name string, data map[string]any)

	// StartTransaction opens a performance transaction bound to this context.
	StartTransaction(name, op string) ISpan
	// Meter records measurements on this context's trace. Never nil.
	Meter() IMeter
	// SendCheckIn reports to a cron monitor and returns the check-in id.
	SendCheckIn(c CheckIn) string

	// Flush blocks until queued events are sent, or timeout elapses.
	Flush(timeout time.Duration) bool
	// Close flushes and releases the transport.
	Close() IError
	// Hub exposes the underlying hub (escape hatch); nil when disabled.
	Hub() *sentry.Hub
}

// ---------------------------------------------------------------------------
// Configuration
// ---------------------------------------------------------------------------

// SentryOptions overrides what NewSentry reads from the environment. A zero
// field keeps the configured (or default) value; the *bool / *float64 fields
// exist so "false" and "0" can be told apart from "not set".
type SentryOptions struct {
	DSN         string
	Environment string
	Release     string
	ServerName  string

	Debug              *bool
	SampleRate         *float64
	TracesSampleRate   *float64
	TracesSampler      sentry.TracesSampler
	AttachStacktrace   *bool
	SendDefaultPII     *bool
	IgnoreErrors       []string
	IgnoreTransactions []string
	MaxBreadcrumbs     int

	// MinStatus is the lowest IError status that is reported automatically.
	// Default 500 — client mistakes are not incidents.
	MinStatus int
	// CaptureRetries reports every failed attempt of a job instead of only the
	// final one. Default false.
	CaptureRetries *bool
	// BreadcrumbLevel is the lowest log level turned into a breadcrumb
	// ("debug"|"info"|"warn"|"error"|"off"). Default "info".
	BreadcrumbLevel string
	// EnableTracing turns on performance transactions for requests and job runs.
	// Default false (tracing has its own quota).
	EnableTracing *bool
	// EnableCrons sends check-ins for scheduled jobs, so Sentry alerts on a run
	// that never happened. Default false — it creates monitors in your org.
	EnableCrons *bool
	// EnableLogs streams every log line to Sentry Logs, where it is searchable on
	// its own and correlated with the trace and the issue it belongs to. Default
	// false — logs have their own quota.
	EnableLogs *bool
	// EnableMetrics lets ctx.Meter() record counters, gauges and distributions.
	// Default false — metrics have their own quota.
	EnableMetrics *bool
	// LogLevel is the lowest level streamed to Sentry Logs
	// ("debug"|"info"|"warn"|"error"). Default: everything the logger emits, so
	// LOG_LEVEL alone decides — raise this only to send Sentry less than you
	// print.
	LogLevel string
	// CaptureRequestBody attaches the (scrubbed) request body of a failed
	// request. Default true.
	CaptureRequestBody *bool
	// MaxBodyBytes caps how much of a body is recorded. Default 16 KiB.
	MaxBodyBytes int
	// SendEnv attaches the (scrubbed) configuration to every event. Default true.
	SendEnv *bool

	// ScrubKeys extends the list of key fragments whose values are masked.
	ScrubKeys []string
	// ScrubValues masks these exact strings wherever they appear.
	ScrubValues []string

	// Tags are attached to every event of the process.
	Tags map[string]string
	// BeforeSend is the last hook before an event leaves; return nil to drop it.
	// It runs after the framework's scrubbing.
	BeforeSend func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event
	// BeforeSendLog is the last hook before a log line leaves; return nil to drop
	// it. It runs after the framework's scrubbing.
	BeforeSendLog func(log *sentry.Log) *sentry.Log
	// BeforeSendMetric is the last hook before a measurement leaves; return nil
	// to drop it. It runs after the framework's scrubbing.
	BeforeSendMetric func(metric *sentry.Metric) *sentry.Metric
	// Transport replaces the HTTP transport — this is what tests swap out.
	Transport sentry.Transport
	// FlushTimeout bounds Close/Flush. Default 5s.
	FlushTimeout time.Duration
}

// sentryConfig is the resolved configuration the framework reads at runtime.
type sentryConfig struct {
	minStatus       int
	captureRetries  bool
	breadcrumbLevel slog.Level
	breadcrumbsOff  bool
	tracing         bool
	crons           bool
	logs            bool
	logLevel        slog.Level
	metrics         bool
	captureBody     bool
	maxBodyBytes    int
	sendEnv         bool
	flushTimeout    time.Duration
	scrubber        *scrubber
}

func defaultSentryConfig() sentryConfig {
	return sentryConfig{
		minStatus:       http.StatusInternalServerError,
		breadcrumbLevel: slog.LevelInfo,
		logLevel:        slog.LevelDebug,
		captureBody:     true,
		maxBodyBytes:    16 << 10,
		sendEnv:         true,
		flushTimeout:    5 * time.Second,
		scrubber:        newScrubber(nil, nil),
	}
}

// sentryOf returns the runtime settings of s, falling back to the defaults for
// a tracker the framework did not build (a custom ISentry, or the no-op one).
func sentryOf(s ISentry) sentryConfig {
	if t, ok := s.(*sentryTracker); ok {
		return t.cfg
	}
	return defaultSentryConfig()
}

// ---------------------------------------------------------------------------
// Construction
// ---------------------------------------------------------------------------

// NewSentry builds the error tracker from configuration. With no DSN it returns
// a no-op tracker, so wiring is unconditional: every service can call
// ctx.Sentry() and only the DSN decides whether anything is sent.
//
//	sentryTracker, err := core.NewSentry(env)
//	app, err := core.NewApp(env, core.WithSentry(sentryTracker))
//
// NewApp does this for you when no tracker is supplied.
func NewSentry(env IENV, opts ...SentryOptions) (ISentry, IError) {
	var o SentryOptions
	if len(opts) > 0 {
		o = opts[0]
	}
	cfg := &ENVConfig{}
	if env != nil {
		cfg = env.Config()
	}

	dsn := firstNonEmpty(o.DSN, cfg.SentryDSN)
	if dsn == "" {
		return NewNoopSentry(), nil
	}

	rc := defaultSentryConfig()
	rc.minStatus = firstPositive(o.MinStatus, cfg.SentryMinStatus, http.StatusInternalServerError)
	rc.captureRetries = boolOr(o.CaptureRetries, cfg.SentryCaptureRetries)
	rc.tracing = boolOr(o.EnableTracing, cfg.SentryEnableTracing)
	rc.crons = boolOr(o.EnableCrons, cfg.SentryEnableCrons)
	rc.logs = boolOr(o.EnableLogs, cfg.SentryEnableLogs)
	rc.metrics = boolOr(o.EnableMetrics, cfg.SentryEnableMetrics)
	rc.logLevel = parseLogStreamLevel(firstNonEmpty(o.LogLevel, cfg.SentryLogLevel))
	rc.captureBody = boolOrDefault(o.CaptureRequestBody, env, "sentry_capture_body", true)
	rc.maxBodyBytes = firstPositive(o.MaxBodyBytes, cfg.SentryMaxBodyBytes, 16<<10)
	rc.sendEnv = boolOrDefault(o.SendEnv, env, "sentry_send_env", true)
	rc.flushTimeout = o.FlushTimeout
	if rc.flushTimeout <= 0 {
		rc.flushTimeout = durationOr(cfg.SentryFlushTimeout, 5*time.Second)
	}
	rc.breadcrumbLevel, rc.breadcrumbsOff = parseBreadcrumbLevel(
		firstNonEmpty(o.BreadcrumbLevel, cfg.SentryBreadcrumbLevel))
	rc.scrubber = newScrubber(o.ScrubKeys, append(o.ScrubValues, secretValuesOf(cfg)...))

	sampleRate := 1.0
	if o.SampleRate != nil {
		sampleRate = *o.SampleRate
	} else if cfg.SentrySampleRate > 0 {
		sampleRate = cfg.SentrySampleRate
	}
	tracesRate := 0.0
	if o.TracesSampleRate != nil {
		tracesRate = *o.TracesSampleRate
	} else if cfg.SentryTracesSampleRate > 0 {
		tracesRate = cfg.SentryTracesSampleRate
	}
	if rc.tracing && tracesRate <= 0 {
		tracesRate = 1.0
	}
	if tracesRate > 0 {
		rc.tracing = true
	}

	environment := firstNonEmpty(o.Environment, cfg.SentryEnvironment, cfg.ENV, "dev")
	tags := map[string]string{}
	for k, v := range o.Tags {
		tags[k] = v
	}
	if cfg.Service != "" {
		tags["service"] = cfg.Service
	}

	clientOpts := sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      environment,
		Release:          firstNonEmpty(o.Release, cfg.SentryRelease),
		ServerName:       firstNonEmpty(o.ServerName, cfg.SentryServerName),
		Debug:            boolOr(o.Debug, cfg.SentryDebug),
		SampleRate:       sampleRate,
		TracesSampleRate: tracesRate,
		EnableTracing:    rc.tracing,
		// the SDK enables logs and metrics the moment anything asks for a logger
		// or a meter, so opting out has to be explicit
		DisableLogs:        !rc.logs,
		DisableMetrics:     !rc.metrics,
		TracesSampler:      o.TracesSampler,
		AttachStacktrace:   boolOrDefault(o.AttachStacktrace, env, "sentry_attach_stacktrace", true),
		SendDefaultPII:     boolOr(o.SendDefaultPII, cfg.SentrySendDefaultPII),
		IgnoreErrors:       orStrings(o.IgnoreErrors, splitList(cfg.SentryIgnoreErrors)),
		IgnoreTransactions: orStrings(o.IgnoreTransactions, splitList(cfg.SentryIgnoreTransactions)),
		MaxBreadcrumbs:     firstPositive(o.MaxBreadcrumbs, cfg.SentryMaxBreadcrumbs, 50),
		Transport:          o.Transport,
		Tags:               tags,
		BeforeSend:         chainBeforeSend(rc.scrubber, o.BeforeSend),
		BeforeSendLog:      chainBeforeSendLog(rc.scrubber, o.BeforeSendLog),
		BeforeSendMetric:   chainBeforeSendMetric(rc.scrubber, o.BeforeSendMetric),
		BeforeSendTransaction: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			return rc.scrubber.scrubEvent(event)
		},
	}

	// the configuration is static, so it is attached once here rather than at
	// every capture — which also means *every* event carries it, whichever layer
	// reported it
	if rc.sendEnv && env != nil {
		clientOpts.BeforeSend = withConfigContext(rc.scrubber.scrubStrings(env.All()), clientOpts.BeforeSend)
	}

	client, err := sentry.NewClient(clientOpts)
	if err != nil {
		return nil, Wrap(err, "sentry: init")
	}
	hub := sentry.NewHub(client, sentry.NewScope())
	// the process-wide attributes every log line and metric inherits; release,
	// environment and server name are added by the SDK itself
	pinAttributes(hub, map[string]string{"service": cfg.Service})
	return &sentryTracker{
		client: client,
		hub:    hub,
		cfg:    rc,
	}, nil
}

// NewNoopSentry returns a tracker that discards everything. It is what services
// without a DSN get, so the capture path is identical in every environment.
func NewNoopSentry() ISentry { return noopSentry{} }

// ---------------------------------------------------------------------------
// Capture options
// ---------------------------------------------------------------------------

// CaptureOption tunes a single capture.
type CaptureOption func(*captureOptions)

type captureOptions struct {
	level       Level
	hasLevel    bool
	tags        map[string]string
	extras      map[string]any
	contexts    map[string]map[string]any
	fingerprint []string
}

// CaptureLevel overrides the severity derived from the error's status.
func CaptureLevel(l Level) CaptureOption {
	return func(o *captureOptions) { o.level, o.hasLevel = l, true }
}

// CaptureTag adds one indexed tag to this event.
func CaptureTag(key, value string) CaptureOption {
	return func(o *captureOptions) {
		if o.tags == nil {
			o.tags = map[string]string{}
		}
		o.tags[key] = value
	}
}

// CaptureTags adds several indexed tags to this event.
func CaptureTags(tags map[string]string) CaptureOption {
	return func(o *captureOptions) {
		if o.tags == nil {
			o.tags = map[string]string{}
		}
		for k, v := range tags {
			o.tags[k] = v
		}
	}
}

// CaptureExtra attaches a non-indexed value to this event.
func CaptureExtra(key string, value any) CaptureOption {
	return func(o *captureOptions) {
		if o.extras == nil {
			o.extras = map[string]any{}
		}
		o.extras[key] = value
	}
}

// CaptureContext attaches a named block of structured data to this event.
func CaptureContext(name string, data map[string]any) CaptureOption {
	return func(o *captureOptions) {
		if o.contexts == nil {
			o.contexts = map[string]map[string]any{}
		}
		o.contexts[name] = data
	}
}

// CaptureFingerprint overrides how Sentry groups this event into an issue.
// Include "{{ default }}" to keep the stack-trace grouping and only split on
// the extra parts.
func CaptureFingerprint(parts ...string) CaptureOption {
	return func(o *captureOptions) { o.fingerprint = parts }
}

func buildCaptureOptions(opts []CaptureOption) captureOptions {
	var o captureOptions
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return o
}

// ---------------------------------------------------------------------------
// Implementation
// ---------------------------------------------------------------------------

type sentryTracker struct {
	client *sentry.Client
	hub    *sentry.Hub
	cfg    sentryConfig
	ctx    context.Context
}

var _ ISentry = (*sentryTracker)(nil)

func (s *sentryTracker) Enabled() bool { return true }

func (s *sentryTracker) Hub() *sentry.Hub { return s.hubFor() }

func (s *sentryTracker) Meter() IMeter { return &meter{tracker: s, ctx: s.ctx} }

func (s *sentryTracker) WithContext(ctx context.Context) ISentry {
	if ctx == nil {
		return s
	}
	cp := *s
	cp.ctx = ctx
	return &cp
}

// hubFor returns the hub of the bound context — the per-request/per-run one
// installed by the HTTP middleware or the job runner — or the process hub.
func (s *sentryTracker) hubFor() *sentry.Hub {
	if s.ctx != nil {
		if hub := sentry.GetHubFromContext(s.ctx); hub != nil {
			return hub
		}
	}
	return s.hub
}

// icontext returns the framework context behind the bound context, when there
// is one; that is where the user, the scoped data and the mode come from.
func (s *sentryTracker) icontext() IContext {
	if s.ctx == nil {
		return nil
	}
	if ic, ok := s.ctx.(IContext); ok {
		return ic
	}
	return nil
}

func (s *sentryTracker) CaptureError(err error, opts ...CaptureOption) string {
	if err == nil {
		return ""
	}
	o := buildCaptureOptions(opts)
	if !o.hasLevel {
		o.level, o.hasLevel = levelForError(err), true
	}
	if o.fingerprint == nil {
		o.fingerprint = defaultFingerprint(err)
	}

	hub := s.hubFor()
	var id *sentry.EventID
	hub.WithScope(func(scope *sentry.Scope) {
		s.applyScope(scope, o)
		withReporterStacktrace(scope)
		id = hub.CaptureException(err)
	})
	return eventID(id)
}

func (s *sentryTracker) CaptureMessage(msg string, level Level, opts ...CaptureOption) string {
	if msg == "" {
		return ""
	}
	o := buildCaptureOptions(opts)
	if !o.hasLevel {
		o.level, o.hasLevel = level, true
	}
	hub := s.hubFor()
	var id *sentry.EventID
	hub.WithScope(func(scope *sentry.Scope) {
		s.applyScope(scope, o)
		withReporterStacktrace(scope)
		id = hub.CaptureMessage(msg)
	})
	return eventID(id)
}

func (s *sentryTracker) Recover(panicValue any, opts ...CaptureOption) string {
	if panicValue == nil {
		return ""
	}
	o := buildCaptureOptions(opts)
	if !o.hasLevel {
		o.level, o.hasLevel = LevelFatal, true
	}
	hub := s.hubFor()
	var id *sentry.EventID
	hub.WithScope(func(scope *sentry.Scope) {
		s.applyScope(scope, o)
		scope.SetTag("panic", "true")
		// a deferred call runs on the stack that panicked, so what is left after
		// the framework's own recovery frames is the line that panicked
		withReporterStacktrace(scope)
		id = hub.Recover(panicValue)
	})
	return eventID(id)
}

func (s *sentryTracker) Breadcrumb(b Breadcrumb) {
	crumb := &sentry.Breadcrumb{
		Type:      firstNonEmpty(b.Type, "default"),
		Category:  b.Category,
		Message:   b.Message,
		Level:     b.Level,
		Data:      s.cfg.scrubber.scrubAny(b.Data),
		Timestamp: time.Now(),
	}
	if crumb.Level == "" {
		crumb.Level = LevelInfo
	}
	s.hubFor().AddBreadcrumb(crumb, nil)
}

func (s *sentryTracker) SetTag(key, value string) {
	s.hubFor().Scope().SetTag(key, value)
}

func (s *sentryTracker) SetContextData(name string, data map[string]any) {
	s.hubFor().Scope().SetContext(name, sentry.Context(s.cfg.scrubber.scrubAny(data)))
}

func (s *sentryTracker) StartTransaction(name, op string) ISpan {
	if !s.cfg.tracing {
		return noopSpan{ctx: s.spanContext()}
	}
	span := sentry.StartTransaction(s.spanContext(), name, sentry.WithOpName(op))
	if span == nil {
		return noopSpan{ctx: s.spanContext()}
	}
	return &tracedSpan{span: span}
}

// spanContext is the context transactions are started on: the bound one, with
// the hub guaranteed to be on it so the transaction lands on this unit of work.
func (s *sentryTracker) spanContext() context.Context {
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if sentry.GetHubFromContext(ctx) == nil {
		ctx = sentry.SetHubOnContext(ctx, s.hubFor())
	}
	return ctx
}

func (s *sentryTracker) SendCheckIn(c CheckIn) string {
	if c.Monitor == "" {
		return ""
	}
	checkIn := &sentry.CheckIn{
		ID:          sentry.EventID(c.ID),
		MonitorSlug: monitorSlug(c.Monitor),
		Status:      c.Status,
		Duration:    c.Duration,
	}
	id := s.hubFor().CaptureCheckIn(checkIn, monitorConfig(c))
	return eventID(id)
}

func (s *sentryTracker) Flush(timeout time.Duration) bool {
	if timeout <= 0 {
		timeout = s.cfg.flushTimeout
	}
	return s.client.Flush(timeout)
}

func (s *sentryTracker) Close() IError {
	s.client.Flush(s.cfg.flushTimeout)
	s.client.Close()
	return nil
}

// applyScope puts everything the framework knows on the event's scope: who the
// user is, what request or run it belongs to, the scoped data, the (scrubbed)
// configuration, and whatever the call site added.
func (s *sentryTracker) applyScope(scope *sentry.Scope, o captureOptions) {
	if o.hasLevel {
		scope.SetLevel(o.level)
	}
	if ic := s.icontext(); ic != nil {
		scope.SetTag("mode", ic.Mode().String())
		if user := sentryUser(ic, ""); user != nil {
			scope.SetUser(*user)
		}
		if data := ic.GetAllData(); len(data) > 0 {
			scope.SetContext("context-data", sentry.Context(s.cfg.scrubber.scrubAny(data)))
		}
	}
	if s.ctx != nil {
		if rid, ok := s.ctx.Value(requestIDKey).(string); ok && rid != "" {
			scope.SetTag("request_id", rid)
		}
		if span := sentry.SpanFromContext(s.ctx); span != nil {
			scope.SetTag("trace_id", span.TraceID.String())
		}
	}
	for k, v := range o.tags {
		scope.SetTag(k, v)
	}
	// "extra" was a first-class field until sentry-go 0.46 removed it; a context
	// block of the same name is where those values live now
	if len(o.extras) > 0 {
		extra := make(map[string]any, len(o.extras))
		for k, v := range o.extras {
			extra[k] = s.cfg.scrubber.scrubValue(k, v)
		}
		scope.SetContext("extra", sentry.Context(extra))
	}
	for name, data := range o.contexts {
		scope.SetContext(name, sentry.Context(s.cfg.scrubber.scrubAny(data)))
	}
	if len(o.fingerprint) > 0 {
		scope.SetFingerprint(o.fingerprint)
	}
}

// ---------------------------------------------------------------------------
// Spans
// ---------------------------------------------------------------------------

type tracedSpan struct{ span *sentry.Span }

var _ ISpan = (*tracedSpan)(nil)

func (s *tracedSpan) Context() context.Context { return s.span.Context() }
func (s *tracedSpan) TraceID() string          { return s.span.TraceID.String() }
func (s *tracedSpan) SetTag(k, v string)       { s.span.SetTag(k, v) }
func (s *tracedSpan) SetData(k string, v any)  { s.span.SetData(k, v) }
func (s *tracedSpan) Sentry() *sentry.Span     { return s.span }

func (s *tracedSpan) Child(op, description string) ISpan {
	child := s.span.StartChild(op, sentry.WithDescription(description))
	if child == nil {
		return noopSpan{ctx: s.span.Context()}
	}
	return &tracedSpan{span: child}
}

func (s *tracedSpan) Finish(err error) {
	if err != nil {
		s.span.Status = sentry.SpanStatusInternalError
		if ierr := From(err); ierr != nil {
			s.span.SetTag("error.code", ierr.GetCode())
		}
	} else if s.span.Status == sentry.SpanStatusUndefined {
		s.span.Status = sentry.SpanStatusOK
	}
	s.span.Finish()
}

// noopSpan is what tracing-off code gets: it still carries the context, so call
// sites never branch.
type noopSpan struct{ ctx context.Context }

var _ ISpan = noopSpan{}

func (s noopSpan) Context() context.Context {
	if s.ctx == nil {
		return context.Background()
	}
	return s.ctx
}
func (s noopSpan) TraceID() string            { return "" }
func (s noopSpan) SetTag(string, string)      {}
func (s noopSpan) SetData(string, any)        {}
func (s noopSpan) Child(string, string) ISpan { return s }
func (s noopSpan) Finish(error)               {}
func (s noopSpan) Sentry() *sentry.Span       { return nil }

// ---------------------------------------------------------------------------
// No-op tracker
// ---------------------------------------------------------------------------

type noopSentry struct{ ctx context.Context }

var _ ISentry = noopSentry{}

func (n noopSentry) Enabled() bool { return false }
func (n noopSentry) WithContext(ctx context.Context) ISentry {
	return noopSentry{ctx: ctx}
}
func (n noopSentry) CaptureError(error, ...CaptureOption) string           { return "" }
func (n noopSentry) CaptureMessage(string, Level, ...CaptureOption) string { return "" }
func (n noopSentry) Recover(any, ...CaptureOption) string                  { return "" }
func (n noopSentry) Breadcrumb(Breadcrumb)                                 {}
func (n noopSentry) SetTag(string, string)                                 {}
func (n noopSentry) SetContextData(string, map[string]any)                 {}
func (n noopSentry) StartTransaction(string, string) ISpan                 { return noopSpan{ctx: n.ctx} }
func (n noopSentry) Meter() IMeter                                         { return noopMeter{} }
func (n noopSentry) SendCheckIn(CheckIn) string                            { return "" }
func (n noopSentry) Flush(time.Duration) bool                              { return true }
func (n noopSentry) Close() IError                                         { return nil }
func (n noopSentry) Hub() *sentry.Hub                                      { return nil }

// ---------------------------------------------------------------------------
// Helpers shared by the integrations
// ---------------------------------------------------------------------------

// String makes Mode readable in tags and logs.
func (m Mode) String() string {
	switch m {
	case ModeCron:
		return "cron"
	case ModeMQ:
		return "mq"
	case ModeTest:
		return "test"
	default:
		return "http"
	}
}

// sentryUser maps the context's principal onto a Sentry user.
func sentryUser(ctx IContext, ip string) *sentry.User {
	return sentryUserOf(ctx.GetUser(), ip)
}

// sentryUserOf maps a principal onto a Sentry user, falling back to the client
// IP alone when nobody is authenticated.
func sentryUserOf(u *ContextUser, ip string) *sentry.User {
	if u == nil {
		if ip == "" {
			return nil
		}
		return &sentry.User{IPAddress: ip}
	}
	data := map[string]string{}
	for k, v := range u.Data {
		data[k] = v
	}
	if u.Segment != "" {
		data["segment"] = u.Segment
	}
	return &sentry.User{
		ID:        u.ID,
		Username:  u.Username,
		Email:     u.Email,
		Name:      u.Name,
		IPAddress: ip,
		Data:      data,
	}
}

// levelForError maps an IError's status onto a severity: server failures are
// errors, everything else is a warning.
func levelForError(err error) Level {
	if status, ok := statusOf(err); ok {
		if status >= http.StatusInternalServerError {
			return LevelError
		}
		return LevelWarning
	}
	return LevelError
}

// defaultFingerprint keeps Sentry's stack-trace grouping but splits issues by
// error code, so one code is one issue instead of one message being many.
func defaultFingerprint(err error) []string {
	var ierr IError
	if errors.As(err, &ierr) && ierr.GetCode() != "" {
		return []string{"{{ default }}", ierr.GetCode()}
	}
	return nil
}

// statusOf returns the HTTP status an error carries, if it carries one.
//
// That is the framework's own IError — and also anything the HTTP layer judged
// for us: echo answers its routing failures (no such route, wrong method) with
// sentinels that are not IError but do report a status. Reading them matters:
// without this every request for a URL that does not exist becomes an issue,
// and a scanner walking the internet fills the project on its own.
func statusOf(err error) (int, bool) {
	var ierr IError
	if errors.As(err, &ierr) && ierr.GetStatus() > 0 {
		return ierr.GetStatus(), true
	}
	var coded interface{ StatusCode() int }
	if errors.As(err, &coded) && coded.StatusCode() > 0 {
		return coded.StatusCode(), true
	}
	return 0, false
}

// hasStatus reports whether err carries an explicit HTTP status — someone's
// judgement about what kind of failure it is. A plain error, or an IError left
// at status 0, carries none, so there is nothing for a threshold to filter on.
func hasStatus(err error) bool {
	_, ok := statusOf(err)
	return ok
}

// captureStatus reports whether an error is severe enough to be reported.
func captureStatus(err error, minStatus int) bool {
	if err == nil {
		return false
	}
	if status, ok := statusOf(err); ok {
		return status >= minStatus
	}
	// a plain error carries no status: treat it as a failure
	return true
}

// pinAttributes puts key/values on a hub's scope as *attributes*, which is what
// logs and metrics carry — a tag only ever reaches an event. Set once per
// request/run, so every line that unit of work writes can be filtered by them
// without any call site repeating itself.
//
// Empty values are dropped: an attribute that is present but blank is worse
// than one that is absent, because it looks like an answer.
func pinAttributes(hub *sentry.Hub, kv map[string]string) {
	if hub == nil || len(kv) == 0 {
		return
	}
	attrs := make([]attribute.Builder, 0, len(kv))
	for k, v := range kv {
		if v == "" {
			continue
		}
		attrs = append(attrs, attribute.String(k, v))
	}
	if len(attrs) > 0 {
		hub.Scope().SetAttributes(attrs...)
	}
}

// breadcrumbTo records a crumb on whatever hub ctx carries. Modules use it to
// leave a trail without depending on the tracker at all — with no hub (no DSN,
// or outside a request) it does nothing.
func breadcrumbTo(ctx context.Context, b Breadcrumb) {
	if ctx == nil {
		return
	}
	hub := sentry.GetHubFromContext(ctx)
	if hub == nil {
		return
	}
	level := b.Level
	if level == "" {
		level = LevelInfo
	}
	hub.AddBreadcrumb(&sentry.Breadcrumb{
		Type:      firstNonEmpty(b.Type, "default"),
		Category:  b.Category,
		Message:   b.Message,
		Level:     level,
		Data:      b.Data,
		Timestamp: time.Now(),
	}, nil)
}

// withHub returns ctx carrying its own hub, cloned from base so the request or
// run gets an isolated scope (v1 shared one global scope across goroutines).
func withHub(ctx context.Context, tracker ISentry) (context.Context, *sentry.Hub) {
	hub := tracker.Hub()
	if hub == nil {
		return ctx, nil
	}
	clone := hub.Clone()
	// A cloned scope inherits the propagation context of the hub it came from,
	// and the process hub's was generated once at boot. Without a fresh one here,
	// every request and every job run of the whole process reports the same trace
	// id — one trace for the lifetime of the deployment, with every log line of
	// every user in it.
	//
	// The transaction path used to be the only thing that replaced it, so this
	// was invisible with tracing on and wrong with tracing off, which is the
	// default.
	clone.Scope().SetPropagationContext(sentry.NewPropagationContext())
	return sentry.SetHubOnContext(ctx, clone), clone
}

func eventID(id *sentry.EventID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}

// monitorSlug normalises a job name into a Sentry monitor slug.
func monitorSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune('-')
		default:
			b.WriteRune('-')
		}
	}
	slug := strings.Trim(b.String(), "-")
	if len(slug) > 50 {
		slug = slug[:50]
	}
	if slug == "" {
		return "job"
	}
	return slug
}

// monitorConfig translates a job's Schedule into what Sentry needs to know a
// run is missing. Unsupported schedules simply carry no config.
func monitorConfig(c CheckIn) *sentry.MonitorConfig {
	if c.Schedule == nil {
		return nil
	}
	var schedule sentry.MonitorSchedule
	timezone := c.Timezone
	switch s := c.Schedule.(type) {
	case cronSchedule:
		if s.withSeconds {
			return nil // Sentry monitors have a one-minute resolution
		}
		// the bare expression: Sentry carries the zone in its own field, and
		// would read a CRON_TZ prefix as a sixth field it cannot parse
		schedule = sentry.CrontabSchedule(s.expr)
		if s.loc != nil {
			// the schedule's own zone is the one gocron fires on, so it beats the
			// scheduler-wide default that Timezone carries
			timezone = s.loc.String()
		}
	case durationSchedule:
		minutes := int64(s.d / time.Minute)
		if minutes < 1 {
			return nil
		}
		schedule = sentry.IntervalSchedule(minutes, sentry.MonitorScheduleUnitMinute)
	default:
		return nil
	}
	cfg := &sentry.MonitorConfig{Schedule: schedule, Timezone: timezone}
	if c.MaxRuntime > 0 {
		cfg.MaxRuntime = int64(c.MaxRuntime / time.Minute)
	}
	if c.CheckInMargin > 0 {
		cfg.CheckInMargin = int64(c.CheckInMargin / time.Minute)
	}
	return cfg
}

// chainBeforeSend scrubs every outgoing event, retitles it with the framework's
// error code, then hands it to the caller's own hook.
func chainBeforeSend(
	sc *scrubber,
	user func(*sentry.Event, *sentry.EventHint) *sentry.Event,
) func(*sentry.Event, *sentry.EventHint) *sentry.Event {
	return func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
		event = sc.scrubEvent(event)
		retitle(event, hint)
		if user != nil {
			return user(event, hint)
		}
		return event
	}
}

// chainBeforeSendLog scrubs every outgoing log line, then hands it to the
// caller's own hook. Logs bypass BeforeSend entirely — without this they would
// be the one channel that leaves the process unmasked.
func chainBeforeSendLog(
	sc *scrubber,
	user func(*sentry.Log) *sentry.Log,
) func(*sentry.Log) *sentry.Log {
	return func(log *sentry.Log) *sentry.Log {
		log = sc.scrubLog(log)
		if log != nil && user != nil {
			return user(log)
		}
		return log
	}
}

// chainBeforeSendMetric scrubs every outgoing measurement, then hands it to the
// caller's own hook. Metrics bypass BeforeSend exactly as logs do.
func chainBeforeSendMetric(
	sc *scrubber,
	user func(*sentry.Metric) *sentry.Metric,
) func(*sentry.Metric) *sentry.Metric {
	return func(metric *sentry.Metric) *sentry.Metric {
		metric = sc.scrubMetric(metric)
		if metric != nil && user != nil {
			return user(metric)
		}
		return metric
	}
}

// withConfigContext attaches the (already scrubbed) configuration to every
// event, then defers to the next hook.
func withConfigContext(
	config map[string]any,
	next func(*sentry.Event, *sentry.EventHint) *sentry.Event,
) func(*sentry.Event, *sentry.EventHint) *sentry.Event {
	return func(event *sentry.Event, hint *sentry.EventHint) *sentry.Event {
		if event != nil && len(config) > 0 {
			if event.Contexts == nil {
				event.Contexts = map[string]sentry.Context{}
			}
			if _, ok := event.Contexts["config"]; !ok {
				event.Contexts["config"] = config
			}
		}
		if next != nil {
			return next(event, hint)
		}
		return event
	}
}

// retitle names the issue after the error code instead of "*core.Error", so the
// Sentry issue list reads like the API's own vocabulary.
func retitle(event *sentry.Event, hint *sentry.EventHint) {
	if event == nil || hint == nil || len(event.Exception) == 0 {
		return
	}
	var ierr IError
	if !errors.As(hint.OriginalException, &ierr) {
		return
	}
	last := &event.Exception[len(event.Exception)-1]
	if !strings.Contains(last.Type, "core.Error") {
		return
	}
	if code := ierr.GetCode(); code != "" {
		last.Type = code
	}
	if msg, ok := ierr.GetMessage().(string); ok && msg != "" {
		last.Value = msg
	}
}

// parseLogStreamLevel is the floor for what reaches Sentry Logs. Unset means
// "whatever the logger already emits" — LOG_LEVEL is the only filter — so it
// maps to debug rather than to info the way the breadcrumb level does.
func parseLogStreamLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "info":
		return slog.LevelInfo
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelDebug
	}
}

func parseBreadcrumbLevel(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none":
		return slog.LevelError, true
	case "debug":
		return slog.LevelDebug, false
	case "warn", "warning":
		return slog.LevelWarn, false
	case "error":
		return slog.LevelError, false
	default:
		return slog.LevelInfo, false
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func boolOr(override *bool, configured bool) bool {
	if override != nil {
		return *override
	}
	return configured
}

// boolOrDefault resolves a flag whose default is true: the option wins, then an
// explicitly configured value, then the default.
func boolOrDefault(override *bool, env IENV, key string, def bool) bool {
	if override != nil {
		return *override
	}
	if env != nil && env.String(key) != "" {
		return env.Bool(key)
	}
	return def
}

func durationOr(seconds int, def time.Duration) time.Duration {
	if seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return def
}

func orStrings(override, configured []string) []string {
	if len(override) > 0 {
		return override
	}
	return configured
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// secretValuesOf collects the configured secrets so they are masked wherever
// they surface — in a URL, a header, a query string or a panic message.
func secretValuesOf(cfg *ENVConfig) []string {
	if cfg == nil {
		return nil
	}
	candidates := []string{
		cfg.DBPassword, cfg.DBMongoPassword, cfg.CachePassword, cfg.MQPassword,
		cfg.JWTSecret, cfg.S3SecretKey, cfg.S3AccessKey, cfg.EmailPassword,
	}
	out := make([]string, 0, len(candidates))
	for _, v := range candidates {
		// very short values would mask unrelated text
		if len(v) >= 6 {
			out = append(out, v)
		}
	}
	return out
}

// statusTag renders a status code for tagging.
func statusTag(status int) string { return strconv.Itoa(status) }
