package core

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// IPubSub is redis publish/subscribe: fan-out to every listener, right now.
//
// It is not a queue and must not be used as one. A message published while
// nobody is listening is gone, there is no acknowledgement, no redelivery and no
// persistence. That makes it right for cache invalidation, config reloads,
// presence and live updates — and wrong for work that must not be lost, which
// belongs on IMQ or the job queue.
type IPubSub interface {
	// Publish sends msg to a channel. string/[]byte go out as they are,
	// everything else is JSON-encoded — the same rule ICache.Set follows.
	Publish(channel string, msg any) IError
	// Subscribe listens on exact channel names.
	Subscribe(channels ...string) (ISubscription, IError)
	// PSubscribe listens on glob patterns ("order.*", "user.?", "cache:[ab]*").
	PSubscribe(patterns ...string) (ISubscription, IError)
	// Channel is the wire name a logical channel travels under: the cache
	// prefix plus the name. Only needed to talk to a publisher that is not
	// built on this framework.
	Channel(name string) string
	// Enabled reports whether there is a real connection behind this handle.
	Enabled() bool
	// WithContext returns a handle bound to another context.
	WithContext(ctx context.Context) IPubSub
	// Close releases the handle. Subscriptions are closed individually.
	Close() IError
}

// ISubscription is one live subscription. Always Close it — a subscription
// holds a connection of its own for as long as it lives.
type ISubscription interface {
	// C delivers messages until Close is called, and is then closed itself, so
	// `for msg := range sub.C()` ends cleanly.
	C() <-chan *PubSubMessage
	// Channels are the names (or patterns) this subscription listens on.
	Channels() []string
	// Close stops the subscription. Safe to call more than once.
	Close() IError
}

// PubSubMessage is one delivery. Payload is the raw bytes as published; Bind decodes
// them.
type PubSubMessage struct {
	// Channel is the channel the message arrived on, without the prefix.
	Channel string
	// Pattern is the glob that matched, for a pattern subscription. Empty for
	// an exact one.
	Pattern string
	Payload []byte
}

// Bind decodes the payload into dest, following the same rules as ICache.Get:
// *string, *[]byte and *json.RawMessage take the raw bytes, anything else is
// JSON-decoded.
func (m *PubSubMessage) Bind(dest any) IError {
	if m == nil {
		return New(400, "PUBSUB_EMPTY_MESSAGE", "pubsub: no message to bind")
	}
	return decodeCacheValue(m.Payload, dest)
}

// String renders the payload as text, for logs.
func (m *PubSubMessage) String() string {
	if m == nil {
		return ""
	}
	return string(m.Payload)
}

// BindMessage is the typed form of PubSubMessage.Bind.
func BindMessage[T any](m *PubSubMessage) (T, IError) {
	var out T
	if err := m.Bind(&out); err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Redis implementation
// ---------------------------------------------------------------------------

type redisPubSub struct {
	ctx    context.Context
	rdb    redis.UniversalClient
	prefix string
}

var _ IPubSub = (*redisPubSub)(nil)

// subscribeTimeout bounds how long a subscription waits for redis to confirm it.
// Without it a redis that accepts connections but never answers turns a boot
// into a hang with no message.
const subscribeTimeout = 5 * time.Second

// subscriptionBuffer is how many messages the driver holds for a subscriber
// that is behind. Past it, redis's own client buffer takes over and the server
// eventually disconnects a subscriber that never catches up.
const subscriptionBuffer = 100

func (p *redisPubSub) Enabled() bool { return true }

func (p *redisPubSub) Channel(name string) string { return p.prefix + name }

func (p *redisPubSub) WithContext(ctx context.Context) IPubSub {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *p
	cp.ctx = ctx
	return &cp
}

func (p *redisPubSub) Publish(channel string, msg any) IError {
	payload, err := encodeCacheValue(msg)
	if err != nil {
		return err
	}
	received, pubErr := p.rdb.Publish(p.ctx, p.Channel(channel), payload).Result()
	if pubErr != nil {
		breadcrumbTo(p.ctx, Breadcrumb{
			Type: "error", Category: "pubsub.publish", Level: LevelError,
			Message: channel,
			Data:    map[string]any{"bytes": len(payload), "error": pubErr.Error()},
		})
		return Wrap(pubErr, "pubsub: publish")
	}
	breadcrumbTo(p.ctx, Breadcrumb{
		Category: "pubsub.publish",
		Message:  channel,
		Data:     map[string]any{"bytes": len(payload), "receivers": received},
	})
	return nil
}

func (p *redisPubSub) Subscribe(channels ...string) (ISubscription, IError) {
	return p.subscribe(channels, nil)
}

func (p *redisPubSub) PSubscribe(patterns ...string) (ISubscription, IError) {
	return p.subscribe(nil, patterns)
}

func (p *redisPubSub) subscribe(channels, patterns []string) (ISubscription, IError) {
	if len(channels) == 0 && len(patterns) == 0 {
		return nil, New(400, "PUBSUB_NO_CHANNEL", "pubsub: subscribe needs at least one channel")
	}

	// A subscription outlives the request that opened it — that is the whole
	// point of one — so it does not inherit that request's cancellation. Close
	// is what ends it.
	ctx := context.WithoutCancel(p.ctx)

	var ps *redis.PubSub
	if len(patterns) > 0 {
		ps = p.rdb.PSubscribe(ctx, p.qualify(patterns)...)
	} else {
		ps = p.rdb.Subscribe(ctx, p.qualify(channels)...)
	}

	// Confirm the server accepted it, so a broken connection is an error here
	// rather than a channel that stays silent forever.
	confirmCtx, cancel := context.WithTimeout(ctx, subscribeTimeout)
	defer cancel()
	if _, err := ps.Receive(confirmCtx); err != nil {
		_ = ps.Close()
		return nil, Wrap(err, "pubsub: subscribe")
	}

	sub := &redisSubscription{
		ps:       ps,
		out:      make(chan *PubSubMessage, subscriptionBuffer),
		done:     make(chan struct{}),
		prefix:   p.prefix,
		channels: append(append([]string{}, channels...), patterns...),
	}
	go sub.pump(ctx)
	return sub, nil
}

func (p *redisPubSub) qualify(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, p.Channel(n))
	}
	return out
}

