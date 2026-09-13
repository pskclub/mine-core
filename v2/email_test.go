package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMailer_disabledFailsLoudly(t *testing.T) {
	m := NewNoopMailer()

	assert.False(t, m.Enabled())
	err := m.Send(EmailMessage{To: []string{"a@b.com"}, Text: "hi"})
	require.Error(t, err)
	assert.Equal(t, "MAILER_DISABLED", err.GetCode())
	assert.ErrorIs(t, err, ErrMailerDisabled)

	assert.ErrorIs(t, m.SendTemplate(EmailMessage{}, "welcome", nil), ErrMailerDisabled)
	assert.ErrorIs(t, m.Ping(), ErrMailerDisabled)
}

func TestMailer_contextMailerIsNeverNil(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(t.Context())

	require.NotNil(t, Mailer(ctx))
	assert.False(t, Mailer(ctx).Enabled())
	assert.ErrorIs(t, Mailer(ctx).Send(EmailMessage{To: []string{"a@b"}, Text: "x"}), ErrMailerDisabled)
}

// sendWelcome is what a domain service looks like: it takes a context.Context,
// not an IContext, so it does not have to import the framework to send mail.
// Making that possible is the whole reason Mailer is a function.
func sendWelcome(ctx context.Context, to string) IError {
	return Mailer(ctx).Send(EmailMessage{To: []string{to}, Subject: "Welcome", Text: "hi"})
}

func TestMailer_reachableFromAPlainContext(t *testing.T) {
	m := NewMemoryMailer()
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), WithMailer(m))
	require.NoError(t, err)

	// a context that has travelled through code knowing nothing about core still
	// carries the App, so the mailer it finds is the configured one
	var plain context.Context = app.NewContext(t.Context())
	plain = context.WithValue(plain, struct{ k string }{"unrelated"}, 1)

	require.NoError(t, sendWelcome(plain, "ann@example.com"))
	require.Len(t, SentMail(m), 1)
}

// A context from nowhere gets the disabled mailer, not nil — a script or an
// early test must not panic on the call.
func TestMailer_contextWithNoAppIsDisabled(t *testing.T) {
	got := Mailer(context.Background())

	require.NotNil(t, got)
	assert.False(t, got.Enabled())
	assert.ErrorIs(t, sendWelcome(context.Background(), "a@b.com"), ErrMailerDisabled)
}

// A message the SMTP server would reject should fail before it is dialled, with
// an error that says which of the two things is missing.
func TestMailer_rejectsIncompleteMessages(t *testing.T) {
	m := NewMemoryMailer()

	err := m.Send(EmailMessage{Text: "body but nobody to send it to"})
	require.Error(t, err)
	assert.Equal(t, "MAIL_NO_RECIPIENT", err.GetCode())

	err = m.Send(EmailMessage{To: []string{"a@b.com"}})
	require.Error(t, err)
	assert.Equal(t, "MAIL_NO_BODY", err.GetCode())
}

func TestMailer_memoryRecordsWhatWouldHaveBeenSent(t *testing.T) {
	m := NewMemoryMailer()
	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), WithMailer(m))
	require.NoError(t, err)

	ctx := app.NewContext(t.Context())
	require.NoError(t, Mailer(ctx).Send(EmailMessage{
		To:      []string{"a@b.com"},
		Subject: "Reset your password",
		HTML:    "<p>click here</p>",
	}))

	sent := SentMail(m)
	require.Len(t, sent, 1, "a handle from WithContext must record into the same list")
	assert.Equal(t, "Reset your password", sent[0].Subject)
	assert.Contains(t, sent[0].HTML, "click here")

	ResetMail(m)
	assert.Empty(t, SentMail(m))
}

func testTemplates(t *testing.T) *MailTemplates {
	t.Helper()
	tpl, err := NewMailTemplates(fstest.MapFS{
		"mail/welcome.html":  {Data: []byte(`<p>Hi {{.Name}}</p>`)},
		"mail/welcome.txt":   {Data: []byte(`Hi {{.Name}}`)},
		"mail/htmlonly.html": {Data: []byte(`<b>{{.Name}}</b>`)},
		"mail/README.md":     {Data: []byte(`not a template`)},
	}, MailTemplateOptions{Root: "mail"})
	require.NoError(t, err)
	return tpl
}

