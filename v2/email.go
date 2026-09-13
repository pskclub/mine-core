package core

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	htmltemplate "html/template"
	"io"
	"io/fs"
	netmail "net/mail"
	"sort"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"

	"github.com/wneessen/go-mail"
)

// ErrMailerDisabled is wrapped by the error every operation returns when no
// EMAIL_* configuration is set.
//
// Mail does not degrade quietly. A password reset that was silently dropped is
// a user locked out, with nothing in the logs to say why.
var ErrMailerDisabled = errors.New("mailer: not configured")

// DefaultMailTimeout bounds one delivery, connection included.
const DefaultMailTimeout = 30 * time.Second

// EmailPriority maps to the Importance/Priority headers a mail client shows.
type EmailPriority uint8

const (
	PriorityNormal EmailPriority = iota
	PriorityLow
	PriorityHigh
)

// EmailAddress is a recipient with a display name.
type EmailAddress struct {
	Name    string
	Address string
}

// String renders the address in the form a header takes, quoting the name when
// it needs quoting.
func (a EmailAddress) String() string {
	if a.Name == "" {
		return a.Address
	}
	return (&netmail.Address{Name: a.Name, Address: a.Address}).String()
}

// Attachment is a file sent with a message. Give it Content or Reader, not both.
type Attachment struct {
	// Name is the filename the recipient sees.
	Name string
	// Content is the file, in memory.
	Content []byte
	// Reader streams the file instead of holding it in memory — an S3 object, a
	// generated report. It is read once, when the message is sent.
	Reader io.Reader
	// ContentType defaults to being guessed from Name.
	ContentType string
	// ContentID makes an inline part referenceable from the HTML body as
	// <img src="cid:the-id">. Put the file in Embeds rather than setting this by
	// hand; the name is used when it is empty.
	ContentID string
}

// EmailMessage describes an outbound email.
type EmailMessage struct {
	// From overrides EMAIL_SENDER for this message; FromName the display name.
	From     string
	FromName string

	To  []string
	Cc  []string
	Bcc []string
	// ToAddresses and its siblings are the named form of the three lists above.
	// Both may be used; they are concatenated.
	ToAddresses  []EmailAddress
	CcAddresses  []EmailAddress
	BccAddresses []EmailAddress

	ReplyTo string
	Subject string

	// HTML and Text are the bodies. Send both when you can: HTML for the client
	// that renders it, text for the one that does not — and for the spam filter,
	// which treats an HTML-only message as a small strike against it.
	HTML string
	Text string

	// Attachments are sent as files; Embeds are inline parts the HTML body
	// references by content id.
	Attachments []Attachment
	Embeds      []Attachment

	// Headers are extra headers (List-Unsubscribe, X-Entity-Ref-ID, ...).
	Headers map[string]string

	Priority EmailPriority
}

// Mailer returns the application's mailer bound to ctx.
//
// It is a function rather than a method on IContext, for the same reason
// core.Requester is: sending an email is not a capability of the request — it is
// something code does, with the request's deadline and trace attached. Taking
// only a context.Context is what lets a service that knows nothing about this
// framework send one:
//
//	func (s *NotificationService) SendWelcome(ctx context.Context, u User) error {
//	    return core.Mailer(ctx).SendTemplate(msg, "welcome", u)
//	}
//
// ctx is anything carrying the App — an IContext from a handler or a job, or any
// context.Context derived from one.
//
// A context from nowhere — context.Background() in a script or an early test —
// gets the disabled mailer rather than nil, so a call site never has to
// nil-check. Every call on it fails with MAILER_DISABLED.
func Mailer(ctx context.Context) IMailer {
	if app := appFrom(ctx); app != nil && app.mailer != nil {
		return app.mailer.WithContext(ctx)
	}
	return noopMailer{}
}