// Close is a no-op: the connections belong to the subscriptions and to the
// cache this handle came from.
func (p *redisPubSub) Close() IError { return nil }

type redisSubscription struct {
	ps       *redis.PubSub
	out      chan *PubSubMessage
	done     chan struct{}
	prefix   string
	channels []string
	closeOne sync.Once
}

var _ ISubscription = (*redisSubscription)(nil)

// pump translates the driver's messages into ours. The driver's channel
// re-subscribes by itself after a reconnect, so a redis restart interrupts
// delivery without ending the subscription.
func (s *redisSubscription) pump(ctx context.Context) {
	defer close(s.out)
	in := s.ps.Channel(redis.WithChannelSize(subscriptionBuffer))
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		case raw, ok := <-in:
			if !ok {
				return
			}
			msg := &PubSubMessage{
				Channel: strings.TrimPrefix(raw.Channel, s.prefix),
				Pattern: strings.TrimPrefix(raw.Pattern, s.prefix),
				Payload: []byte(raw.Payload),
			}
			select {
			case s.out <- msg:
			case <-s.done:
				return
			}
		}
	}
}

func (s *redisSubscription) C() <-chan *PubSubMessage { return s.out }

func (s *redisSubscription) Channels() []string {
	return append([]string{}, s.channels...)
}

func (s *redisSubscription) Close() IError {
	var err error
	s.closeOne.Do(func() {
		close(s.done)
		err = s.ps.Close()
	})
	if err != nil {
		return Wrap(err, "pubsub: close subscription")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Disabled pub/sub
// ---------------------------------------------------------------------------

// noopPubSub is what a service with no cache gets. Publishing is dropped — the
// same trade-off as a write to a disabled cache — but subscribing fails loudly:
// a subscriber that silently receives nothing forever is a service that looks
// healthy while doing none of its work.
type noopPubSub struct{}

var _ IPubSub = noopPubSub{}

func (noopPubSub) Publish(string, any) IError { return nil }

func (noopPubSub) Subscribe(...string) (ISubscription, IError) {
	return nil, pubsubDisabled()
}

func (noopPubSub) PSubscribe(...string) (ISubscription, IError) {
	return nil, pubsubDisabled()
}

func (noopPubSub) Channel(name string) string          { return name }
func (noopPubSub) Enabled() bool                       { return false }
func (noopPubSub) WithContext(context.Context) IPubSub { return noopPubSub{} }
func (noopPubSub) Close() IError                       { return nil }

func pubsubDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "PUBSUB_DISABLED",
		Message: "pubsub: no cache is configured (set CACHE_HOST or CACHE_CONNECTION_STRING)",
		cause:   fmt.Errorf("%w: pubsub", ErrCacheDisabled),
	}
}

// ---------------------------------------------------------------------------
// Pattern matching
// ---------------------------------------------------------------------------

// matchChannel reports whether a redis-style glob matches a channel name. It
// exists for the in-process broker; the redis server does its own matching.
//
// Supported: '*' (any run of characters), '?' (one character), '[abc]' /
// '[a-c]' / '[^a]' (character classes) and '\' to escape any of them. Unlike
// path.Match, '*' matches separators — channel names are not paths.
func matchChannel(pattern, name string) bool {
	// p/n walk the pattern and the name; star/retry remember the last '*' so a
	// failed match can resume from it — the standard backtracking glob, without
	// the recursion.
	var p, n, star, retry int
	star = -1
	for n < len(name) {
		switch {
		case p < len(pattern) && pattern[p] == '\\' && p+1 < len(pattern):
			if pattern[p+1] != name[n] {
				break
			}
			p += 2
			n++
			continue
		case p < len(pattern) && pattern[p] == '*':
			star, retry = p, n
			p++
			continue
		case p < len(pattern) && pattern[p] == '?':
			p++
			n++
			continue
		case p < len(pattern) && pattern[p] == '[':
			end, ok := matchClass(pattern[p:], name[n])
			if ok {
				p += end
				n++
				continue
			}
		case p < len(pattern) && pattern[p] == name[n]:
			p++
			n++
			continue
		}

		if star < 0 {
			return false
		}
		// the last '*' swallows one more character and we try again
		retry++
		p, n = star+1, retry
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// matchClass matches one character against a leading "[...]" class and returns
// the class's length. A class that is never closed is not a class.
func matchClass(pattern string, c byte) (int, bool) {
	end := strings.IndexByte(pattern, ']')
	if end <= 0 {
		return 0, false
	}
	body := pattern[1:end]
	negate := strings.HasPrefix(body, "^")
	if negate {
		body = body[1:]
	}

	matched := false
	for i := 0; i < len(body); i++ {
		switch {
		case i+2 < len(body) && body[i+1] == '-':
			if c >= body[i] && c <= body[i+2] {
				matched = true
			}
			i += 2
		case body[i] == c:
			matched = true
		}
	}
	return end + 1, matched != negate
}
