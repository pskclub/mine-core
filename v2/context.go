package core

import (
	"context"
	"sort"
	"sync"

	"gorm.io/gorm"
)

// Mode identifies the entry point a context serves, mirroring v1's ContextType.
type Mode uint8

const (
	ModeHTTP Mode = iota
	ModeCron
	ModeMQ
	ModeTest
)

// ContextUser is the authenticated principal attached to a context (same shape as v1).
type ContextUser struct {
	ID       string            `json:"id,omitempty"`
	Email    string            `json:"email,omitempty"`
	Username string            `json:"username,omitempty"`
	Name     string            `json:"name,omitempty"`
	Segment  string            `json:"segment,omitempty"`
	Token    string            `json:"token,omitempty"`
	Data     map[string]string `json:"data,omitempty"`
}

// IContext is the per-request/per-job container. It embeds context.Context so it
// is the single ambient context: capability accessors return handles already
// bound to it, so callers never pass a ctx explicitly. Method names match v1.
type IContext interface {
	context.Context

	ENV() IENV
	Log() ILogger
	Mode() Mode
	// Sentry returns the error tracker bound to this request/run: captures made
	// through it carry this context's user, tags and breadcrumbs. It is never
	// nil — with no DSN configured every call is a no-op.
	Sentry() ISentry
	// Meter records measurements on this request/run's trace. Never nil — with
	// metrics off every call is a no-op.
	Meter() IMeter

	DB() *gorm.DB
	DBS(name string) *gorm.DB
	DBMongo() IMongoDB
	DBSMongo(name string) IMongoDB
	// Cache returns the default cache, bound to this request/run. It is never
	// nil — with no cache configured every read misses and every write is
	// dropped, so cache-aside code runs unchanged. Check Enabled when a path
	// genuinely needs one.
	Cache() ICache
	Caches(name string) ICache
	// PubSub is the publish/subscribe side of the default cache. Never nil.
	PubSub() IPubSub
	MQ() IMQ
	// Storage is object storage, bound to this request/run. Never nil — with no
	// bucket configured every call fails with STORAGE_DISABLED rather than
	// panicking. Unlike the cache it does not degrade quietly: an upload that
	// was silently dropped is a file nobody can get back.
	Storage() IStorage

	NewError(err error, errorType IError, args ...interface{}) IError

	GetUser() *ContextUser
	SetUser(user *ContextUser)
	GetData(name string) interface{}
	GetAllData() map[string]interface{}
	SetData(name string, data interface{})

	// WithContext returns a shallow copy bound to a different underlying context
	// (escape hatch for background timeouts). Rarely needed.
	WithContext(ctx context.Context) IContext
}

// App owns the long-lived resources (connection pools, base logger, shared HTTP
// client). It is built once at startup; per-request contexts derive from it.
// This fixes v1's bug where IContext.Close() tore down shared pools per request.
type App struct {
	env       IENV
	log       ILogger
	sentry    ISentry
	dbs       map[string]*gorm.DB
	caches    map[string]ICache
	mongos    map[string]IMongoDB
	mq        IMQ
	storage   IStorage
	mailer    IMailer
	pusher    IPusher
	chats     map[string]IChat
	requester IRequester
	llm       ILLM
	embedder  IEmbedder

	// logTaps observe every line the logger writes. See WithLogTap.
	logTaps []LogTapFunc

	// subs are the background readers this App handed out — pub/sub subscribers
	// and message-queue consumers. Shutdown stops them before it closes the
	// connections they are reading from.
	subsMu sync.Mutex
	subs   []Stoppable

	// closed makes Shutdown idempotent. Two call sites are normal — a
	// `defer app.Shutdown(ctx)` at the top of main and StartHTTPServer closing
	// the pools once the drain is over — and a redis or Sentry client closed
	// twice reports an error that means nothing to whoever reads the log.
	closed sync.Once
}

// Option configures an App at construction.
type Option func(*App)

// WithLogger overrides the base logger.
func WithLogger(l ILogger) Option { return func(a *App) { a.log = l } }

// WithSQL registers a GORM connection under name ("default" for the primary).
func WithSQL(name string, db *gorm.DB) Option {
	return func(a *App) { a.dbs[name] = db }
}

