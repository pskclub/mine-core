package core

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// memoryCache is an in-process ICache. It exists for two reasons: a test should
// be able to exercise the cache path without a redis running, and a
// single-instance service (a dev box, a small internal tool) should be able to
// cache without one either.
//
// It is a real cache, not a stub: values are encoded exactly as the redis
// implementation encodes them, keys expire, counters are atomic and locks
// exclude other goroutines. What it cannot do is span processes — two replicas
// each get their own, so it is the wrong choice for anything that coordinates
// (rate limits, locks, pub/sub) across instances.
type memoryCache struct {
	ctx    context.Context
	store  *memoryStore
	prefix string
}

var _ ICache = (*memoryCache)(nil)

// NewMemoryCache returns an in-process cache. Wire it like any other:
//
//	app, _ := core.NewApp(env, core.WithCache("default", core.NewMemoryCache()))
//
// Close stops its expiry sweeper; an App does that for you.
func NewMemoryCache() ICache {
	return &memoryCache{ctx: context.Background(), store: newMemoryStore()}
}

type memoryItem struct {
	value []byte
	// exp is the zero time for a key that never expires.
	exp time.Time
}

func (i memoryItem) expired(now time.Time) bool {
	return !i.exp.IsZero() && now.After(i.exp)
}

type memoryStore struct {
	mu     sync.Mutex
	items  map[string]memoryItem
	broker *memoryBroker

	stop      chan struct{}
	closeOnce sync.Once
}

// sweepInterval is how often expired keys are dropped. Reads expire keys
// lazily anyway; the sweep is what stops a key nobody reads again from being
// held forever.
const sweepInterval = 30 * time.Second

func newMemoryStore() *memoryStore {
	s := &memoryStore{
		items:  map[string]memoryItem{},
		broker: newMemoryBroker(),
		stop:   make(chan struct{}),
	}
	go s.sweep()
	return s
}

func (s *memoryStore) sweep() {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stop:
			return
		case now := <-ticker.C:
			s.mu.Lock()
			for key, item := range s.items {
				if item.expired(now) {
					delete(s.items, key)
				}
			}
			s.mu.Unlock()
		}
	}
}

// get reads under the lock, treating an expired key as absent.
func (s *memoryStore) get(key string) (memoryItem, bool) {
	item, ok := s.items[key]
	if !ok {
		return memoryItem{}, false
	}
	if item.expired(time.Now()) {
		delete(s.items, key)
		return memoryItem{}, false
	}
	return item, true
}

// expiryAt turns a ttl into an absolute deadline, honouring KeepTTL.
func (s *memoryStore) expiryAt(key string, ttl time.Duration) time.Time {
	switch {
	case ttl == KeepTTL:
		if item, ok := s.get(key); ok {
			return item.exp
		}
		return time.Time{}
	case ttl > 0:
		return time.Now().Add(ttl)
	default:
		return time.Time{}
	}
}

// ---------------------------------------------------------------------------
// Handle plumbing
// ---------------------------------------------------------------------------

func (m *memoryCache) WithContext(ctx context.Context) ICache {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *memoryCache) WithPrefix(prefix string) ICache {
	cp := *m
	cp.prefix += normalizePrefix(prefix)
	return &cp
}

func (m *memoryCache) Prefix() string { return m.prefix }

func (m *memoryCache) Enabled() bool { return true }

// Redis is nil: there is no server behind this cache. Code that reaches for the
// raw client has to handle that, which is the point — it says plainly that this
// backend cannot do everything redis can.
func (m *memoryCache) Redis() redis.UniversalClient { return nil }

func (m *memoryCache) Ping() IError { return nil }

func (m *memoryCache) Close() IError {
	m.store.closeOnce.Do(func() {
		close(m.store.stop)
		m.store.broker.close()
	})
	return nil
}

func (m *memoryCache) k(key string) string { return m.prefix + key }

// ---------------------------------------------------------------------------
// Values
// ---------------------------------------------------------------------------

