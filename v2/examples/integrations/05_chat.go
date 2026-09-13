package main

import (
	"context"
	"strconv"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 5: chat (Slack) ------------------------------------------------
//
// core.Chat(ctx), for the same reason as core.Mailer: posting a message is
// something the code goes and does, not a capability the request has. A service
// that knows nothing about this framework can still post, because the function
// takes a plain context.Context.
//
// Chat does not degrade quietly. With no CHAT_SLACK_TOKEN every call fails with
// CHAT_DISABLED — an alert that was silently dropped is an incident nobody was
// told about, and the log says nothing either, because being the thing somebody
// reads was the whole point of the message.
//
// Providers are named the way SQL connections are — core.Chats(ctx, "line") —
// because one service routinely posts to more than one place.

// announceDeploy is the simplest thing there is: a line in a channel. With no To
// it goes to CHAT_SLACK_CHANNEL, so a service that only ever posts to one place
// never mentions a channel at all.
func announceDeploy(ctx context.Context, version string) core.IError {
	_, err := core.Chat(ctx).Send(core.ChatMessage{
		Text: "deployed " + version,
	})
	return err
}

// reportImportFailure is what an alert should look like. The fields are what
// make it scannable — the same keys in the same order every time, rather than a
// sentence somebody has to read at 3am — and Level is what makes it red without
// this service inventing its own palette.
func reportImportFailure(ctx context.Context, file string, rejected int, runURL string) core.IError {
	_, err := core.Chat(ctx).Send(core.ChatMessage{
		To:    "#ops",
		Title: "Import failed",
		Text:  "the run stopped before it finished",
		Level: core.ChatError,
		Fields: []core.ChatField{
			{Name: "file", Value: file, Inline: true},
			{Name: "rejected", Value: strconv.Itoa(rejected), Inline: true},
		},
		Links: []core.ChatLink{{Text: "run log", URL: runURL}},
	})
	return err
}

// reportProgress posts once and then hangs everything else off that message.
//
// The id a Send returns is the thread: without it a long job writes twenty lines
// into the channel and buries whatever else was being discussed there.
func reportProgress(ctx context.Context, job string) core.IError {
	started, err := core.Chat(ctx).Send(core.ChatMessage{
		To: "#ops", Title: job + " started", Level: core.ChatInfo,
	})
	if err != nil {
		return err
	}

	_, err = core.Chat(ctx).Send(core.ChatMessage{
		To:       "#ops",
		Text:     "halfway",
		ThreadID: started.ID,
	})
	return err
}

// postBlockKit reaches past the portable fields. Native is the provider's own
// request body, used as it is — Block Kit here, a Discord embed or a LINE Flex
// message on another provider.
//
// The trade is deliberate: a message written this way is tied to Slack. There is
// no unified builder over Block Kit, embeds and Flex because those are genuinely
// different models of a message, and a common denominator over them would be
// worse to write against than any of the three.
//
// To and ThreadID are still filled in, so threading keeps working the moment a
// caller reaches for blocks.
func postBlockKit(ctx context.Context, text string) core.IError {
	_, err := core.Chat(ctx).Send(core.ChatMessage{
		To: "#ops",
		Native: map[string]any{
			// text is still worth setting: it is the notification preview and what
			// a client that renders no blocks falls back to
			"text": text,
			"blocks": []any{
				map[string]any{
					"type": "section",
					"text": map[string]any{"type": "mrkdwn", "text": "*" + text + "*"},
				},
				map[string]any{"type": "divider"},
			},
		},
	})
	return err
}

// alertJob is where posting belongs when it is not the point of the request.
// Slack is somebody else's service and it has outages: an order must not fail to
// save because a channel could not be posted to.
//
// A retried job with no guard is a channel with five identical alerts, so the
// job carries an idempotency key.
func registerChatJobs(reg *core.JobRegistry) {
	_ = reg.Register(core.JobDef{
		Name:        "chat.alert",
		Description: "post an alert out of band",
		MaxAttempts: 5,
	}, func(c core.ICronjobContext) error {
		// c is an IContext, so core.Chat(c) binds the post to the run: cancelling
		// the run cancels the HTTP call it is waiting on
		return reportImportFailure(c, "orders.csv", 3, "https://example.com/runs/7")
	})
}

// Testing: NewMemoryChat records messages instead of posting them, and runs the
// same validation a real provider does — so a message Slack would have rejected
// fails the test instead of passing it.
//
//	c := core.NewMemoryChat()
//	app, _ := core.NewApp(env, core.WithChat("default", c))
//
//	require.NoError(t, reportImportFailure(app.NewContext(context.Background()), "orders.csv", 3, url))
//
//	sent := core.SentChatMessages(c)
//	require.Len(t, sent, 1)
//	// assert the alert, not that a method was called
//	assert.Equal(t, core.ChatError, sent[0].Level)
//	core.ResetChatMessages(c)
//
// It is also what dev should use, so nothing escapes into a real channel by
// accident.
func sentChatTitles(c core.IChat) []string {
	sent := core.SentChatMessages(c)
	out := make([]string, 0, len(sent))
	for _, msg := range sent {
		title := msg.Title
		if title == "" {
			title = msg.Text
		}
		if title == "" {
			// a Native payload carries its own text, which this portable view
			// deliberately cannot read
			title = "(native)"
		}
		out = append(out, title)
	}
	return out
}