// WithCache registers a cache under name ("default" for the primary).
func WithCache(name string, c ICache) Option {
	return func(a *App) { a.caches[name] = c }
}

// WithMongo registers a Mongo connection under name ("default" for the primary).
func WithMongo(name string, m IMongoDB) Option {
	return func(a *App) { a.mongos[name] = m }
}

// WithMQ registers the message-queue publisher.
func WithMQ(mq IMQ) Option { return func(a *App) { a.mq = mq } }

// WithStorage registers object storage.
func WithStorage(s IStorage) Option { return func(a *App) { a.storage = s } }

// WithMailer registers the mailer. Pass NewMemoryMailer() in a test to assert on
// what a service would have sent.
func WithMailer(m IMailer) Option { return func(a *App) { a.mailer = m } }

// WithPusher registers the push-notification sender. Pass NewMemoryPusher() in a
// test to assert on what a service would have pushed.
func WithPusher(p IPusher) Option { return func(a *App) { a.pusher = p } }

// WithChat registers a chat provider under name ("default" for the primary).
// Build one with a driver (chat/slack.New(env)) at startup, or pass
// NewMemoryChat() in a test to assert on what a service would have posted.
//
// It is named like WithSQL and WithCache rather than like WithMailer because a
// service routinely posts to more than one place — alerts to an internal Slack,
// replies to customers on LINE — and those are different providers, not one
// provider with two destinations.
func WithChat(name string, c IChat) Option {
	return func(a *App) { a.chats[name] = c }
}

// WithRequester overrides the shared HTTP client.
func WithRequester(r IRequester) Option { return func(a *App) { a.requester = r } }

// WithLLM registers the language model. Build one with a driver
// (llm/goai.New(env)) at startup, or pass NewMemoryLLM(...) in a test to assert
// on what a service would have asked a model, without a network call or a key.
func WithLLM(l ILLM) Option { return func(a *App) { a.llm = l } }

// WithEmbedder registers the embedding model. It is separate from WithLLM
// because the two are separate models — often from different vendors, since the
// provider a service generates with may not offer embeddings at all.
func WithEmbedder(e IEmbedder) Option { return func(a *App) { a.embedder = e } }

// WithSentry overrides the error tracker. Without it NewApp builds one from the
// configuration — a no-op tracker when no DSN is set — so error reporting is
// wired the same way in every environment.
func WithSentry(s ISentry) Option { return func(a *App) { a.sentry = s } }

// NewApp builds the application container from configuration and options.
func NewApp(env IENV, opts ...Option) (*App, IError) {
	a := &App{
		env:    env,
		dbs:    map[string]*gorm.DB{},
		caches: map[string]ICache{},
		mongos: map[string]IMongoDB{},
		chats:  map[string]IChat{},
	}
	for _, o := range opts {
		o(a)
	}
	if a.log == nil {
		a.log = NewLogger(env)
	}
	if a.sentry == nil {
		s, err := NewSentry(env)
		if err != nil {
			return nil, err
		}
		a.sentry = s
	}
	if a.sentry.Enabled() {
		// log lines become breadcrumbs — and a line carrying a 5xx error becomes
		// an event, and every line reaches Sentry Logs when that is enabled — so
		// application code never has to call the tracker by hand
		a.log = withSentryLogging(a.log, a.sentry)
	}
	// outermost, so a tap sees the line whether or not Sentry is configured and
	// exactly as it was written — before any handler has had a chance to drop it
	a.log = tapLogger(a.log, a.logTaps)
	if a.requester == nil {
		a.requester = NewRequester(a.env, a.log)
	}
	instrumentRequester(a.requester, a.sentry)
	// the call log is independent of Sentry: a service with no DSN still has to
	// be able to see what it asked of a dependency and what came back
	instrumentRequesterLog(a.requester, a.log, httpLogLevelFrom(a.env), 0,
		a.env != nil && a.env.Config().HTTPLogBody)
	// token spend and latency are recorded for whatever driver was registered,
	// so cost accounting does not depend on which provider a service picked
	a.llm = instrumentLLM(a.llm, a.log, a.env)
	return a, nil
}