func (m *memoryCache) Get(key string, dest any) IError {
	m.store.mu.Lock()
	item, ok := m.store.get(m.k(key))
	m.store.mu.Unlock()
	if !ok {
		return cacheMiss(key)
	}
	return decodeCacheValue(item.value, dest)
}

func (m *memoryCache) Set(key string, value any, ttl time.Duration) IError {
	raw, err := encodeCacheValue(value)
	if err != nil {
		return err
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	full := m.k(key)
	m.store.items[full] = memoryItem{value: raw, exp: m.store.expiryAt(full, ttl)}
	return nil
}

func (m *memoryCache) SetNX(key string, value any, ttl time.Duration) (bool, IError) {
	raw, err := encodeCacheValue(value)
	if err != nil {
		return false, err
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	full := m.k(key)
	if _, exists := m.store.get(full); exists {
		return false, nil
	}
	m.store.items[full] = memoryItem{value: raw, exp: m.store.expiryAt(full, ttl)}
	return true, nil
}

func (m *memoryCache) GetDel(key string, dest any) IError {
	m.store.mu.Lock()
	full := m.k(key)
	item, ok := m.store.get(full)
	if ok {
		delete(m.store.items, full)
	}
	m.store.mu.Unlock()
	if !ok {
		return cacheMiss(key)
	}
	return decodeCacheValue(item.value, dest)
}

func (m *memoryCache) MGet(keys ...string) (map[string][]byte, IError) {
	out := make(map[string][]byte, len(keys))
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for _, key := range keys {
		if item, ok := m.store.get(m.k(key)); ok {
			out[key] = item.value
		}
	}
	return out, nil
}

func (m *memoryCache) MSet(values map[string]any, ttl time.Duration) IError {
	encoded := make(map[string][]byte, len(values))
	for key, value := range values {
		raw, err := encodeCacheValue(value)
		if err != nil {
			return err
		}
		encoded[key] = raw
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for key, raw := range encoded {
		full := m.k(key)
		m.store.items[full] = memoryItem{value: raw, exp: m.store.expiryAt(full, ttl)}
	}
	return nil
}

func (m *memoryCache) Del(keys ...string) IError {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	for _, key := range keys {
		delete(m.store.items, m.k(key))
	}
	return nil
}

func (m *memoryCache) DelByPrefix(prefix string) (int64, IError) {
	full := m.k(prefix)
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	var deleted int64
	for key := range m.store.items {
		if strings.HasPrefix(key, full) {
			delete(m.store.items, key)
			deleted++
		}
	}
	return deleted, nil
}

func (m *memoryCache) Exists(key string) (bool, IError) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	_, ok := m.store.get(m.k(key))
	return ok, nil
}

func (m *memoryCache) Expire(key string, ttl time.Duration) (bool, IError) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	full := m.k(key)
	item, ok := m.store.get(full)
	if !ok {
		return false, nil
	}
	if ttl > 0 {
		item.exp = time.Now().Add(ttl)
	} else {
		item.exp = time.Time{}
	}
	m.store.items[full] = item
	return true, nil
}

func (m *memoryCache) TTL(key string) (time.Duration, IError) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	item, ok := m.store.get(m.k(key))
	if !ok {
		return 0, cacheMiss(key)
	}
	if item.exp.IsZero() {
		return TTLNoExpiry, nil
	}
	return time.Until(item.exp), nil
}

func (m *memoryCache) Incr(key string, delta int64, ttl time.Duration) (int64, IError) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()

	full := m.k(key)
	item, ok := m.store.get(full)
	var current int64
	if ok {
		parsed, err := strconv.ParseInt(strings.TrimSpace(string(item.value)), 10, 64)
		if err != nil {
			return 0, New(500, "CACHE_INCR", "cache: incr on a value that is not an integer")
		}
		current = parsed
	}
	current += delta

	next := memoryItem{value: []byte(strconv.FormatInt(current, 10)), exp: item.exp}
	// the ttl applies to a counter this call created — the same rule the redis
	// script follows, so a window does not get extended by traffic inside it
	if !ok && ttl > 0 {
		next.exp = time.Now().Add(ttl)
	}
	m.store.items[full] = next
	return current, nil
}

