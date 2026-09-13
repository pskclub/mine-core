package main

import (
	"context"
	"io"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 2: email -------------------------------------------------------
//
// core.Mailer(ctx), for the same reason as core.Requester: sending mail is
// something the code goes and does, not a capability the request has. A
// NotificationService that knows nothing about this framework can still send one,
// because the function takes a plain context.Context.
//
// Mail does not degrade quietly either. With no EMAIL_* configuration every call
// fails with MAILER_DISABLED — because a password reset that was silently
// dropped is a user locked out with nothing in the logs to say why.

// exampleTemplates builds the template set in code. A real service embeds a
// directory instead:
//
//	//go:embed templates/email
//	var emailFS embed.FS
//	tpl, err := core.NewMailTemplates(emailFS, core.MailTemplateOptions{Root: "templates/email"})
//
// where "welcome.html" and "welcome.txt" are the two bodies of one template
// named "welcome". Everything parses into one set, so a layout defined in one
// file is usable from every other.
func exampleTemplates() *core.MailTemplates {
	tpl := core.NewMailTemplateSet(map[string]any{"upper": strings.ToUpper})

	// The HTML body is parsed by html/template and the text body by text/template
	// — deliberately different sets. A display name containing "<" must not be
	// able to rewrite the markup around it, and a plain-text body must not be
	// mangled by escaping it does not need.
	_ = tpl.Add("welcome",
		`<h1>สวัสดี {{.Name}}</h1><p>ขอบคุณที่สมัครใช้งาน</p>`,
		"สวัสดี {{.Name}} — ขอบคุณที่สมัครใช้งาน",
	)
	_ = tpl.Add("export-ready",
		`<p>รายงานของคุณพร้อมแล้ว <a href="{{.URL}}">ดาวน์โหลด</a></p>`,
		"รายงานของคุณพร้อมแล้ว: {{.URL}}",
	)
	return tpl
}

// sendWelcome renders a template and sends it. SendTemplate only fills the
// bodies the caller left empty, so setting HTML by hand still wins.
func sendWelcome(ctx context.Context, to, name string) core.IError {
	return core.Mailer(ctx).SendTemplate(core.EmailMessage{
		// the named form quotes a display name that needs quoting — otherwise a
		// name containing a comma silently becomes two recipients
		ToAddresses: []core.EmailAddress{{Name: name, Address: to}},
		Subject:     "ยินดีต้อนรับ",
		ReplyTo:     "support@example.com",
		Headers:     map[string]string{"List-Unsubscribe": "<https://example.com/u/abc>"},
	}, "welcome", map[string]any{"Name": name})
}

// sendReceipt attaches files two ways. Content holds the bytes; Reader streams
// them, read once at send time — so an S3 object larger than memory can be
// attached without ever being whole in the process.
//
// Embeds are inline parts the HTML references as cid:<name>, which is why an
// empty ContentID falls back to the filename.
func sendReceipt(ctx context.Context, to string, pdf []byte, logo io.Reader) core.IError {
	return core.Mailer(ctx).Send(core.EmailMessage{
		To:      []string{to},
		Subject: "ใบเสร็จของคุณ",
		HTML:    `<p>ใบเสร็จแนบมาแล้ว <img src="cid:logo.png"></p>`,
		// send both bodies when you can: HTML for clients that render it, text
		// for those that do not — and for the spam filter, which counts an
		// HTML-only message as a small strike
		Text: "ใบเสร็จแนบมาแล้ว",
		Attachments: []core.Attachment{
			{Name: "invoice.pdf", Content: pdf, ContentType: "application/pdf"},
		},
		Embeds: []core.Attachment{
			{Name: "logo.png", Reader: logo},
		},
	})
}

// mailJob is where sending belongs. SMTP is slow and it fails, and neither of
// those should be the user's problem: a signup must not fail because the mail
// server is down, and nobody should watch a spinner while a relay thinks.
//
// A retried job with no guard is a user with five identical emails, so the job
// carries an idempotency key — one send per user per event, however many times
// the run is retried or replayed.
func registerMailJobs(reg *core.JobRegistry) {
	_ = reg.Register(core.JobDef{
		Name:        "mail.send-welcome",
		Description: "send the welcome email out of band",
		MaxAttempts: 5,
	}, func(c core.ICronjobContext) error {
		// c is an IContext, so core.Mailer(c) binds the run: cancelling the run
		// cancels the delivery it is waiting on
		return sendWelcome(c, "user@example.com", "สมชาย")
	})
}

// previewWelcome renders without sending — for a preview route, or a test that
// asserts on the wording rather than on the fact that a method was called.
func previewWelcome(ctx context.Context, name string) (string, string, core.IError) {
	return core.Mailer(ctx).Render("welcome", map[string]any{"Name": name})
}

// Testing: NewMemoryMailer records messages instead of delivering them, and runs
// the same validation the real mailer does — so a message an SMTP server would
// have rejected fails the test instead of passing it.
//
//	m := core.NewMemoryMailer(exampleTemplates())
//	app, _ := core.NewApp(env, core.WithMailer(m))
//
//	require.NoError(t, sendWelcome(app.NewContext(context.Background()), "a@b.co", "สมชาย"))
//
//	sent := core.SentMail(m)
//	require.Len(t, sent, 1)
//	// assert the wording, not that a method was called
//	assert.Contains(t, sent[0].HTML, "สวัสดี สมชาย")
//	core.ResetMail(m)
//
// It is also what dev should use, so nothing escapes to a real inbox by accident.
func sentSubjects(m core.IMailer) []string {
	sent := core.SentMail(m)
	out := make([]string, 0, len(sent))
	for _, msg := range sent {
		out = append(out, msg.Subject)
	}
	return out
}