func TestMailTemplates_rendersBothBodies(t *testing.T) {
	tpl := testTemplates(t)

	html, text, err := tpl.Render("welcome", map[string]string{"Name": "Ann"})
	require.NoError(t, err)
	assert.Equal(t, "<p>Hi Ann</p>", html)
	assert.Equal(t, "Hi Ann", text)

	assert.Equal(t, []string{"htmlonly", "welcome"}, tpl.Names(),
		"a file that is neither .html nor .txt is not a template")
}

func TestMailTemplates_oneBodyIsEnough(t *testing.T) {
	tpl := testTemplates(t)

	html, text, err := tpl.Render("htmlonly", map[string]string{"Name": "Ann"})
	require.NoError(t, err)
	assert.Equal(t, "<b>Ann</b>", html)
	assert.Empty(t, text)
}

func TestMailTemplates_unknownNameIsAnError(t *testing.T) {
	_, _, err := testTemplates(t).Render("nope", nil)
	require.Error(t, err)
	assert.Equal(t, "MAIL_TEMPLATE_NOT_FOUND", err.GetCode())
}

// The HTML body must escape what it interpolates and the text body must not: a
// display name is data in both, but only one of them is markup.
func TestMailTemplates_escapesHTMLButNotText(t *testing.T) {
	tpl := testTemplates(t)

	html, text, err := tpl.Render("welcome", map[string]string{"Name": `<script>x</script>`})
	require.NoError(t, err)
	assert.NotContains(t, html, "<script>", "an HTML body must escape interpolated values")
	assert.Contains(t, html, "&lt;script&gt;")
	assert.Contains(t, text, "<script>", "a plain body must not be mangled by escaping")
}

func TestMailer_sendTemplateFillsTheBodies(t *testing.T) {
	m := NewMemoryMailer(testTemplates(t))

	require.NoError(t, m.SendTemplate(EmailMessage{
		To:      []string{"ann@example.com"},
		Subject: "Welcome",
	}, "welcome", map[string]string{"Name": "Ann"}))

	sent := SentMail(m)
	require.Len(t, sent, 1)
	assert.Equal(t, "<p>Hi Ann</p>", sent[0].HTML)
	assert.Equal(t, "Hi Ann", sent[0].Text)
}

func TestMailer_sendTemplateKeepsABodyTheCallerSet(t *testing.T) {
	m := NewMemoryMailer(testTemplates(t))

	require.NoError(t, m.SendTemplate(EmailMessage{
		To:   []string{"ann@example.com"},
		HTML: "<p>hand written</p>",
	}, "welcome", map[string]string{"Name": "Ann"}))

	sent := SentMail(m)
	require.Len(t, sent, 1)
	assert.Equal(t, "<p>hand written</p>", sent[0].HTML)
	assert.Equal(t, "Hi Ann", sent[0].Text, "the body that was not set is still filled")
}

func TestMailer_templatesFromStrings(t *testing.T) {
	tpl := NewMailTemplateSet(map[string]any{
		"upper": strings.ToUpper,
	})
	require.NoError(t, tpl.Add("otp", `<b>{{upper .Code}}</b>`, `Your code is {{.Code}}`))

	html, text, err := tpl.Render("otp", map[string]string{"Code": "ab12"})
	require.NoError(t, err)
	assert.Equal(t, "<b>AB12</b>", html)
	assert.Equal(t, "Your code is ab12", text)
}

func TestMailer_addressFormatting(t *testing.T) {
	assert.Equal(t, "a@b.com", EmailAddress{Address: "a@b.com"}.String())
	assert.Equal(t, `"Ann Lee" <ann@example.com>`,
		EmailAddress{Name: "Ann Lee", Address: "ann@example.com"}.String())
	assert.Equal(t, `"Lee, Ann" <ann@example.com>`,
		EmailAddress{Name: "Lee, Ann", Address: "ann@example.com"}.String(),
		"a name containing a comma must be quoted or it becomes two recipients")
}

func TestMailer_recipientsMergesBothForms(t *testing.T) {
	msg := EmailMessage{
		To:           []string{"a@b.com"},
		CcAddresses:  []EmailAddress{{Name: "Ann", Address: "ann@b.com"}},
		BccAddresses: []EmailAddress{{Address: "audit@b.com"}},
	}
	assert.Len(t, recipients(msg), 3)
	assert.Equal(t, []string{"a@b.com"}, addressList(msg.To, msg.ToAddresses))
	assert.Equal(t, []string{`"Ann" <ann@b.com>`}, addressList(msg.Cc, msg.CcAddresses))
}