// ---------------------------------------------------------------------------
// Lock
// ---------------------------------------------------------------------------

type memoryLock struct {
	cache *memoryCache
	key   string
	token string

	mu       sync.Mutex
	released bool
}

var _ ILock = (*memoryLock)(nil)

func (m *memoryCache) Lock(key string, ttl time.Duration) (ILock, IError) {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	token := lockToken()
	ok, err := m.SetNX(key, token, ttl)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, lockNotAcquired(key)
	}
	return &memoryLock{cache: m, key: key, token: token}, nil
}

func (l *memoryLock) Key() string { return l.key }

func (l *memoryLock) Extend(ttl time.Duration) IError {
	if ttl <= 0 {
		ttl = defaultLockTTL
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released || !l.cache.extendIfHeld(l.key, l.token, ttl) {
		return Wrap(ErrLockLost, "cache: extend").WithCode("LOCK_LOST").WithStatus(409)
	}
	return nil
}

func (l *memoryLock) Unlock() IError {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	l.cache.delIfHeld(l.key, l.token)
	return nil
}

// delIfHeld and extendIfHeld are the in-memory equivalents of the two Lua
// scripts: compare the token and act in one critical section, so a lock that
// expired mid-work and was taken over is never released or refreshed by its
// previous holder.
func (m *memoryCache) delIfHeld(key, token string) {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	full := m.k(key)
	if item, ok := m.store.get(full); ok && string(item.value) == token {
		delete(m.store.items, full)
	}
}

func (m *memoryCache) extendIfHeld(key, token string, ttl time.Duration) bool {
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	full := m.k(key)
	item, ok := m.store.get(full)
	if !ok || string(item.value) != token {
		return false
	}
	item.exp = time.Now().Add(ttl)
	m.store.items[full] = item
	return true
}

// ---------------------------------------------------------------------------
// In-process pub/sub
// ---------------------------------------------------------------------------

func (m *memoryCache) PubSub() IPubSub {
	return &memoryPubSub{ctx: m.ctx, broker: m.store.broker, prefix: m.prefix}
}

// memoryBroker fans messages out to the subscriptions of one memoryCache.
type memoryBroker struct {
	mu     sync.Mutex
	subs   map[*memorySubscription]struct{}
	closed bool
}

func newMemoryBroker() *memoryBroker {
	return &memoryBroker{subs: map[*memorySubscription]struct{}{}}
}

func (b *memoryBroker) add(s *memorySubscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subs[s] = struct{}{}
}

func (b *memoryBroker) remove(s *memorySubscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.subs, s)
}

func (b *memoryBroker) close() {
	b.mu.Lock()
	subs := make([]*memorySubscription, 0, len(b.subs))
	for s := range b.subs {
		subs = append(subs, s)
	}
	b.closed = true
	b.subs = map[*memorySubscription]struct{}{}
	b.mu.Unlock()

	for _, s := range subs {
		_ = s.Close()
	}
}

// publish delivers to every matching subscription. Delivery blocks while a
// subscriber's buffer is full — dropping messages silently would make a test
// pass that should not — but never past the caller's context.
func (b *memoryBroker) publish(ctx context.Context, channel string, payload []byte) {
	b.mu.Lock()
	targets := make([]*memorySubscription, 0, len(b.subs))
	for s := range b.subs {
		targets = append(targets, s)
	}
	b.mu.Unlock()

	for _, s := range targets {
		pattern, ok := s.matches(channel)
		if !ok {
			continue
		}
		// subscribers see their own channel names, never the namespace the
		// messages actually travel under
		msg := &PubSubMessage{
			Channel: s.strip(channel),
			Pattern: s.strip(pattern),
			Payload: append([]byte(nil), payload...),
		}
		select {
		case s.ch <- msg:
		case <-s.done:
		case <-ctx.Done():
			return
		}
	}
}