// LogCapabilities states what this process was actually wired with.
//
// Every capability degrades rather than refusing to boot: no CACHE_* gives a
// cache that misses, no S3_* gives storage that errors, an unregistered Mongo
// name gives a disabled handle. That is deliberate — a service should not fail
// to start over something it may never touch — but it means a missing
// connection is invisible until the first request that needed it, and the
// symptom then ("nothing is being cached", "the queue never gets anything") is
// several layers away from the cause, which is one line of configuration.
//
// One line at boot answers it. Named connections are listed by name so that
// "which replica did it open?" is answerable too.
//
// StartHTTPServer and Runner call it, so a service using either gets it for
// free. It is exported for a process that starts itself some other way — and it
// is deliberately not called by NewApp, because building the container is not
// the same event as starting the process, and every test builds one.
func (a *App) LogCapabilities() {
	fields := []any{
		"env", a.env.Config().ENV,
		"service", a.env.Config().Service,
		"sql", connNames(a.dbs),
		"mongo", connNames(a.mongos),
		"cache", connNames(a.caches),
		"mq", a.mq != nil && a.mq.Enabled(),
		"storage", a.storage != nil && a.storage.Enabled(),
		"mailer", a.mailer != nil && a.mailer.Enabled(),
		"pusher", a.pusher != nil && a.pusher.Enabled(),
		"chat", connNames(a.chats),
		"llm", llmBootField(a.llm),
		"embedder", embedderBootField(a.embedder),
		"sentry", a.Sentry().Enabled(),
	}
	a.log.Info("app ready", fields...)
}

// connNames is the sorted names of the registered connections, so the line is
// stable across restarts. An empty list reads as "[]", which is the point: it
// says the connection is absent rather than leaving the reader to notice that a
// key they expected is missing.
func connNames[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Config returns the loaded configuration.
func (a *App) Config() *ENVConfig { return a.env.Config() }

// ENV returns the environment accessor.
func (a *App) ENV() IENV { return a.env }

// Log returns the base logger.
func (a *App) Log() ILogger { return a.log }

// Cache returns a registered cache, "default" when called without a name. Never
// nil: an unregistered name gives the disabled cache. Prefer ctx.Cache(), which
// is bound to the request or run.
func (a *App) Cache(name ...string) ICache {
	key := defaultConn
	if len(name) > 0 && name[0] != "" {
		key = name[0]
	}
	if c, ok := a.caches[key]; ok && c != nil {
		return c
	}
	return noopCache{}
}

// PubSub is the publish/subscribe side of the default cache. Never nil.
func (a *App) PubSub() IPubSub { return a.Cache().PubSub() }

// Storage returns object storage. Never nil: without a bucket every call fails
// with STORAGE_DISABLED. Prefer ctx.Storage(), which is bound to the request or
// run and so is cancelled with it.
func (a *App) Storage() IStorage {
	if a.storage == nil {
		return noopStorage{}
	}
	return a.storage
}

// MQ returns the message-queue publisher. Never nil: without a broker every
// call fails with MQ_DISABLED. Prefer ctx.MQ(), which is bound to the request or
// run and so is cancelled with it.
func (a *App) MQ() IMQ {
	if a.mq == nil {
		return noopMQ{}
	}
	return a.mq
}

// Mailer returns the mailer, unbound to any request — for a startup check, or a
// background goroutine that outlives one. Never nil: without an SMTP server
// every call fails with MAILER_DISABLED.
//
// Inside a request or a job use core.Mailer(ctx), which binds the delivery to
// that unit of work.
func (a *App) Mailer() IMailer {
	if a.mailer == nil {
		return noopMailer{}
	}
	return a.mailer
}

// Pusher returns the push sender, unbound to any request. Never nil: without
// Firebase credentials every call fails with PUSH_DISABLED.
//
// Inside a request or a job use core.Pusher(ctx).
func (a *App) Pusher() IPusher {
	if a.pusher == nil {
		return noopPusher{}
	}
	return a.pusher
}

// Chat returns a registered chat provider, "default" when called without a name.
// Never nil: an unregistered name gives the disabled provider, whose every call
// fails with CHAT_DISABLED.
//
// Inside a request or a job use core.Chat(ctx) / core.Chats(ctx, name), which
// bind the post to that unit of work.
func (a *App) Chat(name ...string) IChat {
	key := defaultConn
	if len(name) > 0 && name[0] != "" {
		key = name[0]
	}
	if c, ok := a.chats[key]; ok && c != nil {
		return c
	}
	return noopChat{}
}

// LLMModel returns the language model, unbound to any request — for a startup
// check, or a background goroutine that outlives one. Never nil: without a
// provider every call fails with LLM_DISABLED.
//
// Inside a request or a job use core.LLM(ctx), which binds the generation to
// that unit of work so it is cancelled with it. The name avoids colliding with
// the core.LLM function, which is what almost every call site should use.
func (a *App) LLMModel() ILLM {
	if a.llm == nil {
		return noopLLM{}
	}
	return a.llm
}

// Stoppable is a background reader the App must stop before it closes the
// connections that reader is using — a pub/sub subscriber, a queue consumer.
type Stoppable interface {
	Stop(ctx context.Context) IError
}

func (a *App) trackSubscriber(s Stoppable) {
	a.subsMu.Lock()
	defer a.subsMu.Unlock()
	a.subs = append(a.subs, s)
}

// Meter records measurements outside any request or run — at boot, in a
// background goroutine. Inside one, use ctx.Meter() so the measurement carries
// that unit of work's trace.
func (a *App) Meter() IMeter { return a.Sentry().Meter() }

// Sentry returns the process-wide error tracker. Prefer ctx.Sentry(), which is
// bound to the request or run and therefore reports far more context.
func (a *App) Sentry() ISentry {
	if a.sentry == nil {
		return NewNoopSentry()
	}
	return a.sentry
}

// NewContext derives a per-request/job context bound to ctx. mode defaults to
// ModeHTTP when omitted.
func (a *App) NewContext(ctx context.Context, mode ...Mode) IContext {
	m := ModeHTTP
	if len(mode) > 0 {
		m = mode[0]
	}
	return &coreContext{Context: a.onContext(ctx), app: a, mode: m}
}

// appContextKey carries the App on the context itself, which is what lets
// package-level helpers — Requester(ctx) — find the shared client from any
// context derived from a request or a job run, including a plain
// context.Context that has travelled through code knowing nothing about core.
type appContextKey struct{}

func (a *App) onContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Value(appContextKey{}) == a {
		return ctx
	}
	return context.WithValue(ctx, appContextKey{}, a)
}