// IMailer sends email.
//
// core.Mailer(ctx) is never nil: a service with no EMAIL_* configuration gets a
// disabled mailer whose every call fails with MAILER_DISABLED.
type IMailer interface {
	// Send delivers msg, bounded by the handle's context.
	Send(msg EmailMessage) IError
	// SendTemplate renders the template registered under name and sends it. It
	// fills HTML from "<name>.html" and Text from "<name>.txt", whichever exist,
	// and leaves a body the caller already set alone.
	SendTemplate(msg EmailMessage, name string, data any) IError
	// Render returns what SendTemplate would send, without sending it — for a
	// preview route, or a test that asserts on the wording.
	Render(name string, data any) (html string, text string, err IError)
	// Ping opens a connection to the SMTP server and closes it, for a readiness
	// probe.
	Ping() IError
	// Enabled reports whether this is a real mailer.
	Enabled() bool
	// WithContext returns a handle bound to a different context.
	WithContext(ctx context.Context) IMailer
	// Close releases whatever the mailer holds. Owned by App.Shutdown.
	Close() IError
}

// MailerOption tunes a mailer at construction.
type MailerOption func(*mailerConfig)

type mailerConfig struct {
	timeout   time.Duration
	tlsPolicy *mail.TLSPolicy
	authType  *mail.SMTPAuthType
	tlsConfig *tls.Config
	helo      string
	templates *MailTemplates
	clientOpt []mail.Option
}

// WithMailTimeout bounds one delivery (default DefaultMailTimeout).
func WithMailTimeout(d time.Duration) MailerOption {
	return func(c *mailerConfig) {
		if d > 0 {
			c.timeout = d
		}
	}
}

// WithMailTLSPolicy overrides EMAIL_TLS_POLICY.
func WithMailTLSPolicy(p mail.TLSPolicy) MailerOption {
	return func(c *mailerConfig) { c.tlsPolicy = &p }
}

// WithMailAuth overrides EMAIL_AUTH.
func WithMailAuth(a mail.SMTPAuthType) MailerOption {
	return func(c *mailerConfig) { c.authType = &a }
}

// WithMailTLSConfig sets the TLS configuration — a private CA, a client
// certificate.
func WithMailTLSConfig(t *tls.Config) MailerOption {
	return func(c *mailerConfig) { c.tlsConfig = t }
}

// WithMailHELO sets the HELO/EHLO name. Some providers refuse a name that does
// not resolve.
func WithMailHELO(name string) MailerOption {
	return func(c *mailerConfig) { c.helo = name }
}

// WithMailTemplates registers the templates SendTemplate renders.
func WithMailTemplates(t *MailTemplates) MailerOption {
	return func(c *mailerConfig) { c.templates = t }
}

// WithMailClientOptions passes go-mail options through, for anything this
// package does not name.
func WithMailClientOptions(opts ...mail.Option) MailerOption {
	return func(c *mailerConfig) { c.clientOpt = append(c.clientOpt, opts...) }
}

type mailer struct {
	ctx       context.Context
	client    *mail.Client
	sender    string
	name      string
	timeout   time.Duration
	templates *MailTemplates
}

var _ IMailer = (*mailer)(nil)