type memorySubscription struct {
	broker   *memoryBroker
	ch       chan *PubSubMessage
	done     chan struct{}
	closeOne sync.Once
	prefix   string

	mu       sync.Mutex
	channels []string
	patterns []string
}

// strip removes the namespace a channel travels under, so a subscriber reads
// back the name it subscribed with.
func (s *memorySubscription) strip(channel string) string {
	if channel == "" {
		return ""
	}
	return strings.TrimPrefix(channel, s.prefix)
}

var _ ISubscription = (*memorySubscription)(nil)

// memorySubscriptionBuffer is deliberately generous: an in-process broker is
// used in tests, where the subscriber often only starts reading after the
// publisher has finished.
const memorySubscriptionBuffer = 256

func (s *memorySubscription) matches(channel string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.channels {
		if c == channel {
			return "", true
		}
	}
	for _, p := range s.patterns {
		if matchChannel(p, channel) {
			return p, true
		}
	}
	return "", false
}

func (s *memorySubscription) C() <-chan *PubSubMessage { return s.ch }

func (s *memorySubscription) Channels() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.channels)+len(s.patterns))
	for _, c := range s.channels {
		out = append(out, s.strip(c))
	}
	for _, p := range s.patterns {
		out = append(out, s.strip(p))
	}
	return out
}

func (s *memorySubscription) Close() IError {
	s.closeOne.Do(func() {
		close(s.done)
		s.broker.remove(s)
		close(s.ch)
	})
	return nil
}

type memoryPubSub struct {
	ctx    context.Context
	broker *memoryBroker
	prefix string
}

var _ IPubSub = (*memoryPubSub)(nil)

func (p *memoryPubSub) Enabled() bool { return true }

func (p *memoryPubSub) Channel(name string) string { return p.prefix + name }

func (p *memoryPubSub) WithContext(ctx context.Context) IPubSub {
	if ctx == nil {
		ctx = context.Background()
	}
	cp := *p
	cp.ctx = ctx
	return &cp
}

func (p *memoryPubSub) Publish(channel string, msg any) IError {
	payload, err := encodeCacheValue(msg)
	if err != nil {
		return err
	}
	p.broker.mu.Lock()
	closed := p.broker.closed
	p.broker.mu.Unlock()
	if closed {
		return Wrap(ErrCacheDisabled, "pubsub: publish")
	}
	p.broker.publish(p.ctx, p.Channel(channel), payload)
	return nil
}

func (p *memoryPubSub) Subscribe(channels ...string) (ISubscription, IError) {
	return p.subscribe(channels, nil)
}

func (p *memoryPubSub) PSubscribe(patterns ...string) (ISubscription, IError) {
	return p.subscribe(nil, patterns)
}

func (p *memoryPubSub) subscribe(channels, patterns []string) (ISubscription, IError) {
	if len(channels) == 0 && len(patterns) == 0 {
		return nil, New(400, "PUBSUB_NO_CHANNEL", "pubsub: subscribe needs at least one channel")
	}
	p.broker.mu.Lock()
	closed := p.broker.closed
	p.broker.mu.Unlock()
	if closed {
		return nil, Wrap(ErrCacheDisabled, "pubsub: subscribe")
	}

	sub := &memorySubscription{
		broker:   p.broker,
		ch:       make(chan *PubSubMessage, memorySubscriptionBuffer),
		done:     make(chan struct{}),
		prefix:   p.prefix,
		channels: p.qualify(channels),
		patterns: p.qualify(patterns),
	}
	p.broker.add(sub)
	return sub, nil
}

func (p *memoryPubSub) qualify(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, p.Channel(n))
	}
	return out
}

func (p *memoryPubSub) Close() IError { return nil }
