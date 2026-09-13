package core

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

// ErrPushDisabled is wrapped by the error every operation returns when no
// Firebase credentials are configured.
var ErrPushDisabled = errors.New("push: not configured")

// ErrPushUnregistered is wrapped by a per-token failure that means the token is
// dead — the app was uninstalled, or the token was replaced. Delete it.
//
//	if errors.Is(res.Error, core.ErrPushUnregistered) {
//	    _ = repo.DeleteToken(res.Token)
//	}
var ErrPushUnregistered = errors.New("push: token is not registered")

// DefaultPushTimeout bounds one send.
const DefaultPushTimeout = 15 * time.Second

// fcmMulticastLimit is how many tokens FCM accepts in one multicast call.
const fcmMulticastLimit = 500

// fcmTopicSubscribeLimit is how many tokens the topic-management API takes.
const fcmTopicSubscribeLimit = 1000

// PushPriority is how urgently the device should be woken.
type PushPriority uint8

const (
	// PushPriorityNormal lets the device batch the delivery to save battery.
	PushPriorityNormal PushPriority = iota
	// PushPriorityHigh wakes the device immediately. Reserve it for messages a
	// person is waiting for: providers throttle a sender that marks everything
	// high.
	PushPriorityHigh
)

// PushMessage is a notification to deliver.
//
// Notification (Title/Body) is what the device shows; Data is what the app
// receives. A message with only Data is a silent one — delivered to the app
// without the system drawing anything — which is how a background sync is
// triggered.
type PushMessage struct {
	Title string
	Body  string
	// Image is a URL shown with the notification.
	Image string
	// Data is the custom payload. FCM carries strings only.
	Data map[string]string

	// Android, APNS and Web tune one platform each: channel and priority on
	// Android, sound and badge on iOS, actions and icon on the web. A config set
	// here is used as it is, and the shorthand fields below are ignored for that
	// platform.
	Android    *messaging.AndroidConfig
	APNS       *messaging.APNSConfig
	Web        *messaging.WebpushConfig
	FCMOptions *messaging.FCMOptions

	// Sound, Badge, ChannelID and Priority are the settings wanted often enough
	// not to be worth building a platform config for.
	Sound     string
	Badge     *int
	ChannelID string
	Priority  PushPriority

	// TTL is how long FCM keeps trying. Zero is the provider's default (four
	// weeks); a short one suits a message that is worthless when it is late.
	TTL time.Duration

	// CollapseKey replaces any undelivered message carrying the same key, so a
	// device coming back online gets the latest rather than the backlog.
	CollapseKey string
}

// PushResult is one token's outcome inside a batch.
type PushResult struct {
	Token     string
	Success   bool
	MessageID string
	// Error is the per-token failure. It wraps ErrPushUnregistered when the
	// token is dead and should be deleted.
	Error error
}

// BatchResult summarises a multi-token send.
type BatchResult struct {
	SuccessCount int
	FailureCount int
	Results      []PushResult
}

// UnregisteredTokens are the tokens the provider rejected as dead. Delete them:
// they will never work again, and a store full of them turns every broadcast
// into a slow one.
func (b *BatchResult) UnregisteredTokens() []string {
	if b == nil {
		return nil
	}
	out := make([]string, 0)
	for _, r := range b.Results {
		if !r.Success && errors.Is(r.Error, ErrPushUnregistered) {
			out = append(out, r.Token)
		}
	}
	return out
}

// Pusher returns the application's push sender bound to ctx.
//
// It is a function rather than a method on IContext, for the same reason
// core.Requester and core.Mailer are: sending a notification is not a capability
// of the request — it is something code does. Taking only a context.Context is
// what lets a service that knows nothing about this framework send one:
//
//	func (s *OrderService) NotifyShipped(ctx context.Context, o Order) error {
//	    return core.Pusher(ctx).Send(o.DeviceToken, msg)
//	}
//
// A context carrying no App gets the disabled pusher rather than nil, so a call
// site never has to nil-check. Every call on it fails with PUSH_DISABLED.
func Pusher(ctx context.Context) IPusher {
	if app := appFrom(ctx); app != nil && app.pusher != nil {
		return app.pusher.WithContext(ctx)
	}
	return noopPusher{}
}