// NewMailer builds an SMTP mailer from configuration.
//
//	EMAIL_SERVER=smtp.example.com
//	EMAIL_PORT=587
//	EMAIL_USERNAME=... EMAIL_PASSWORD=...
//	EMAIL_SENDER=no-reply@example.com
//	EMAIL_SENDER_NAME=Example
//	EMAIL_TLS_POLICY=mandatory   # mandatory (default) | opportunistic | none
//	EMAIL_SSL=false              # implicit TLS, for port 465
//	EMAIL_AUTH=auto              # plain | login | cram-md5 | xoauth2 | none | auto
//	EMAIL_TIMEOUT=30             # seconds
func NewMailer(env IENV, opts ...MailerOption) (IMailer, IError) {
	cfg := env.Config()
	c := &mailerConfig{timeout: DefaultMailTimeout}
	if cfg.EmailTimeout > 0 {
		c.timeout = time.Duration(cfg.EmailTimeout) * time.Second
	}
	for _, o := range opts {
		o(c)
	}

	if cfg.EmailServer == "" {
		return nil, New(500, "INVALID_CONFIG", "mailer: EMAIL_SERVER is required")
	}

	clientOpts := []mail.Option{mail.WithTimeout(c.timeout)}
	if cfg.EmailPort > 0 {
		clientOpts = append(clientOpts, mail.WithPort(cfg.EmailPort))
	}
	if cfg.EmailSSL {
		clientOpts = append(clientOpts, mail.WithSSL())
	}

	policy := mail.TLSMandatory
	if c.tlsPolicy != nil {
		policy = *c.tlsPolicy
	} else if p, ok := mailTLSPolicy(cfg.EmailTLSPolicy); ok {
		policy = p
	}
	clientOpts = append(clientOpts, mail.WithTLSPolicy(policy))

	if c.tlsConfig != nil {
		clientOpts = append(clientOpts, mail.WithTLSConfig(c.tlsConfig))
	} else if cfg.EmailTLSSkipVerify {
		// for a self-signed relay inside a private network; the option exists
		// because those exist, not because skipping verification is a good idea
		clientOpts = append(clientOpts, mail.WithTLSConfig(&tls.Config{
			InsecureSkipVerify: true,
			ServerName:         cfg.EmailServer,
		}))
	}
	if c.helo != "" {
		clientOpts = append(clientOpts, mail.WithHELO(c.helo))
	}

	// authentication last: a relay that takes none must not be handed a
	// username, or an AUTH is negotiated that the server will refuse
	auth := mailAuthType(cfg, c.authType)
	if auth != mail.SMTPAuthNoAuth {
		clientOpts = append(clientOpts,
			mail.WithSMTPAuth(auth),
			mail.WithUsername(cfg.EmailUsername),
			mail.WithPassword(cfg.EmailPassword),
		)
	}
	clientOpts = append(clientOpts, c.clientOpt...)

	client, err := mail.NewClient(cfg.EmailServer, clientOpts...)
	if err != nil {
		return nil, Wrap(err, "mailer: new client")
	}

	return &mailer{
		ctx:       context.Background(),
		client:    client,
		sender:    cfg.EmailSender,
		name:      cfg.EmailSenderName,
		timeout:   c.timeout,
		templates: c.templates,
	}, nil
}

// mailTLSPolicy reads EMAIL_TLS_POLICY. Mandatory is the default: credentials
// travel on this connection, and a relay that cannot do STARTTLS is one to fix
// rather than to accommodate.
func mailTLSPolicy(s string) (mail.TLSPolicy, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "mandatory", "required", "true":
		return mail.TLSMandatory, true
	case "opportunistic", "try":
		return mail.TLSOpportunistic, true
	case "none", "off", "false", "no":
		return mail.NoTLS, true
	default:
		return mail.TLSMandatory, false
	}
}

// mailAuthType reads EMAIL_AUTH, defaulting to PLAIN when a username is set and
// to no authentication when none is.
func mailAuthType(cfg *ENVConfig, override *mail.SMTPAuthType) mail.SMTPAuthType {
	if override != nil {
		return *override
	}
	switch strings.ToLower(strings.TrimSpace(cfg.EmailAuth)) {
	case "plain":
		return mail.SMTPAuthPlain
	case "login":
		return mail.SMTPAuthLogin
	case "cram-md5", "crammd5":
		return mail.SMTPAuthCramMD5
	case "xoauth2":
		return mail.SMTPAuthXOAUTH2
	case "scram-sha-1":
		return mail.SMTPAuthSCRAMSHA1
	case "scram-sha-256":
		return mail.SMTPAuthSCRAMSHA256
	case "none", "noauth":
		return mail.SMTPAuthNoAuth
	case "auto", "":
		if cfg.EmailUsername == "" {
			return mail.SMTPAuthNoAuth
		}
		return mail.SMTPAuthPlain
	default:
		return mail.SMTPAuthPlain
	}
}

func (m *mailer) WithContext(ctx context.Context) IMailer {
	cp := *m
	cp.ctx = ctx
	return &cp
}

func (m *mailer) Enabled() bool { return true }

func (m *mailer) Close() IError {
	if m.client == nil {
		return nil
	}
	// Close on a client that never dialled is not an error worth reporting: the
	// mailer connects per delivery, so a service that sent nothing has nothing
	// to close.
	_ = m.client.Close()
	return nil
}