// appFrom returns the App a context was created by, or nil.
func appFrom(ctx context.Context) *App {
	if ctx == nil {
		return nil
	}
	a, _ := ctx.Value(appContextKey{}).(*App)
	return a
}

// Shutdown closes the owned pools. Safe to call more than once: the second call
// is a no-op, so a `defer app.Shutdown(ctx)` and StartHTTPServer's own close can
// coexist.
//
// Call it after everything that uses a pool has stopped — requests drained, jobs
// finished — never before.
func (a *App) Shutdown(ctx context.Context) IError {
	var firstErr IError
	a.closed.Do(func() { firstErr = a.shutdown(ctx) })
	return firstErr
}

// shutdown stops the subscribers and closes every pool. The context bounds the
// drain of in-flight pub/sub handlers; the Close methods themselves take none.
func (a *App) shutdown(ctx context.Context) IError {
	var firstErr IError
	setErr := func(err IError) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	// first: stop reading. A subscriber left running would keep dispatching
	// messages into handlers whose database is about to be closed underneath
	// them.
	a.subsMu.Lock()
	subs := append([]Stoppable(nil), a.subs...)
	a.subsMu.Unlock()
	for _, s := range subs {
		setErr(s.Stop(ctx))
	}
	for _, c := range a.caches {
		setErr(c.Close())
	}
	for _, m := range a.mongos {
		setErr(m.Close())
	}
	if a.mq != nil {
		setErr(a.mq.Close())
	}
	if a.mailer != nil {
		setErr(a.mailer.Close())
	}
	for _, c := range a.chats {
		if c != nil {
			setErr(c.Close())
		}
	}
	if a.llm != nil {
		setErr(a.llm.Close())
	}
	if a.embedder != nil {
		setErr(a.embedder.Close())
	}
	for _, db := range a.dbs {
		if sqlDB, err := db.DB(); err == nil {
			if closeErr := sqlDB.Close(); closeErr != nil {
				setErr(Wrap(closeErr, "sqldb: close"))
			}
		}
	}
	// last: events queued by the shutdown itself still get out
	if a.sentry != nil {
		setErr(a.sentry.Close())
	}
	return firstErr
}

