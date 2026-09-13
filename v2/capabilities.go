package core

import "gorm.io/gorm"

// Capability is one dependency this process was wired with, in a shape something
// other than a log line can read.
//
// It answers the question LogCapabilities answers at boot — "does this process
// actually have a cache?" — at any later moment, and with the pool numbers a
// boot line cannot have: a service whose requests are queueing on a full
// connection pool looks identical, from the outside, to one whose database is
// slow.
type Capability struct {
	// Kind is what this is: "sql", "mongo", "cache", "chat", "mq", "storage",
	// "mailer", "pusher", "llm", "embedder", "sentry".
	Kind string `json:"kind"`
	// Name is the connection name for the kinds that have several ("default",
	// "replica"), empty for the ones a process has at most one of.
	Name string `json:"name,omitempty"`
	// Enabled is false for the disabled implementation a missing configuration
	// gives — the whole point of the list, since that one never fails to build.
	Enabled bool `json:"enabled"`
	// Detail identifies what is on the other end, in terms that carry no
	// credentials: a driver, a database name, a model.
	Detail string `json:"detail,omitempty"`
	// Stats is live counters, currently only the SQL pool's.
	Stats map[string]any `json:"stats,omitempty"`
}

// Capabilities lists every capability of this App, ordered by kind and then by
// connection name so two readings can be compared.
//
// Nothing here does I/O — it reports what was wired, not what is reachable. Use
// CheckHealth for the second question.
func (a *App) Capabilities() []Capability {
	if a == nil {
		return nil
	}
	out := make([]Capability, 0, 12)

	for _, name := range connNames(a.dbs) {
		out = append(out, sqlCapability(name, a.dbs[name]))
	}
	for _, name := range connNames(a.mongos) {
		m := a.mongos[name]
		c := Capability{Kind: "mongo", Name: name}
		if m != nil {
			c.Enabled, c.Detail = m.Enabled(), m.Name()
		}
		out = append(out, c)
	}
	for _, name := range connNames(a.caches) {
		ch := a.caches[name]
		c := Capability{Kind: "cache", Name: name}
		if ch != nil {
			c.Enabled = ch.Enabled()
		}
		out = append(out, c)
	}

	for _, name := range connNames(a.chats) {
		ch := a.chats[name]
		c := Capability{Kind: "chat", Name: name}
		if ch != nil {
			c.Enabled, c.Detail = ch.Enabled(), ch.Provider()
		}
		out = append(out, c)
	}

	cfg := &ENVConfig{}
	if a.env != nil {
		cfg = a.env.Config()
	}
	out = append(out,
		Capability{Kind: "mq", Enabled: a.MQ().Enabled()},
		Capability{Kind: "storage", Enabled: a.Storage().Enabled(), Detail: cfg.S3Bucket},
		Capability{Kind: "mailer", Enabled: a.Mailer().Enabled(), Detail: cfg.EmailServer},
		Capability{Kind: "pusher", Enabled: a.Pusher().Enabled()},
		Capability{Kind: "llm", Enabled: a.LLMModel().Enabled(), Detail: modelDetail(a.llm)},
		Capability{Kind: "embedder", Enabled: a.embedder != nil && a.embedder.Enabled(), Detail: embedderDetail(a.embedder)},
		Capability{Kind: "sentry", Enabled: a.Sentry().Enabled(), Detail: cfg.SentryEnvironment},
	)
	return out
}

// sqlCapability reports one connection and the state of its pool.
func sqlCapability(name string, db *gorm.DB) Capability {
	c := Capability{Kind: "sql", Name: name}
	if db == nil {
		return c
	}
	c.Enabled = true
	if db.Dialector != nil {
		c.Detail = db.Dialector.Name()
	}
	// A pool that cannot hand out its underlying *sql.DB is not worth failing
	// over: the connection is still registered, and this is a report.
	sqlDB, err := db.DB()
	if err != nil {
		return c
	}
	s := sqlDB.Stats()
	c.Stats = map[string]any{
		"open":       s.OpenConnections,
		"in_use":     s.InUse,
		"idle":       s.Idle,
		"max_open":   s.MaxOpenConnections,
		"wait_count": s.WaitCount,
		"wait_ms":    s.WaitDuration.Milliseconds(),
	}
	return c
}

func modelDetail(l ILLM) string {
	if l == nil || !l.Enabled() {
		return ""
	}
	if m := l.Model(); m != "" {
		return l.Provider() + "/" + m
	}
	return l.Provider()
}

func embedderDetail(e IEmbedder) string {
	if e == nil || !e.Enabled() {
		return ""
	}
	if m := e.Model(); m != "" {
		return e.Provider() + "/" + m
	}
	return e.Provider()
}