// Ping opens a connection and closes it, which is the only way to know an SMTP
// server is reachable and willing to talk to these credentials.
func (m *mailer) Ping() IError {
	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()

	if err := m.client.DialWithContext(ctx); err != nil {
		return Wrap(err, "mailer: dial")
	}
	if err := m.client.Close(); err != nil {
		return Wrap(err, "mailer: close")
	}
	return nil
}

func (m *mailer) Render(name string, data any) (string, string, IError) {
	if m.templates == nil {
		return "", "", New(500, "MAIL_NO_TEMPLATES", "mailer: no templates are registered")
	}
	return m.templates.Render(name, data)
}

func (m *mailer) SendTemplate(msg EmailMessage, name string, data any) IError {
	rendered, err := renderInto(msg, m, name, data)
	if err != nil {
		return err
	}
	return m.Send(rendered)
}

func (m *mailer) Send(msg EmailMessage) IError {
	built, err := m.build(msg)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(m.ctx, m.timeout)
	defer cancel()

	if sendErr := m.client.DialAndSendWithContext(ctx, built); sendErr != nil {
		breadcrumbTo(m.ctx, Breadcrumb{
			Type: "error", Category: "mail.send", Level: LevelError,
			Message: msg.Subject,
			Data:    map[string]any{"to": len(recipients(msg)), "error": sendErr.Error()},
		})
		return Wrap(sendErr, "mailer: send")
	}

	breadcrumbTo(m.ctx, Breadcrumb{
		Category: "mail.send",
		Message:  msg.Subject,
		Data:     map[string]any{"to": len(recipients(msg))},
	})
	return nil
}

// renderInto fills the bodies a caller left empty from a template. A body set by
// hand wins: SendTemplate is a convenience, not a rule about where bodies come
// from.
func renderInto(msg EmailMessage, m IMailer, name string, data any) (EmailMessage, IError) {
	html, text, err := m.Render(name, data)
	if err != nil {
		return msg, err
	}
	if msg.HTML == "" {
		msg.HTML = html
	}
	if msg.Text == "" {
		msg.Text = text
	}
	return msg, nil
}

// build turns an EmailMessage into a go-mail message, reporting the first thing
// wrong with it rather than sending half of it.
func (m *mailer) build(msg EmailMessage) (*mail.Msg, IError) {
	if err := validateEmail(msg); err != nil {
		return nil, err
	}

	out := mail.NewMsg()

	from, fromName := msg.From, msg.FromName
	if from == "" {
		from, fromName = m.sender, m.name
	}
	if from == "" {
		return nil, New(500, "INVALID_CONFIG", "mailer: no sender (set EMAIL_SENDER)")
	}
	if fromName != "" {
		if err := out.FromFormat(fromName, from); err != nil {
			return nil, Wrap(err, "mailer: from")
		}
	} else if err := out.From(from); err != nil {
		return nil, Wrap(err, "mailer: from")
	}

	if err := out.To(addressList(msg.To, msg.ToAddresses)...); err != nil {
		return nil, Wrap(err, "mailer: to")
	}
	if cc := addressList(msg.Cc, msg.CcAddresses); len(cc) > 0 {
		if err := out.Cc(cc...); err != nil {
			return nil, Wrap(err, "mailer: cc")
		}
	}
	if bcc := addressList(msg.Bcc, msg.BccAddresses); len(bcc) > 0 {
		if err := out.Bcc(bcc...); err != nil {
			return nil, Wrap(err, "mailer: bcc")
		}
	}
	if msg.ReplyTo != "" {
		if err := out.ReplyTo(msg.ReplyTo); err != nil {
			return nil, Wrap(err, "mailer: reply-to")
		}
	}

	out.Subject(msg.Subject)
	out.SetDate()

	// plain first, HTML as the alternative: a client reads multipart/alternative
	// last part first, so the richest body has to come last
	switch {
	case msg.Text != "" && msg.HTML != "":
		out.SetBodyString(mail.TypeTextPlain, msg.Text)
		out.AddAlternativeString(mail.TypeTextHTML, msg.HTML)
	case msg.HTML != "":
		out.SetBodyString(mail.TypeTextHTML, msg.HTML)
	default:
		out.SetBodyString(mail.TypeTextPlain, msg.Text)
	}

	for _, a := range msg.Embeds {
		if err := attach(out, a, true); err != nil {
			return nil, err
		}
	}
	for _, a := range msg.Attachments {
		if err := attach(out, a, false); err != nil {
			return nil, err
		}
	}

	for k, v := range msg.Headers {
		out.SetGenHeader(mail.Header(k), v)
	}
	switch msg.Priority {
	case PriorityHigh:
		out.SetImportance(mail.ImportanceHigh)
	case PriorityLow:
		out.SetImportance(mail.ImportanceLow)
	}

	return out, nil
}