// IPusher sends push notifications (FCM).
//
// core.Pusher(ctx) is never nil: a service with no Firebase credentials gets a
// disabled pusher whose every call fails with PUSH_DISABLED.
type IPusher interface {
	// Send delivers to one device token.
	Send(token string, msg PushMessage) IError
	// SendMulticast delivers to many tokens, in batches of 500, and reports each
	// one. A token failing is not an error — read BatchResult.
	SendMulticast(tokens []string, msg PushMessage) (*BatchResult, IError)
	// SendToTopic delivers to everyone subscribed to a topic.
	SendToTopic(topic string, msg PushMessage) IError
	// SendToCondition delivers to the devices matching a topic condition, e.g.
	// "'news' in topics && 'th' in topics".
	SendToCondition(condition string, msg PushMessage) IError
	// Subscribe adds tokens to a topic.
	Subscribe(topic string, tokens ...string) (*BatchResult, IError)
	// Unsubscribe removes tokens from a topic.
	Unsubscribe(topic string, tokens ...string) (*BatchResult, IError)
	// Validate checks a message against the provider without delivering it —
	// FCM's dry run.
	Validate(token string, msg PushMessage) IError
	// Enabled reports whether this is a real pusher.
	Enabled() bool
	// WithContext returns a handle bound to a different context.
	WithContext(ctx context.Context) IPusher
}

// PusherOption tunes a pusher at construction.
type PusherOption func(*pusherConfig)

type pusherConfig struct {
	timeout   time.Duration
	projectID string
	opts      []option.ClientOption
}

