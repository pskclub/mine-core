package core

import (
	"context"
	"sort"
	"sync"
	"time"
)

// PubSubHandler handles one delivery. The context is a full IContext — logger,
// database, cache, Sentry — built for that message and cancelled when the
// handler's timeout runs out.
//
// Returning an error logs and reports it. There is no redelivery: pub/sub has no
// acknowledgement, so a handler that must not lose work should write what it
// received somewhere durable (a job, a row) before doing anything slow.
type PubSubHandler func(ctx IContext, msg *PubSubMessage) error

// ISubscriber turns a set of channels into handlers, the way the HTTP server
// turns routes into handlers. It owns the subscription, the panic recovery, the
// per-message context and the drain on shutdown.
//
//	sub := app.NewSubscriber()
//	sub.On("user.updated", func(ctx core.IContext, msg *core.PubSubMessage) error {
//	    user, err := core.BindMessage[User](msg)
//	    if err != nil {
//	        return err
//	    }
//	    return cacheUser(ctx, user)
//	})
//	if err := sub.Start(); err != nil { ... }
type ISubscriber interface {
	// On registers a handler for an exact channel. Registering a channel twice
	// replaces the first handler.
	On(channel string, h PubSubHandler) ISubscriber
	// OnPattern registers a handler for a glob ("order.*").
	OnPattern(pattern string, h PubSubHandler) ISubscriber
	// Start opens the subscriptions and begins dispatching. It returns as soon
	// as the subscriptions are live — the work happens in the background.
	Start() IError
	// Stop ends the subscriptions and waits for in-flight handlers, up to the
	// deadline on ctx. Safe to call more than once.
	Stop(ctx context.Context) IError
	// Running reports whether Start has run and Stop has not.
	Running() bool
}

// SubscriberOption tunes a subscriber.
type SubscriberOption func(*subscriberOptions)

type subscriberOptions struct {
	cacheName   string
	concurrency int
	timeout     time.Duration
}

// WithSubscriberCache picks which registered cache to subscribe on. Defaults to
// "default".
func WithSubscriberCache(name string) SubscriberOption {
	return func(o *subscriberOptions) { o.cacheName = name }
}

// WithSubscriberConcurrency allows n handlers to run at once. The default is 1,
// which keeps messages in the order they arrived; raising it trades that order
// for throughput, so raise it only when the handlers are independent.
func WithSubscriberConcurrency(n int) SubscriberOption {
	return func(o *subscriberOptions) {
		if n > 0 {
			o.concurrency = n
		}
	}
}

// WithSubscriberTimeout bounds one handler. The default is 30s; a handler that
// overruns has its context cancelled, which is what stops a stuck message from
// holding the only worker forever.
func WithSubscriberTimeout(d time.Duration) SubscriberOption {
	return func(o *subscriberOptions) {
		if d > 0 {
			o.timeout = d
		}
	}
}

const (
	defaultSubscriberConcurrency = 1
	defaultSubscriberTimeout     = 30 * time.Second
	// stopGrace is how long Stop waits for handlers when the caller gave no
	// deadline of its own.
	stopGrace = 10 * time.Second
)

// NewSubscriber builds a subscriber on one of the App's caches. Nothing is
// subscribed until Start, so handlers can be registered in any order.
//
// The App remembers it and stops it during Shutdown, before the connections it
// is reading from are closed.
func (a *App) NewSubscriber(opts ...SubscriberOption) ISubscriber {
	o := subscriberOptions{
		cacheName:   defaultConn,
		concurrency: defaultSubscriberConcurrency,
		timeout:     defaultSubscriberTimeout,
	}
	for _, opt := range opts {
		opt(&o)
	}
	s := &subscriber{
		app:      a,
		opts:     o,
		handlers: map[string]PubSubHandler{},
		patterns: map[string]PubSubHandler{},
	}
	a.trackSubscriber(s)
	return s
}

type subscriber struct {
	app  *App
	opts subscriberOptions

	mu       sync.Mutex
	handlers map[string]PubSubHandler
	patterns map[string]PubSubHandler
	subs     []ISubscription
	running  bool

	wg   sync.WaitGroup
	sem  chan struct{}
	stop chan struct{}
	once sync.Once
}

var _ ISubscriber = (*subscriber)(nil)

func (s *subscriber) On(channel string, h PubSubHandler) ISubscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.handlers[channel] = h
	return s
}

func (s *subscriber) OnPattern(pattern string, h PubSubHandler) ISubscriber {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.patterns[pattern] = h
	return s
}

func (s *subscriber) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