// validateEmail rejects the two messages an SMTP server would reject anyway,
// with an error that says which — and does it before dialling, so a bug in a
// caller is not diagnosed from a relay's error text.
func validateEmail(msg EmailMessage) IError {
	if len(recipients(msg)) == 0 {
		return New(400, "MAIL_NO_RECIPIENT", "mailer: the message has no recipient")
	}
	if msg.HTML == "" && msg.Text == "" {
		return New(400, "MAIL_NO_BODY", "mailer: the message has no body")
	}
	return nil
}

// attach adds one file, inline or not.
func attach(out *mail.Msg, a Attachment, inline bool) IError {
	if a.Name == "" {
		return New(400, "MAIL_INVALID_ATTACHMENT", "mailer: an attachment needs a name")
	}

	opts := make([]mail.FileOption, 0, 2)
	if a.ContentType != "" {
		opts = append(opts, mail.WithFileContentType(mail.ContentType(a.ContentType)))
	}
	switch {
	case a.ContentID != "":
		opts = append(opts, mail.WithFileContentID(a.ContentID))
	case inline:
		// an inline part with no id cannot be referenced from the HTML, so the
		// filename becomes the id — which is what the HTML would name anyway
		opts = append(opts, mail.WithFileContentID(a.Name))
	}

	reader := a.Reader
	if reader == nil {
		reader = bytes.NewReader(a.Content)
	}

	var err error
	if inline {
		err = out.EmbedReader(a.Name, reader, opts...)
	} else {
		err = out.AttachReader(a.Name, reader, opts...)
	}
	if err != nil {
		return Wrapf(err, "mailer: attach %s", a.Name)
	}
	return nil
}

// addressList merges the plain and the named recipient lists into the header
// form.
func addressList(plain []string, named []EmailAddress) []string {
	out := make([]string, 0, len(plain)+len(named))
	out = append(out, plain...)
	for _, a := range named {
		if a.Address == "" {
			continue
		}
		out = append(out, a.String())
	}
	return out
}

// recipients is every address the message goes to, for the breadcrumb count and
// the empty check. It deliberately does not record the addresses themselves.
func recipients(msg EmailMessage) []string {
	n := make([]string, 0, len(msg.To)+len(msg.Cc)+len(msg.Bcc))
	n = append(n, msg.To...)
	n = append(n, msg.Cc...)
	n = append(n, msg.Bcc...)
	for _, a := range msg.ToAddresses {
		n = append(n, a.Address)
	}
	for _, a := range msg.CcAddresses {
		n = append(n, a.Address)
	}
	for _, a := range msg.BccAddresses {
		n = append(n, a.Address)
	}
	return n
}

// ---------------------------------------------------------------------------
// Templates
// ---------------------------------------------------------------------------

// MailTemplates renders the bodies of an email. It keeps an html/template set
// for HTML bodies and a text/template set for plain ones, so an HTML body
// escapes what it interpolates — a display name containing "<" must not be able
// to rewrite the message around it — and a plain body does not get mangled by
// escaping it does not need.
type MailTemplates struct {
	html *htmltemplate.Template
	text *texttemplate.Template
}

// MailTemplateOptions tunes template parsing.
type MailTemplateOptions struct {
	// Root is a subdirectory of the FS to read from, so an embed.FS rooted at
	// the module can still be addressed by template name alone.
	Root string
	// Funcs are the functions templates may call.
	Funcs map[string]any
}