const defaultConn = "default"

type coreContext struct {
	context.Context
	app  *App
	mode Mode
	user *ContextUser

	dataMu sync.RWMutex
	data   map[string]interface{}
}

var _ IContext = (*coreContext)(nil)

func (c *coreContext) ENV() IENV { return c.app.env }

func (c *coreContext) Mode() Mode { return c.mode }

func (c *coreContext) Log() ILogger {
	if l, ok := c.app.log.(*logger); ok {
		return l.withContext(c.Context)
	}
	return c.app.log
}

func (c *coreContext) Sentry() ISentry { return c.app.Sentry().WithContext(c) }

func (c *coreContext) Meter() IMeter { return c.app.Sentry().WithContext(c).Meter() }

func (c *coreContext) DB() *gorm.DB { return c.DBS(defaultConn) }

func (c *coreContext) DBS(name string) *gorm.DB {
	db, ok := c.app.dbs[name]
	if !ok || db == nil {
		return nil
	}
	return db.WithContext(c.Context) // bind request ctx (ambient)
}

func (c *coreContext) DBMongo() IMongoDB { return c.DBSMongo(defaultConn) }

// DBSMongo returns a named Mongo connection. An unregistered name gives the
// disabled handle rather than nil: every call then fails with MONGO_DISABLED,
// naming the missing configuration, instead of panicking on a nil interface.
func (c *coreContext) DBSMongo(name string) IMongoDB {
	m, ok := c.app.mongos[name]
	if !ok || m == nil {
		return noopMongo{}
	}
	return m.WithContext(c.Context)
}

func (c *coreContext) Cache() ICache { return c.Caches(defaultConn) }

// Caches returns a named cache. An unregistered name gives the disabled cache
// rather than nil: a missing cache degrades the code that uses it, it does not
// crash it. This is deliberate — v2.2 returned nil here and every cache-aside
// helper panicked in a service that had no redis.
func (c *coreContext) Caches(name string) ICache {
	ch, ok := c.app.caches[name]
	if !ok || ch == nil {
		return noopCache{}
	}
	return ch.WithContext(c.Context)
}

func (c *coreContext) PubSub() IPubSub { return c.Cache().PubSub() }

func (c *coreContext) Storage() IStorage {
	if c.app.storage == nil {
		return noopStorage{}
	}
	return c.app.storage.WithContext(c.Context)
}

// MQ returns the message-queue publisher. An unconfigured broker gives the
// disabled publisher rather than nil: every call then fails with MQ_DISABLED,
// naming the missing configuration, instead of panicking on a nil interface.
// Unlike the cache it does not degrade quietly — a message silently dropped is
// work nobody will ever pick up.
func (c *coreContext) MQ() IMQ {
	if c.app.mq == nil {
		return noopMQ{}
	}
	return c.app.mq.WithContext(c.Context)
}

func (c *coreContext) NewError(err error, errorType IError, args ...interface{}) IError {
	return newError(c, err, errorType, args...)
}

func (c *coreContext) GetUser() *ContextUser { return c.user }

func (c *coreContext) SetUser(user *ContextUser) { c.user = user }

func (c *coreContext) GetData(name string) interface{} {
	c.dataMu.RLock()
	defer c.dataMu.RUnlock()
	if c.data == nil {
		return nil
	}
	return c.data[name]
}

func (c *coreContext) GetAllData() map[string]interface{} {
	c.dataMu.RLock()
	defer c.dataMu.RUnlock()
	out := make(map[string]interface{}, len(c.data))
	for k, v := range c.data {
		out[k] = v
	}
	return out
}

