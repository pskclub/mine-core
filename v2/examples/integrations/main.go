// Command integrations is a runnable tour of the things a service talks to that
// are not its own database: other people's HTTP APIs, SMTP, Firebase, and the
// CSV files humans exchange.
//
// Each example lives in its own file:
//
//	01_requester.go  core.Requester(ctx), SetError, timeouts, safe retries
//	02_mailer.go     messages, templates, attachments, sending from a job
//	03_push.go       FCM: notification vs data, topic vs token, dead tokens
//	04_csv.go        reading and writing, and exporting more rows than fit
//	05_chat.go       Slack: alerts, threads, Block Kit, posting from a job
//
// Run it with: go run ./examples/integrations
//
// It needs no external service at all. The HTTP example calls a stub server this
// process starts on a loopback port; mail, push and chat run on the memory
// implementations that ship with the framework — the same ones the tests use, so
// what you see printed is exactly what a test would assert on.
//
// To use the real ones, configure them and wire the real implementation in:
//
//	EMAIL_SERVER=smtp.example.com EMAIL_SENDER=no-reply@example.com   # core.NewMailer(env)
//	FIREBASE_CREDENTIAL='{"type":"service_account",...}'              # core.NewPusherFromEnv(env)
//	CHAT_SLACK_TOKEN=xoxb-... CHAT_SLACK_CHANNEL=#ops                 # slack.New(env)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	core "github.com/pskclub/mine-core/v2"
)

func main() {
	env, err := core.NewEnv()
	if err != nil {
		panic(err)
	}

	// The memory implementations are how a capability is faked in v2 — there are
	// no generated mocks. Each of these is a real, exported implementation that
	// records instead of delivering.
	mailer := core.NewMemoryMailer(exampleTemplates())
	pusher := core.NewMemoryPusher()
	chat := core.NewMemoryChat()

	app, err := core.NewApp(env,
		core.WithMailer(mailer),
		core.WithPusher(pusher),
		// named, because a service posting to both an internal Slack and a
		// customer-facing LINE has two providers, not one with two destinations
		core.WithChat("default", chat),
	)
	if err != nil {
		panic(err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = app.Shutdown(shutdownCtx)
	}()

	ctx := app.NewContext(context.Background())
	log := ctx.Log()

	// 1. HTTP: a stub upstream, so the example is honest offline
	upstream := stubUpstream()
	defer upstream.Close()

	rate, err := fetchRate(ctx, upstream.URL, "THB/USD")
	if err != nil {
		log.Error("rate lookup failed", "err", err)
		return
	}
	log.Info("fetched a rate", "pair", rate.Pair, "value", rate.Value)

	// the upstream answers 402 with a body carrying more than {code, message};
	// SetError is what turns that body into a decision
	if cErr := charge(ctx, upstream.URL, 2500); cErr != nil {
		log.Info("charge failed as expected", "code", cErr.GetCode(), "status", cErr.GetStatus())
	}

	// 2. mail — nothing leaves the process
	if mErr := sendWelcome(ctx, "user@example.com", "สมชาย"); mErr != nil {
		log.Error("welcome mail failed", "err", mErr)
		return
	}
	log.Info("recorded mail", "subjects", sentSubjects(mailer))

	html, _, rErr := previewWelcome(ctx, "สมชาย")
	if rErr != nil {
		log.Error("render failed", "err", rErr)
		return
	}
	log.Info("rendered without sending", "html_bytes", len(html))

	// 3. push
	if pErr := notifyShipped(ctx, "tok-1", "o-1"); pErr != nil {
		log.Error("push failed", "err", pErr)
		return
	}
	log.Info("recorded push", "titles", sentTitles(pusher))

	// 4. CSV — a streaming export of more rows than we would want in memory,
	//    written here into a discard sink to keep the output readable
	rows := []SalesRow{
		{Day: time.Now(), Name: "สมชาย", Amount: 1250.50, Paid: true},
		{Day: time.Now(), Name: "สมหญิง", Amount: 980, Paid: false},
	}
	file, cErr := exportSmall(rows)
	if cErr != nil {
		log.Error("csv export failed", "err", cErr)
		return
	}
	log.Info("built a csv", "bytes", len(file))

	back, cErr := importAll(bytes.NewReader(file))
	if cErr != nil {
		log.Error("csv import failed", "err", cErr)
		return
	}
	log.Info("read it back", "rows", len(back), "first", back[0].Name)

	// 5. chat — an alert, then a thread, then Block Kit; nothing leaves the
	//    process
	if chErr := reportImportFailure(ctx, "orders.csv", 3, "https://example.com/runs/7"); chErr != nil {
		log.Error("alert failed", "err", chErr)
		return
	}
	if chErr := announceDeploy(ctx, "v2.31.0"); chErr != nil {
		log.Error("announce failed", "err", chErr)
		return
	}
	if chErr := reportProgress(ctx, "nightly-import"); chErr != nil {
		log.Error("progress failed", "err", chErr)
		return
	}
	if chErr := postBlockKit(ctx, "all queues drained"); chErr != nil {
		log.Error("block kit post failed", "err", chErr)
		return
	}
	log.Info("recorded chat", "messages", sentChatTitles(chat))
}

// stubUpstream stands in for somebody else's API so this example runs with no
// network. A real integration test would point at the sandbox the provider
// gives you; this is only here to make `go run .` work anywhere.
func stubUpstream() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/rates", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Rate{Pair: r.URL.Query().Get("pair"), Value: 36.42})
	})
	mux.HandleFunc("/v1/charges", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusPaymentRequired)
		_ = json.NewEncoder(w).Encode(chargeError{
			Code: "CARD_DECLINED", Message: "card declined",
			DeclineCode: "insufficient_funds", Retryable: false,
		})
	})
	return httptest.NewServer(mux)
}