// NewMailTemplates parses an fs.FS of templates. Files ending in .html (or
// .gohtml) join the HTML set and .txt (or .gotxt) the text set, keyed by the
// path without its extension — so "welcome.html" and "welcome.txt" are the two
// bodies of the template "welcome".
//
//	//go:embed templates/email
//	var emailFS embed.FS
//
//	tpl, err := core.NewMailTemplates(emailFS, core.MailTemplateOptions{Root: "templates/email"})
//	mailer, err := core.NewMailer(env, core.WithMailTemplates(tpl))
//
// Everything parses into one set, so a shared layout or a partial defined in one
// file is usable from every other.
func NewMailTemplates(fsys fs.FS, opts ...MailTemplateOptions) (*MailTemplates, IError) {
	o := MailTemplateOptions{}
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.Root != "" {
		sub, err := fs.Sub(fsys, o.Root)
		if err != nil {
			return nil, Wrapf(err, "mail templates: root %s", o.Root)
		}
		fsys = sub
	}

	t := NewMailTemplateSet(o.Funcs)

	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		name, kind := templateName(path)
		if kind == templateOther {
			return nil
		}
		body, readErr := fs.ReadFile(fsys, path)
		if readErr != nil {
			return readErr
		}
		if kind == templateHTML {
			_, perr := t.html.New(name).Parse(string(body))
			return perr
		}
		_, perr := t.text.New(name).Parse(string(body))
		return perr
	})
	if err != nil {
		return nil, Wrap(err, "mail templates: parse")
	}
	return t, nil
}

// NewMailTemplateSet builds an empty set, for bodies that live in code or come
// from a database rather than from files.
func NewMailTemplateSet(funcs ...map[string]any) *MailTemplates {
	f := map[string]any{}
	if len(funcs) > 0 && funcs[0] != nil {
		f = funcs[0]
	}
	return &MailTemplates{
		html: htmltemplate.New("").Funcs(htmltemplate.FuncMap(f)),
		text: texttemplate.New("").Funcs(texttemplate.FuncMap(f)),
	}
}

// Add registers one template. Either body may be empty.
func (t *MailTemplates) Add(name, html, text string) IError {
	if html != "" {
		if _, err := t.html.New(name).Parse(html); err != nil {
			return Wrapf(err, "mail templates: parse %s (html)", name)
		}
	}
	if text != "" {
		if _, err := t.text.New(name).Parse(text); err != nil {
			return Wrapf(err, "mail templates: parse %s (text)", name)
		}
	}
	return nil
}

// Render executes both bodies of a template. A template with only one body
// returns "" for the other; a name with neither is an error, because a message
// with no body is never what was meant.
func (t *MailTemplates) Render(name string, data any) (string, string, IError) {
	var html, text string

	if tpl := t.html.Lookup(name); tpl != nil {
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return "", "", Wrapf(err, "mail templates: render %s (html)", name)
		}
		html = buf.String()
	}
	if tpl := t.text.Lookup(name); tpl != nil {
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			return "", "", Wrapf(err, "mail templates: render %s (text)", name)
		}
		text = buf.String()
	}

	if html == "" && text == "" {
		return "", "", Newf(500, "MAIL_TEMPLATE_NOT_FOUND", "mailer: no template named %q", name)
	}
	return html, text, nil
}