func (s *subscriber) Start() IError {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return New(409, "SUBSCRIBER_RUNNING", "pubsub: subscriber is already running")
	}
	channels := keysOf(s.handlers)
	patterns := keysOf(s.patterns)
	s.mu.Unlock()

	if len(channels) == 0 && len(patterns) == 0 {
		return New(400, "SUBSCRIBER_EMPTY", "pubsub: subscriber has no handlers")
	}

	ps := s.app.Cache(s.opts.cacheName).PubSub()
	if !ps.Enabled() {
		return pubsubDisabled()
	}

	var opened []ISubscription
	// two subscriptions at most — one for the exact names, one for the globs —
	// rather than one connection per channel
	if len(channels) > 0 {
		sub, err := ps.Subscribe(channels...)
		if err != nil {
			return err
		}
		opened = append(opened, sub)
	}
	if len(patterns) > 0 {
		sub, err := ps.PSubscribe(patterns...)
		if err != nil {
			for _, o := range opened {
				_ = o.Close()
			}
			return err
		}
		opened = append(opened, sub)
	}

	s.mu.Lock()
	s.subs = opened
	s.stop = make(chan struct{})
	s.once = sync.Once{}
	s.sem = make(chan struct{}, s.opts.concurrency)
	// counted here, before this subscriber can be seen as running: a WaitGroup
	// may not be added to while somebody is waiting on it, and Stop waits
	s.wg.Add(len(opened))
	s.running = true
	s.mu.Unlock()

	for _, sub := range opened {
		go func(sub ISubscription) {
			defer s.wg.Done()
			s.consume(sub)
		}(sub)
	}
	// sorted: both lists come out of a map, and a boot log that reorders itself
	// between restarts cannot be diffed
	sort.Strings(channels)
	sort.Strings(patterns)

	s.app.Log().Info("pubsub subscriber started",
		"channels", channels,
		"patterns", patterns,
		"concurrency", s.opts.concurrency,
		"handler_timeout", s.opts.timeout.String(),
		"cache", s.opts.cacheName,
		// the prefix is why a message published by another service does not
		// arrive: two deployments sharing a redis under different prefixes are
		// subscribed to channels that only look like the same name
		"prefix", s.app.Cache(s.opts.cacheName).Prefix())
	return nil
}

// consume reads one subscription until it ends. The read loop stays sequential;
// only the handlers fan out, so the semaphore is the single place concurrency is
// decided.
//
// It owns the count of its own handlers and returns only once they have
// finished, which is what makes the subscriber's WaitGroup a drain: Stop waits
// for the readers, and each reader waits for what it started.
func (s *subscriber) consume(sub ISubscription) {
	var handlers sync.WaitGroup
	defer handlers.Wait()

	for msg := range sub.C() {
		h := s.handlerFor(msg)
		if h == nil {
			continue
		}

		select {
		case s.sem <- struct{}{}:
		case <-s.stop:
			return
		}
		handlers.Add(1)
		go func(msg *PubSubMessage) {
			defer handlers.Done()
			defer func() { <-s.sem }()
			s.dispatch(h, msg)
		}(msg)
	}
}

// handlerFor picks the handler a message belongs to: the pattern it arrived
// under when redis matched one, otherwise the exact channel.
func (s *subscriber) handlerFor(msg *PubSubMessage) PubSubHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	if msg.Pattern != "" {
		if h, ok := s.patterns[msg.Pattern]; ok {
			return h
		}
	}
	if h, ok := s.handlers[msg.Channel]; ok {
		return h
	}
	// a pattern subscription whose delivery carried no pattern (the in-process
	// broker for an exact name), or a channel that was unsubscribed mid-flight
	for pattern, h := range s.patterns {
		if matchChannel(pattern, msg.Channel) {
			return h
		}
	}
	return nil
}

// dispatch runs one handler with everything a unit of work gets: its own
// context, its own Sentry scope and transaction, a timeout, and a panic that
// ends the message rather than the process.
func (s *subscriber) dispatch(h PubSubHandler, msg *PubSubMessage) {
	base, cancel := context.WithTimeout(context.Background(), s.opts.timeout)
	defer cancel()

	tracker := s.app.Sentry()
	if hubCtx, hub := withHub(base, tracker); hub != nil {
		hub.Scope().SetTags(map[string]string{
			"mode":    ModeMQ.String(),
			"channel": msg.Channel,
			"pattern": msg.Pattern,
		})
		base = hubCtx
	}

	ctx := s.app.NewContext(base, ModeMQ)
	span := ctx.Sentry().StartTransaction("pubsub "+msg.Channel, "queue.process")
	ctx = ctx.WithContext(span.Context())

	started := time.Now()
	var err error
	func() {
		defer Recover(&err)
		err = h(ctx, msg)
	}()
	span.Finish(err)

	if err != nil {
		// logging it is what reports it: the logger's Sentry bridge turns a line
		// carrying a 5xx error into an event, so this is one issue, not two
		ctx.Log().Error("pubsub handler failed",
			"channel", msg.Channel, "pattern", msg.Pattern,
			"took", time.Since(started).String(), "err", err)
		return
	}
	ctx.Log().Debug("pubsub message handled",
		"channel", msg.Channel, "took", time.Since(started).String())
}

func (s *subscriber) Stop(ctx context.Context) IError {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.running = false
	subs := s.subs
	s.subs = nil
	stop := s.stop
	s.mu.Unlock()

	s.once.Do(func() { close(stop) })
	for _, sub := range subs {
		_ = sub.Close()
	}

	// wait for what is already running, but never past the caller's deadline:
	// shutdown has to end
	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), stopGrace)
		defer cancel()
	}
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		s.app.Log().Info("pubsub subscriber stopped")
		return nil
	case <-ctx.Done():
		return Wrap(ctx.Err(), "pubsub: stop timed out with handlers still running")
	}
}

// keysOf is the registered names of a handler map, in no particular order.
func keysOf(m map[string]PubSubHandler) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