func TestMailer_tlsPolicyAndAuthFromConfig(t *testing.T) {
	_, ok := mailTLSPolicy("")
	assert.False(t, ok, "an unset policy leaves the mandatory default in place")

	p, ok := mailTLSPolicy("opportunistic")
	require.True(t, ok)
	assert.Equal(t, "TLSOpportunistic", p.String())

	// no username means no authentication: an internal relay takes none, and
	// offering one it will refuse breaks the connection
	assert.Equal(t, "NOAUTH", string(mailAuthType(&ENVConfig{}, nil)))
	assert.Equal(t, "PLAIN", string(mailAuthType(&ENVConfig{EmailUsername: "u"}, nil)))
	assert.Equal(t, "LOGIN", string(mailAuthType(&ENVConfig{EmailAuth: "login"}, nil)))
}

func TestMailer_requiresAServer(t *testing.T) {
	_, err := NewMailer(mustEnv(t, map[string]string{"ENV": "test"}))
	require.Error(t, err)
	assert.Equal(t, "INVALID_CONFIG", err.GetCode())
}

func TestMailer_buildsAMessageWithAttachments(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "EMAIL_SERVER": "smtp.example.com", "EMAIL_SENDER": "no-reply@example.com",
		"EMAIL_SENDER_NAME": "Example",
	})
	m, err := NewMailer(env)
	require.NoError(t, err)

	built, berr := m.(*mailer).build(EmailMessage{
		To:      []string{"ann@example.com"},
		Subject: "Invoice",
		HTML:    `<p>see attached <img src="cid:logo.png"></p>`,
		Text:    "see attached",
		Attachments: []Attachment{
			{Name: "invoice.pdf", Content: []byte("%PDF-"), ContentType: "application/pdf"},
		},
		Embeds:   []Attachment{{Name: "logo.png", Content: []byte("\x89PNG")}},
		Headers:  map[string]string{"X-Entity-Ref-ID": "inv-1"},
		Priority: PriorityHigh,
	})
	require.NoError(t, berr)
	require.NotNil(t, built)

	to, rerr := built.GetRecipients()
	require.NoError(t, rerr)
	assert.Equal(t, []string{"<ann@example.com>"}, to)
	assert.Equal(t, "Invoice", built.GetGenHeader("Subject")[0])
	assert.Contains(t, built.GetGenHeader("X-Entity-Ref-ID"), "inv-1")
}

func TestMailer_attachmentNeedsAName(t *testing.T) {
	env := mustEnv(t, map[string]string{
		"ENV": "test", "EMAIL_SERVER": "smtp.example.com", "EMAIL_SENDER": "a@b.com",
	})
	m, err := NewMailer(env)
	require.NoError(t, err)

	_, berr := m.(*mailer).build(EmailMessage{
		To:          []string{"ann@example.com"},
		Text:        "hi",
		Attachments: []Attachment{{Content: []byte("x")}},
	})
	require.Error(t, berr)
	assert.Equal(t, "MAIL_INVALID_ATTACHMENT", berr.GetCode())
}

func TestMailer_noSenderIsAConfigError(t *testing.T) {
	env := mustEnv(t, map[string]string{"ENV": "test", "EMAIL_SERVER": "smtp.example.com"})
	m, err := NewMailer(env)
	require.NoError(t, err)

	_, berr := m.(*mailer).build(EmailMessage{To: []string{"a@b.com"}, Text: "hi"})
	require.Error(t, berr)
	assert.Equal(t, "INVALID_CONFIG", berr.GetCode())
	assert.True(t, errors.Is(berr, berr))
}

// closeRecordingMailer notices when App.Shutdown closes it.
type closeRecordingMailer struct {
	IMailer
	closed *bool
}

func (m closeRecordingMailer) Close() IError {
	*m.closed = true
	return nil
}

func (m closeRecordingMailer) WithContext(context.Context) IMailer { return m }

// A mailer holds an SMTP connection. App.Shutdown closes every other pool it
// owns, and skipping this one leaks a socket per process restart.
func TestMailer_isClosedOnShutdown(t *testing.T) {
	closed := false
	m := closeRecordingMailer{IMailer: NewMemoryMailer(), closed: &closed}

	app, err := NewApp(mustEnv(t, map[string]string{"ENV": "test"}), WithMailer(m))
	require.NoError(t, err)

	require.NoError(t, app.Shutdown(t.Context()))
	assert.True(t, closed, "App.Shutdown must close the mailer")
}