// Names lists every template that can be rendered, sorted — for a startup check
// that the ones a service sends actually parsed.
func (t *MailTemplates) Names() []string {
	seen := map[string]struct{}{}
	for _, tpl := range t.html.Templates() {
		if tpl.Name() != "" {
			seen[tpl.Name()] = struct{}{}
		}
	}
	for _, tpl := range t.text.Templates() {
		if tpl.Name() != "" {
			seen[tpl.Name()] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

type templateKind uint8

const (
	templateOther templateKind = iota
	templateHTML
	templateText
)

// templateName is the path with its extension removed, so templates in
// different directories may share a base name.
func templateName(path string) (string, templateKind) {
	for _, ext := range []string{".html", ".gohtml"} {
		if strings.HasSuffix(path, ext) {
			return strings.TrimSuffix(path, ext), templateHTML
		}
	}
	for _, ext := range []string{".txt", ".gotxt"} {
		if strings.HasSuffix(path, ext) {
			return strings.TrimSuffix(path, ext), templateText
		}
	}
	return path, templateOther
}

// ---------------------------------------------------------------------------
// Memory mailer (tests)
// ---------------------------------------------------------------------------

// memoryMailer records messages instead of sending them.
type memoryMailer struct {
	ctx       context.Context
	templates *MailTemplates

	// the recorder is shared by pointer, so a handle from WithContext records
	// into the same list the test holds
	rec *mailRecorder
}

type mailRecorder struct {
	mu   sync.Mutex
	sent []EmailMessage
}

var _ IMailer = (*memoryMailer)(nil)

// NewMemoryMailer returns a mailer that records messages instead of delivering
// them, so a test can assert on what a service would have sent:
//
//	m := core.NewMemoryMailer(templates)
//	app, _ := core.NewApp(env, core.WithMailer(m))
//	...
//	sent := core.SentMail(m)
//	require.Len(t, sent, 1)
//	assert.Contains(t, sent[0].HTML, "reset your password")
//
// It runs the same validation the real mailer does, so a message the SMTP server
// would have rejected fails the test rather than passing it.
func NewMemoryMailer(templates ...*MailTemplates) IMailer {
	m := &memoryMailer{ctx: context.Background(), rec: &mailRecorder{}}
	if len(templates) > 0 {
		m.templates = templates[0]
	}
	return m
}

// SentMail returns what a memory mailer has recorded, in order. It returns nil
// for any other mailer.
func SentMail(m IMailer) []EmailMessage {
	mm, ok := m.(*memoryMailer)
	if !ok {
		return nil
	}
	mm.rec.mu.Lock()
	defer mm.rec.mu.Unlock()
	return append([]EmailMessage(nil), mm.rec.sent...)
}

// ResetMail drops everything a memory mailer has recorded.
func ResetMail(m IMailer) {
	if mm, ok := m.(*memoryMailer); ok {
		mm.rec.mu.Lock()
		defer mm.rec.mu.Unlock()
		mm.rec.sent = nil
	}
}

func (m *memoryMailer) Send(msg EmailMessage) IError {
	if err := validateEmail(msg); err != nil {
		return err
	}
	m.rec.mu.Lock()
	defer m.rec.mu.Unlock()
	m.rec.sent = append(m.rec.sent, msg)
	return nil
}

func (m *memoryMailer) SendTemplate(msg EmailMessage, name string, data any) IError {
	rendered, err := renderInto(msg, m, name, data)
	if err != nil {
		return err
	}
	return m.Send(rendered)
}

func (m *memoryMailer) Render(name string, data any) (string, string, IError) {
	if m.templates == nil {
		return "", "", New(500, "MAIL_NO_TEMPLATES", "mailer: no templates are registered")
	}
	return m.templates.Render(name, data)
}

func (m *memoryMailer) Ping() IError  { return nil }
func (m *memoryMailer) Enabled() bool { return true }
func (m *memoryMailer) Close() IError { return nil }

func (m *memoryMailer) WithContext(ctx context.Context) IMailer {
	cp := *m
	cp.ctx = ctx
	return &cp
}

// ---------------------------------------------------------------------------
// Disabled mailer
// ---------------------------------------------------------------------------

type noopMailer struct{}

var _ IMailer = noopMailer{}

// NewNoopMailer returns a mailer that refuses every operation. It is what a
// service with no EMAIL_* configuration gets, so the failure is a clear error
// instead of a nil dereference.
func NewNoopMailer() IMailer { return noopMailer{} }

func (noopMailer) Send(EmailMessage) IError                      { return mailerDisabled() }
func (noopMailer) SendTemplate(EmailMessage, string, any) IError { return mailerDisabled() }
func (noopMailer) Render(string, any) (string, string, IError)   { return "", "", mailerDisabled() }
func (noopMailer) Ping() IError                                  { return mailerDisabled() }
func (noopMailer) Enabled() bool                                 { return false }
func (n noopMailer) WithContext(context.Context) IMailer         { return n }
func (noopMailer) Close() IError                                 { return nil }

func mailerDisabled() *Error {
	return &Error{
		Status:  503,
		Code:    "MAILER_DISABLED",
		Message: "mailer: no SMTP server is configured (set EMAIL_SERVER)",
		cause:   ErrMailerDisabled,
	}
}