// WithPushTimeout bounds one send (default DefaultPushTimeout).
func WithPushTimeout(d time.Duration) PusherOption {
	return func(c *pusherConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithPushProjectID names the Firebase project explicitly, for credentials that
// do not carry one.
func WithPushProjectID(id string) PusherOption {
	return func(c *pusherConfig) { c.projectID = id }
}

// WithPushClientOptions passes Google API client options through — a custom HTTP
// client, an alternative credential source.
func WithPushClientOptions(opts ...option.ClientOption) PusherOption {
	return func(c *pusherConfig) { c.opts = append(c.opts, opts...) }
}

type pusher struct {
	ctx     context.Context
	client  *messaging.Client
	timeout time.Duration
}

var _ IPusher = (*pusher)(nil)

// NewPusher builds an FCM client. credentialJSON is the service-account JSON
// (ENVConfig.FirebaseCredential); pass an empty string to use the machine's
// ambient Google credentials, which is what a workload identity provides.
func NewPusher(credentialJSON string, opts ...PusherOption) (IPusher, IError) {
	c := &pusherConfig{timeout: DefaultPushTimeout}
	for _, o := range opts {
		o(c)
	}

	clientOpts := c.opts
	if strings.TrimSpace(credentialJSON) != "" {
		// deprecated upstream because a credential configuration from an
		// untrusted source must be validated first. This one comes from the
		// service's own configuration, which is as trusted as the binary.
		//nolint:staticcheck // SA1019: the replacement requires a credential type we cannot infer here
		clientOpts = append(clientOpts, option.WithCredentialsJSON([]byte(credentialJSON)))
	}

	// bounded, so a service whose credentials cannot be resolved fails to boot
	// rather than hanging on the metadata server
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	app, err := firebase.NewApp(ctx, &firebase.Config{ProjectID: c.projectID}, clientOpts...)
	if err != nil {
		return nil, Wrap(err, "push: init app")
	}
	client, err := app.Messaging(ctx)
	if err != nil {
		return nil, Wrap(err, "push: messaging client")
	}
	return &pusher{ctx: context.Background(), client: client, timeout: c.timeout}, nil
}

// NewPusherFromEnv builds a pusher from FIREBASE_CREDENTIAL.
func NewPusherFromEnv(env IENV, opts ...PusherOption) (IPusher, IError) {
	return NewPusher(env.Config().FirebaseCredential, opts...)
}

func (p *pusher) WithContext(ctx context.Context) IPusher {
	cp := *p
	cp.ctx = ctx
	return &cp
}

func (p *pusher) Enabled() bool { return true }

func (p *pusher) call() (context.Context, context.CancelFunc) {
	return context.WithTimeout(p.ctx, p.timeout)
}

func (p *pusher) Send(token string, msg PushMessage) IError {
	if token == "" {
		return New(400, "PUSH_NO_TOKEN", "push: no device token")
	}
	return p.send(p.message(msg, func(m *messaging.Message) { m.Token = token }), "send")
}

func (p *pusher) SendToTopic(topic string, msg PushMessage) IError {
	if topic == "" {
		return New(400, "PUSH_NO_TOPIC", "push: no topic")
	}
	return p.send(p.message(msg, func(m *messaging.Message) { m.Topic = topic }), "topic")
}

func (p *pusher) SendToCondition(condition string, msg PushMessage) IError {
	if condition == "" {
		return New(400, "PUSH_NO_CONDITION", "push: no condition")
	}
	return p.send(p.message(msg, func(m *messaging.Message) { m.Condition = condition }), "condition")
}

func (p *pusher) send(m *messaging.Message, kind string) IError {
	ctx, cancel := p.call()
	defer cancel()

	id, err := p.client.Send(ctx, m)
	if err != nil {
		breadcrumbTo(p.ctx, Breadcrumb{
			Type: "error", Category: "push." + kind, Level: LevelError,
			Data: map[string]any{"error": err.Error()},
		})
		return pushError(err)
	}
	breadcrumbTo(p.ctx, Breadcrumb{
		Category: "push." + kind,
		Data:     map[string]any{"message_id": id},
	})
	return nil
}

func (p *pusher) Validate(token string, msg PushMessage) IError {
	ctx, cancel := p.call()
	defer cancel()

	_, err := p.client.SendDryRun(ctx, p.message(msg, func(m *messaging.Message) { m.Token = token }))
	if err != nil {
		return pushError(err)
	}
	return nil
}

// SendMulticast delivers to every token, in batches of 500 — the provider's
// limit, applied here so a caller with ten thousand tokens does not have to know
// about it.
func (p *pusher) SendMulticast(tokens []string, msg PushMessage) (*BatchResult, IError) {
	tokens = nonEmptyStrings(tokens)
	if len(tokens) == 0 {
		return &BatchResult{Results: []PushResult{}}, nil
	}

	total := &BatchResult{Results: make([]PushResult, 0, len(tokens))}
	base := p.message(msg, nil)

	for _, batch := range chunkStrings(tokens, fcmMulticastLimit) {
		ctx, cancel := p.call()
		resp, err := p.client.SendEachForMulticast(ctx, &messaging.MulticastMessage{
			Tokens:       batch,
			Notification: base.Notification,
			Data:         base.Data,
			Android:      base.Android,
			APNS:         base.APNS,
			Webpush:      base.Webpush,
			FCMOptions:   base.FCMOptions,
		})
		cancel()

		if err != nil {
			// the call itself failed, so nothing in this batch was attempted —
			// that is an error, not a set of per-token failures
			return total, pushError(err)
		}
		for i, r := range resp.Responses {
			res := PushResult{Token: batch[i], Success: r.Success, MessageID: r.MessageID}
			if r.Error != nil {
				res.Error = perTokenPushError(r.Error)
				total.FailureCount++
			} else {
				total.SuccessCount++
			}
			total.Results = append(total.Results, res)
		}
	}

	breadcrumbTo(p.ctx, Breadcrumb{
		Category: "push.multicast",
		Data:     map[string]any{"ok": total.SuccessCount, "failed": total.FailureCount},
	})
	return total, nil
}

func (p *pusher) Subscribe(topic string, tokens ...string) (*BatchResult, IError) {
	return p.topicManagement(topic, tokens, true)
}

func (p *pusher) Unsubscribe(topic string, tokens ...string) (*BatchResult, IError) {
	return p.topicManagement(topic, tokens, false)
}

func (p *pusher) topicManagement(topic string, tokens []string, subscribe bool) (*BatchResult, IError) {
	if topic == "" {
		return nil, New(400, "PUSH_NO_TOPIC", "push: no topic")
	}
	tokens = nonEmptyStrings(tokens)
	if len(tokens) == 0 {
		return &BatchResult{Results: []PushResult{}}, nil
	}

	total := &BatchResult{Results: make([]PushResult, 0, len(tokens))}
	for _, batch := range chunkStrings(tokens, fcmTopicSubscribeLimit) {
		ctx, cancel := p.call()
		var (
			resp *messaging.TopicManagementResponse
			err  error
		)
		if subscribe {
			resp, err = p.client.SubscribeToTopic(ctx, batch, topic)
		} else {
			resp, err = p.client.UnsubscribeFromTopic(ctx, batch, topic)
		}
		cancel()

		if err != nil {
			return total, pushError(err)
		}

		// the provider reports failures by index into the batch and says nothing
		// about the rest, so the successes are whatever is left over
		failed := make(map[int]string, len(resp.Errors))
		for _, e := range resp.Errors {
			failed[e.Index] = e.Reason
		}
		for i, token := range batch {
			res := PushResult{Token: token, Success: true}
			if reason, bad := failed[i]; bad {
				res.Success = false
				res.Error = errors.New(reason)
				total.FailureCount++
			} else {
				total.SuccessCount++
			}
			total.Results = append(total.Results, res)
		}
	}
	return total, nil
}

// message builds the provider's message from ours. A per-platform config the
// caller supplied is used as it is; the shorthand fields only fill in platforms
// they left alone.
func (p *pusher) message(msg PushMessage, target func(*messaging.Message)) *messaging.Message {
	out := &messaging.Message{
		Data:       msg.Data,
		Android:    msg.Android,
		APNS:       msg.APNS,
		Webpush:    msg.Web,
		FCMOptions: msg.FCMOptions,
	}
	if msg.Title != "" || msg.Body != "" || msg.Image != "" {
		out.Notification = &messaging.Notification{
			Title:    msg.Title,
			Body:     msg.Body,
			ImageURL: msg.Image,
		}
	}

	applyAndroidPush(out, msg)
	applyAPNSPush(out, msg)

	if target != nil {
		target(out)
	}
	return out
}

// applyAndroidPush fills the Android config from the shorthand fields.
func applyAndroidPush(out *messaging.Message, msg PushMessage) {
	needs := msg.Priority == PushPriorityHigh || msg.TTL > 0 ||
		msg.CollapseKey != "" || msg.ChannelID != "" || msg.Sound != ""
	if out.Android != nil || !needs {
		return
	}

	cfg := &messaging.AndroidConfig{CollapseKey: msg.CollapseKey}
	if msg.Priority == PushPriorityHigh {
		cfg.Priority = "high"
	}
	if msg.TTL > 0 {
		ttl := msg.TTL
		cfg.TTL = &ttl
	}
	if msg.ChannelID != "" || msg.Sound != "" {
		cfg.Notification = &messaging.AndroidNotification{
			ChannelID: msg.ChannelID,
			Sound:     msg.Sound,
		}
	}
	out.Android = cfg
}

// applyAPNSPush does the same for iOS. Priority is a header there, and a badge
// of zero is meaningful — it clears the badge — so it is a pointer.
func applyAPNSPush(out *messaging.Message, msg PushMessage) {
	silent := out.Notification == nil
	needs := msg.Priority == PushPriorityHigh || msg.Sound != "" || msg.Badge != nil || silent
	if out.APNS != nil || !needs {
		return
	}

	aps := &messaging.Aps{Sound: msg.Sound, Badge: msg.Badge}
	// iOS drops a message with no alert unless it is marked as one the app
	// should be woken for
	if silent {
		aps.ContentAvailable = true
	}

	cfg := &messaging.APNSConfig{Payload: &messaging.APNSPayload{Aps: aps}}
	if msg.Priority == PushPriorityHigh {
		cfg.Headers = map[string]string{"apns-priority": "10"}
	}
	out.APNS = cfg
}

// pushError maps a provider failure to a framework error, keeping the ones a
// caller can act on apart from the ones it cannot.
func pushError(err error) IError {
	switch {
	case messaging.IsUnregistered(err):
		return New(410, "PUSH_TOKEN_UNREGISTERED", "push: the device token is no longer registered").
			WithCause(errors.Join(ErrPushUnregistered, err))
	case messaging.IsInvalidArgument(err):
		return Wrap(err, "push: invalid message").WithStatus(400).WithCode("PUSH_INVALID_MESSAGE")
	case messaging.IsQuotaExceeded(err):
		return Wrap(err, "push: rate limited").WithStatus(429).WithCode("PUSH_RATE_LIMITED")
	case messaging.IsSenderIDMismatch(err):
		return Wrap(err, "push: token belongs to another sender").
			WithStatus(403).WithCode("PUSH_SENDER_MISMATCH")
	default:
		return Wrap(err, "push: send")
	}
}

// perTokenPushError marks a dead token so a caller finds it with errors.Is,
// without knowing anything about the provider's error types.
func perTokenPushError(err error) error {
	if messaging.IsUnregistered(err) {
		return errors.Join(ErrPushUnregistered, err)
	}
	return err
}

func nonEmptyStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

// chunkStrings splits a list into runs of at most size.
func chunkStrings(in []string, size int) [][]string {
	out := make([][]string, 0, (len(in)+size-1)/size)
	for start := 0; start < len(in); start += size {
		out = append(out, in[start:min(start+size, len(in))])
	}
	return out
}

// ---------------------------------------------------------------------------
// Memory pusher (tests)
// ---------------------------------------------------------------------------

// SentPush is one delivery a memory pusher recorded. Exactly one of Tokens,
// Topic and Condition is set.
type SentPush struct {
	Tokens    []string
	Topic     string
	Condition string
	Message   PushMessage
	// DryRun is set for a Validate call.
	DryRun bool
}

type memoryPusher struct {
	ctx context.Context
	// the recorder is shared by pointer, so a handle from WithContext records
	// into the same list the test holds
	rec *pushRecorder
}

type pushRecorder struct {
	mu     sync.Mutex
	sent   []SentPush
	topics map[string]map[string]struct{}
}

var _ IPusher = (*memoryPusher)(nil)

// NewMemoryPusher returns a pusher that records deliveries instead of making
// them, so a test can assert on what a service would have sent:
//
//	p := core.NewMemoryPusher()
//	app, _ := core.NewApp(env, core.WithPusher(p))
//	...
//	sent := core.SentPushes(p)
//	require.Len(t, sent, 1)
//	assert.Equal(t, "Order shipped", sent[0].Message.Title)
func NewMemoryPusher() IPusher {
	return &memoryPusher{
		ctx: context.Background(),
		rec: &pushRecorder{topics: map[string]map[string]struct{}{}},
	}
}

// SentPushes returns what a memory pusher recorded, in order. Nil for any other
// pusher.
func SentPushes(p IPusher) []SentPush {
	mp, ok := p.(*memoryPusher)
	if !ok {
		return nil
	}
	mp.rec.mu.Lock()
	defer mp.rec.mu.Unlock()
	return append([]SentPush(nil), mp.rec.sent...)
}

// PushTopicTokens returns the tokens a memory pusher believes are subscribed to
// a topic, sorted.
func PushTopicTokens(p IPusher, topic string) []string {
	mp, ok := p.(*memoryPusher)
	if !ok {
		return nil
	}
	mp.rec.mu.Lock()
	defer mp.rec.mu.Unlock()

	out := make([]string, 0, len(mp.rec.topics[topic]))
	for token := range mp.rec.topics[topic] {
		out = append(out, token)
	}
	sort.Strings(out)
	return out
}

// ResetPushes drops everything a memory pusher recorded, subscriptions included.
func ResetPushes(p IPusher) {
	if mp, ok := p.(*memoryPusher); ok {
		mp.rec.mu.Lock()
		defer mp.rec.mu.Unlock()
		mp.rec.sent = nil
		mp.rec.topics = map[string]map[string]struct{}{}
	}
}

func (p *memoryPusher) record(s SentPush) {
	p.rec.mu.Lock()
	defer p.rec.mu.Unlock()
	p.rec.sent = append(p.rec.sent, s)
}

func (p *memoryPusher) Send(token string, msg PushMessage) IError {
	if token == "" {
		return New(400, "PUSH_NO_TOKEN", "push: no device token")
	}
	p.record(SentPush{Tokens: []string{token}, Message: msg})
	return nil
}

func (p *memoryPusher) SendMulticast(tokens []string, msg PushMessage) (*BatchResult, IError) {
	tokens = nonEmptyStrings(tokens)
	if len(tokens) == 0 {
		return &BatchResult{Results: []PushResult{}}, nil
	}
	p.record(SentPush{Tokens: tokens, Message: msg})

	out := &BatchResult{SuccessCount: len(tokens), Results: make([]PushResult, 0, len(tokens))}
	for _, t := range tokens {
		out.Results = append(out.Results, PushResult{Token: t, Success: true, MessageID: "memory"})
	}
	return out, nil
}

func (p *memoryPusher) SendToTopic(topic string, msg PushMessage) IError {
	if topic == "" {
		return New(400, "PUSH_NO_TOPIC", "push: no topic")
	}
	p.record(SentPush{Topic: topic, Message: msg})
	return nil
}

func (p *memoryPusher) SendToCondition(condition string, msg PushMessage) IError {
	if condition == "" {
		return New(400, "PUSH_NO_CONDITION", "push: no condition")
	}
	p.record(SentPush{Condition: condition, Message: msg})
	return nil
}

func (p *memoryPusher) Validate(token string, msg PushMessage) IError {
	p.record(SentPush{Tokens: []string{token}, Message: msg, DryRun: true})
	return nil
}

func (p *memoryPusher) Subscribe(topic string, tokens ...string) (*BatchResult, IError) {
	return p.subscription(topic, tokens, true)
}

func (p *memoryPusher) Unsubscribe(topic string, tokens ...string) (*BatchResult, IError) {
	return p.subscription(topic, tokens, false)
}

func (p *memoryPusher) subscription(topic string, tokens []string, add bool) (*BatchResult, IError) {
	if topic == "" {
		return nil, New(400, "PUSH_NO_TOPIC", "push: no topic")
	}
	tokens = nonEmptyStrings(tokens)

	p.rec.mu.Lock()
	defer p.rec.mu.Unlock()
	if p.rec.topics[topic] == nil {
		p.rec.topics[topic] = map[string]struct{}{}
	}

	out := &BatchResult{SuccessCount: len(tokens), Results: make([]PushResult, 0, len(tokens))}
	for _, t := range tokens {
		if add {
			p.rec.topics[topic][t] = struct{}{}
		} else {
			delete(p.rec.topics[topic], t)
		}
		out.Results = append(out.Results, PushResult{Token: t, Success: true})
	}
	return out, nil
}

func (p *memoryPusher) Enabled() bool { return true }

func (p *memoryPusher) WithContext(ctx context.Context) IPusher {
	cp := *p
	cp.ctx = ctx
	return &cp
}

// ---------------------------------------------------------------------------
// Disabled pusher
// ---------------------------------------------------------------------------

type noopPusher struct{}

var _ IPusher = noopPusher{}

// NewNoopPusher returns a pusher that refuses every operation. It is what a
// service with no Firebase credentials gets, so the failure is a clear error
// instead of a nil dereference.
func NewNoopPusher() IPusher { return noopPusher{} }

func (noopPusher) Send(string, PushMessage) IError            { return pushDisabled() }
func (noopPusher) SendToTopic(string, PushMessage) IError     { return pushDisabled() }
func (noopPusher) SendToCondition(string, PushMessage) IError { return pushDisabled() }
func (noopPusher) Validate(string, PushMessage) IError        { return pushDisabled() }
func (noopPusher) Enabled() bool                              { return false }
func (n noopPusher) WithContext(context.Context) IPusher      { return n }

func (noopPusher) SendMulticast([]string, PushMessage) (*BatchResult, IError) {
	return nil, pushDisabled()
}
func (noopPusher) Subscribe(string, ...string) (*BatchResult, IError)   { return nil, pushDisabled() }
func (noopPusher) Unsubscribe(string, ...string) (*BatchResult, IError) { return nil, pushDisabled() }

func pushDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "PUSH_DISABLED",
		Message: "push: no Firebase credentials are configured (set FIREBASE_CREDENTIAL)",
		cause:   ErrPushDisabled,
	}
}