func (c *coreContext) SetData(name string, data interface{}) {
	c.dataMu.Lock()
	defer c.dataMu.Unlock()
	if c.data == nil {
		c.data = make(map[string]interface{})
	}
	c.data[name] = data
}

func (c *coreContext) WithContext(ctx context.Context) IContext {
	// re-stamp the App: a context built from context.Background() would otherwise
	// lose it, and Requester(ctx) would fall back to a client this App never
	// configured
	return &coreContext{Context: c.app.onContext(ctx), app: c.app, mode: c.mode, user: c.user}
}

// newError builds the returned error from errorType, surfacing the underlying
// message in dev and logging (never panicking — v1's type assertion could).
func newError(c IContext, err error, errorType IError, args ...interface{}) IError {
	base := From(errorType)
	if base == nil {
		base = InternalServerError()
	}
	out := base.clone()
	if err != nil {
		out.cause = err
		if c.ENV().IsDev() {
			// the root cause, not err.Error(): by the time a repository or
			// client error reaches here it is already wrapped, and the wrapper's
			// label ("repository") is not what a developer needs to read
			out.Message = devMessage(err)
		}
	}
	// the stack always comes from here, never from errorType: a sentinel says
	// what kind of failure this is, not where it happened, and keeping its stack
	// would make every error sharing that sentinel report the same trace
	// (skip 2 = past newError and coreContext.NewError, to the caller)
	out.stack = captureStack(2)
	// report before logging: the log line then carries an error that is already
	// marked as reported, so the logger's own Sentry bridge leaves it alone and
	// one failure stays one issue
	captureError(c, out, args...)
	if out.GetStatus() >= 500 {
		fields := []any{"code", out.Code, "err", out}
		if err != nil {
			// the cause goes under its own key: logs keep the driver's message,
			// and the bridge only ever looks at "err"
			fields = append(fields, "cause", err)
		}
		// +2 frames: the line that matters is the one that called ctx.NewError,
		// not this file. Without it every 5xx in every service is blamed on the
		// same context.go line, which answers nothing.
		callerSkip(c.Log(), 2).Error("request error", append(fields, args...)...)
	}
	return out
}

// captureError reports the error to Sentry at the point it is built — where the
// context still knows the user, the request and the trail that led here. The
// event id is remembered on the error so the layers above it (HTTP error
// handler, job runner) do not report the same failure a second time.
func captureError(c IContext, out *Error, args ...interface{}) {
	tracker := c.Sentry()
	if !tracker.Enabled() || out.eventID != "" {
		return
	}
	if !captureStatus(out, sentryOf(tracker).minStatus) {
		return
	}
	// the cause may already have been reported (logged before it was wrapped):
	// inherit its event instead of opening a second issue for one failure
	if id := capturedID(out.cause); id != "" {
		out.eventID = id
		return
	}
	opts := make([]CaptureOption, 0, len(args)+2)
	opts = append(opts,
		CaptureTag("error.code", out.Code),
		CaptureTag("error.status", statusTag(out.Status)))
	for i := 0; i+1 < len(args); i += 2 {
		if key, ok := args[i].(string); ok {
			opts = append(opts, CaptureExtra(key, args[i+1]))
		}
	}
	if len(args)%2 == 1 {
		opts = append(opts, CaptureExtra("arg", args[len(args)-1]))
	}
	out.eventID = tracker.CaptureError(out, opts...)
	// the cause was reported as part of this event; marking it stops a later
	// layer that only sees the cause from reporting it again
	markCaptured(out.cause, out.eventID)
}

// InternalServerError is the fallback 500 used when no error type is supplied.
func InternalServerError() *Error {
	return New(500, "INTERNAL_SERVER_ERROR", "Internal server error")
}

// Set stores a typed value on the context's data bag and returns ctx for chaining.
func Set[T any](ctx IContext, key string, value T) IContext {
	ctx.SetData(key, value)
	return ctx
}

// Get retrieves a typed value previously stored with Set (or SetData).
func Get[T any](ctx IContext, key string) (T, bool) {
	var zero T
	v := ctx.GetData(key)
	if v == nil {
		return zero, false
	}
	typed, ok := v.(T)
	return typed, ok
}
