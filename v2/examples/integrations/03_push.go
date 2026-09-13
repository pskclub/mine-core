package main

import (
	"context"
	"errors"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 3: push notifications ------------------------------------------
//
// core.Pusher(ctx) — a function taking a context.Context, for the same reason as
// Mailer and Requester. With no FIREBASE_CREDENTIAL every call fails with
// PUSH_DISABLED rather than returning nil.

// notifyShipped sends to one device. Title/Body are what the device *shows*;
// Data is what the app *receives*.
//
// Note what is not in the payload: nothing sensitive. A notification is drawn on
// a lock screen and passes through Google's and Apple's infrastructure, so send
// an id and let the app fetch the real thing behind the user's session.
func notifyShipped(ctx context.Context, token, orderID string) core.IError {
	badge := 1
	return core.Pusher(ctx).Send(token, core.PushMessage{
		Title: "ออเดอร์ถูกจัดส่งแล้ว",
		Body:  "พัสดุของคุณออกจากคลังแล้ว",
		Data:  map[string]string{"order_id": orderID},

		// High priority wakes the device immediately. Reserve it for something a
		// person is actually waiting for — providers throttle a sender that marks
		// everything high, and then the messages that matter arrive late too.
		Priority:  core.PushPriorityHigh,
		Sound:     "ping.caf",
		Badge:     &badge, // a pointer, because 0 means "clear the badge"
		ChannelID: "orders",

		// after five minutes this notification is a lie, so do not deliver it
		TTL: 5 * time.Minute,
		// a device coming back online gets the latest state of this order, not a
		// backlog of every step it missed
		CollapseKey: "order-" + orderID,
	})
}

// syncSilently sends data with no notification block. The system draws nothing
// and the app is simply woken to sync — which is why the framework sets
// content-available for iOS automatically; without it iOS drops the message.
func syncSilently(ctx context.Context, token string) core.IError {
	return core.Pusher(ctx).Send(token, core.PushMessage{
		Data: map[string]string{"action": "sync"},
	})
}

// broadcastToDevices sends to many tokens and cleans up as it goes.
//
// The distinction that matters: a returned error means the *call* failed, so
// nothing in that batch was attempted. A token failing is not an error — it is a
// result, and it lives in BatchResult. Batching to the provider's limit of 500 is
// handled inside, so a caller with ten thousand tokens does not have to know.
func broadcastToDevices(ctx context.Context, tokens []string, msg core.PushMessage) ([]string, core.IError) {
	res, err := core.Pusher(ctx).SendMulticast(tokens, msg)
	if err != nil {
		return nil, err
	}

	// Dead tokens — the app was uninstalled, or the token was rotated. They will
	// never work again, and a store full of them makes every broadcast slower and
	// every "delivered" number a lie. Delete them the moment you learn.
	dead := res.UnregisteredTokens()

	// per token, the same fact is available without knowing anything about the
	// provider's error types
	for _, r := range res.Results {
		if !r.Success && errors.Is(r.Error, core.ErrPushUnregistered) {
			_ = r.Token // repo.DeleteToken(r.Token)
		}
	}
	return dead, nil
}

// announceToTopic is the other addressing mode. A topic is right for broad news
// — the provider does the fan-out and you do not keep a token list. It is wrong
// for anything personal: a topic cannot be cancelled for one person, and you
// cannot see who is on it.
func announceToTopic(ctx context.Context, topic string, msg core.PushMessage) core.IError {
	p := core.Pusher(ctx)
	if err := p.SendToTopic(topic, msg); err != nil {
		return err
	}
	// conditions combine topics without a second send
	return p.SendToCondition("'"+topic+"' in topics && 'th' in topics", msg)
}

// manageSubscriptions batches to the provider's topic limit of 1000 internally.
func manageSubscriptions(ctx context.Context, topic string, tokens []string) core.IError {
	_, err := core.Pusher(ctx).Subscribe(topic, tokens...)
	return err
}

// validatePayload asks the provider whether a message is well-formed without
// delivering it. Worth doing whenever the payload shape changes: a malformed
// payload fails silently on the user's device, not on our server, so nothing in
// our logs would ever mention it.
func validatePayload(ctx context.Context, token string, msg core.PushMessage) core.IError {
	return core.Pusher(ctx).Validate(token, msg)
}

// registerPushJobs puts the fan-out in a job. Sending to tens of thousands of
// people is not a request's work, and it needs bounded retries.
func registerPushJobs(reg *core.JobRegistry) {
	_ = reg.Register(core.JobDef{
		Name:        "push.broadcast",
		Description: "send an announcement to every registered device",
		Timeout:     30 * time.Minute,
		MaxAttempts: 3,
	}, func(c core.ICronjobContext) error {
		dead, err := broadcastToDevices(c, []string{"tok-1", "tok-2"}, core.PushMessage{
			Title: "มีอัปเดตใหม่", Body: "เปิดแอปเพื่อดูรายละเอียด",
		})
		if err != nil {
			return err
		}
		c.Log().Info("broadcast finished", "dead_tokens", len(dead))
		return nil
	})
}

// Testing: NewMemoryPusher records deliveries instead of making them. Assert on
// the payload — that is what the user sees; that a method was called is not.
//
//	p := core.NewMemoryPusher()
//	app, _ := core.NewApp(env, core.WithPusher(p))
//
//	require.NoError(t, notifyShipped(app.NewContext(context.Background()), "tok-1", "o-1"))
//
//	sent := core.SentPushes(p)
//	require.Len(t, sent, 1)
//	assert.Equal(t, []string{"tok-1"}, sent[0].Tokens)
//	assert.Equal(t, "o-1", sent[0].Message.Data["order_id"])
//	assert.Equal(t, []string{"tok-1"}, core.PushTopicTokens(p, "news"))
//	core.ResetPushes(p)
func sentTitles(p core.IPusher) []string {
	sent := core.SentPushes(p)
	out := make([]string, 0, len(sent))
	for _, s := range sent {
		out = append(out, s.Message.Title)
	}
	return out
}
