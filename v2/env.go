package core

import (
	"fmt"
	"os"
	"strings"

	"github.com/knadh/koanf/parsers/dotenv"
	kenv "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const (
	envFileName     = ".env"
	envTestFileName = "test.env"
	// envPrefix is stripped from OS env vars, e.g. APP_DB_HOST -> db_host.
	envPrefix = "APP_"
)

// IENV exposes configuration. Same interface as v1 so callers do not relearn,
// but the implementation is a per-instance koanf (no global viper, no manual
// key-bind list).
type IENV interface {
	Config() *ENVConfig
	IsDev() bool
	IsTest() bool
	IsMock() bool
	IsProd() bool
	Bool(key string) bool
	Int(key string) int
	Float64(key string) float64
	String(key string) string
	All() map[string]string
}

// ENVConfig is the strongly-typed configuration. Field names/keys mirror v1;
// adding a key means adding a field here only — the loader binds env vars by
// prefix automatically (v1 needed a second manual envKeys list).
type ENVConfig struct {
	LogHost   string `koanf:"log_host"`
	LogPort   string `koanf:"log_port"`
	LogLevel  string `koanf:"log_level"`
	LogSimple bool   `koanf:"log_simple"`
	// LOG_REQUEST defaults to *true* and so is read through IENV.String — set it
	// to false to drop the per-request access line (see HTTPOptions).
	// LOG_SOURCE is the same: it defaults to *true* and adds the file:line that
	// wrote each line (see wantSource).

	// HTTPLogLevel overrides how much of the *outgoing* HTTP traffic is logged,
	// independently of LOG_LEVEL: "silent" (nothing), "error", "warn" (slow calls
	// and failures — the default), "info"/"debug" (every call).
	HTTPLogLevel string `koanf:"http_log_level"`
	// HTTPLogBody adds the request and response bodies to those lines, scrubbed
	// and truncated. Off by default: a body carries credentials and personal
	// data, and is the easiest way to turn a log store into a place secrets live.
	HTTPLogBody bool `koanf:"http_log_body"`

	Host    string `koanf:"host"`
	ENV     string `koanf:"env"`
	Service string `koanf:"service"`

	// Sentry. Only SentryDSN is required — set it and errors, panics, job
	// failures and breadcrumbs are reported with no further wiring. Everything
	// else tunes what is sent; see docs/sentry.md.
	SentryDSN                string  `koanf:"sentry_dsn"`
	SentryEnvironment        string  `koanf:"sentry_environment"`
	SentryRelease            string  `koanf:"sentry_release"`
	SentryServerName         string  `koanf:"sentry_server_name"`
	SentryDebug              bool    `koanf:"sentry_debug"`
	SentrySampleRate         float64 `koanf:"sentry_sample_rate"`
	SentryTracesSampleRate   float64 `koanf:"sentry_traces_sample_rate"`
	SentrySendDefaultPII     bool    `koanf:"sentry_send_default_pii"`
	SentryIgnoreErrors       string  `koanf:"sentry_ignore_errors"`
	SentryIgnoreTransactions string  `koanf:"sentry_ignore_transactions"`
	SentryMaxBreadcrumbs     int     `koanf:"sentry_max_breadcrumbs"`
	SentryBreadcrumbLevel    string  `koanf:"sentry_breadcrumb_level"`
	SentryMinStatus          int     `koanf:"sentry_min_status"`
	SentryCaptureRetries     bool    `koanf:"sentry_capture_retries"`
	SentryEnableTracing      bool    `koanf:"sentry_enable_tracing"`
	SentryEnableCrons        bool    `koanf:"sentry_enable_crons"`
	SentryEnableLogs         bool    `koanf:"sentry_enable_logs"`
	SentryLogLevel           string  `koanf:"sentry_log_level"`
	SentryEnableMetrics      bool    `koanf:"sentry_enable_metrics"`
	SentryMaxBodyBytes       int     `koanf:"sentry_max_body_bytes"`
	SentryFlushTimeout       int     `koanf:"sentry_flush_timeout"`
	// Three more keys default to *true* and so are read through IENV.String (an
	// unset bool field cannot be told apart from an explicit false):
	// APP_SENTRY_CAPTURE_BODY, APP_SENTRY_SEND_ENV and
	// APP_SENTRY_ATTACH_STACKTRACE.

	JWTSecret string `koanf:"jwt_secret"`

	DBDriver           string `koanf:"db_driver"`
	DBConnectionString string `koanf:"db_connection_string"`
	DBHost             string `koanf:"db_host"`
	DBName             string `koanf:"db_name"`
	DBSid              string `koanf:"db_sid"`
	DBUser             string `koanf:"db_user"`
	DBPassword         string `koanf:"db_password"`
	DBPort             string `koanf:"db_port"`
	DBSSLMode          string `koanf:"db_sslmode"`
	// DBLogLevel overrides how much SQL is logged, independently of LOG_LEVEL:
	// "silent" (nothing at all), "error", "warn" (slow queries and failures —
	// the default), "info" (every statement).
	DBLogLevel string `koanf:"db_log_level"`

	// Mongo. DB_MONGO_NAME is always required — it names the database, which is
	// not taken from the connection string. DB_MONGO_HOST may be a
	// comma-separated list for a replica set.
	DBMongoHost             string `koanf:"db_mongo_host"`
	DBMongoName             string `koanf:"db_mongo_name"`
	DBMongoUserName         string `koanf:"db_mongo_username"`
	DBMongoPassword         string `koanf:"db_mongo_password"`
	DBMongoPort             string `koanf:"db_mongo_port"`
	DBMongoReplicaName      string `koanf:"db_mongo_replica_name"`
	DBMongoTLS              bool   `koanf:"db_mongo_tls"`
	DBMongoConnectionString string `koanf:"db_mongo_connection_string"`
	DBMongoMaxPoolSize      int    `koanf:"db_mongo_max_pool_size"`
	DBMongoMinPoolSize      int    `koanf:"db_mongo_min_pool_size"`
	// DBMongoTimeout is the driver's deadline for one operation, in seconds. It
	// is what stops a query started from a context with no deadline — a cron
	// run, a consumer — from hanging forever.
	DBMongoTimeout int `koanf:"db_mongo_timeout"`
	// DBMongoLogLevel overrides how much of the traffic to Mongo is logged,
	// independently of LOG_LEVEL: "silent" (nothing at all), "error", "warn"
	// (slow commands and failures — the default), "info"/"debug" (every command).
	// It is the Mongo counterpart of DB_LOG_LEVEL.
	DBMongoLogLevel string `koanf:"db_mongo_log_level"`

	MQHost             string `koanf:"mq_host"`
	MQUser             string `koanf:"mq_user"`
	MQPassword         string `koanf:"mq_password"`
	MQPort             string `koanf:"mq_port"`
	MQConnectionString string `koanf:"mq_connection_string"`

	// Cache (redis). One of CACHE_CONNECTION_STRING, CACHE_ADDRS or
	// CACHE_HOST/CACHE_PORT is enough; the rest tunes the pool. CACHE_MASTER_NAME
	// turns the address list into a sentinel list, several CACHE_ADDRS into a
	// cluster — the deployment shape is configuration, not code.
	CacheHost             string `koanf:"cache_host"`
	CachePort             string `koanf:"cache_port"`
	CacheUsername         string `koanf:"cache_username"`
	CachePassword         string `koanf:"cache_password"`
	CacheDB               int    `koanf:"cache_db"`
	CacheConnectionString string `koanf:"cache_connection_string"`
	// CacheAddrs is a comma-separated host:port list (cluster, or sentinels
	// together with CacheMasterName).
	CacheAddrs            string `koanf:"cache_addrs"`
	CacheMasterName       string `koanf:"cache_master_name"`
	CacheSentinelPassword string `koanf:"cache_sentinel_password"`
	// CachePrefix namespaces every key and channel. Set it per service (and per
	// environment) when instances share a redis, so a staging deploy cannot read
	// — or invalidate — production's keys.
	CachePrefix        string `koanf:"cache_prefix"`
	CacheTLS           bool   `koanf:"cache_tls"`
	CacheTLSSkipVerify bool   `koanf:"cache_tls_skip_verify"`
	CachePoolSize      int    `koanf:"cache_pool_size"`
	CacheMinIdleConns  int    `koanf:"cache_min_idle_conns"`
	// The three timeouts are in seconds; 0 keeps the driver's defaults (5s dial,
	// 3s read/write). CacheMaxRetries is -1 to disable retrying.
	CacheDialTimeout  int `koanf:"cache_dial_timeout"`
	CacheReadTimeout  int `koanf:"cache_read_timeout"`
	CacheWriteTimeout int `koanf:"cache_write_timeout"`
	CacheMaxRetries   int `koanf:"cache_max_retries"`

	// Object storage. S3_BUCKET is the only required key: with no access
	// key/secret pair the AWS default credential chain is used, so a deployment
	// with an instance role sets neither.
	S3Endpoint  string `koanf:"s3_endpoint"`
	S3AccessKey string `koanf:"s3_access_key"`
	S3SecretKey string `koanf:"s3_secret_key"`
	S3Bucket    string `koanf:"s3_bucket"`
	S3Region    string `koanf:"s3_region"`
	// S3IsHTTPS picks the scheme when S3_ENDPOINT was written without one
	// ("minio:9000"), as it usually is in a compose file.
	S3IsHTTPS        bool `koanf:"s3_https"`
	S3ForcePathStyle bool `koanf:"s3_force_path_style"`
	// S3Prefix namespaces every key — set it per service (and per environment)
	// when several share a bucket.
	S3Prefix string `koanf:"s3_prefix"`
	// S3PublicURL is the base address PublicURL builds on: a CDN in front of the
	// bucket, rather than the bucket itself.
	S3PublicURL string `koanf:"s3_public_url"`

	// Mail. EMAIL_SERVER is the only required key; with no username the mailer
	// authenticates with nothing, which is what an internal relay expects.
	EmailServer   string `koanf:"email_server"`
	EmailPort     int    `koanf:"email_port"`
	EmailUsername string `koanf:"email_username"`
	EmailPassword string `koanf:"email_password"`
	EmailSender   string `koanf:"email_sender"`
	// EmailSenderName is the display name every message is sent under, unless a
	// message sets its own.
	EmailSenderName string `koanf:"email_sender_name"`
	// EmailTLSPolicy is "mandatory" (the default), "opportunistic" or "none".
	// Credentials travel on this connection, so the default refuses to send at
	// all over a link the server will not encrypt.
	EmailTLSPolicy string `koanf:"email_tls_policy"`
	// EmailSSL is implicit TLS — the whole connection, from the first byte. That
	// is port 465; port 587 uses STARTTLS, which is the TLS policy above.
	EmailSSL bool `koanf:"email_ssl"`
	// EmailTLSSkipVerify accepts a certificate that does not verify. Only for a
	// self-signed relay inside a private network.
	EmailTLSSkipVerify bool `koanf:"email_tls_skip_verify"`
	// EmailAuth is "plain", "login", "cram-md5", "xoauth2", "scram-sha-1",
	// "scram-sha-256", "none", or "auto" (the default: plain when a username is
	// set, none when it is not).
	EmailAuth string `koanf:"email_auth"`
	// EmailTimeout bounds one delivery, in seconds (default 30).
	EmailTimeout int `koanf:"email_timeout"`

	FirebaseCredential string `koanf:"firebase_credential"`

	// Chat. CHAT_SLACK_TOKEN is a bot token (xoxb-…); with none the Slack
	// provider is disabled and every post fails with CHAT_DISABLED, which is
	// deliberate — an alert nobody received is worse than a service that says it
	// cannot send one.
	ChatSlackToken string `koanf:"chat_slack_token"`
	// ChatSlackChannel is where a message with no destination of its own goes —
	// "#alerts", or a channel id. A service that only ever posts to one place
	// sets it and never fills in ChatMessage.To.
	ChatSlackChannel string `koanf:"chat_slack_channel"`
	// ChatSlackTimeout bounds one post, in seconds (default 10).
	ChatSlackTimeout int `koanf:"chat_slack_timeout"`

	// Language model. AI_PROVIDER and AI_MODEL are the two that matter: with
	// either missing the model is disabled and every generation fails with
	// LLM_DISABLED, which is deliberate — an empty answer nobody noticed is
	// worse than a service that says it has no model.
	AIProvider string `koanf:"ai_provider"`
	AIModel    string `koanf:"ai_model"`
	AIAPIKey   string `koanf:"ai_api_key"`
	// AIBaseURL points a provider at another endpoint — a gateway, a proxy, or
	// any OpenAI-compatible service. Empty uses the provider's own.
	AIBaseURL string `koanf:"ai_base_url"`
	// AIMaxTokens caps a reply when the request does not (default 4096).
	AIMaxTokens int `koanf:"ai_max_tokens"`
	// AITimeout bounds one generation, in seconds (default 120). Generations
	// run far longer than an ordinary HTTP call — a reasoning model on a hard
	// prompt takes minutes — so the requester's timeout would cut them off.
	AITimeout int `koanf:"ai_timeout"`
	// AIMaxRetries is how many times a retryable failure (429, 5xx, network) is
	// retried (default 2).
	AIMaxRetries int `koanf:"ai_max_retries"`
	// AIEmbedModel is the embedding model. It is a separate key from AIModel
	// because embedding is a different model — and frequently a different
	// vendor, since the provider a service generates with may not offer
	// embeddings at all.
	AIEmbedModel string `koanf:"ai_embed_model"`
	// AIEmbedProvider overrides AI_PROVIDER for embeddings only. Empty reuses
	// it, which is right until the generation provider has no embedding API.
	AIEmbedProvider string `koanf:"ai_embed_provider"`
	// AIEmbedAPIKey and AIEmbedBaseURL likewise default to the generation ones.
	AIEmbedAPIKey  string `koanf:"ai_embed_api_key"`
	AIEmbedBaseURL string `koanf:"ai_embed_base_url"`
	// AIEmbedDimensions is the vector length to ask the model for. 0 takes the
	// model's own default, which is only right when the vector column was
	// declared with that number.
	//
	// It is configuration rather than a per-call argument because it belongs to
	// the index: gemini-embedding-001 returns 3072 unless asked otherwise, and a
	// single call that forgot to ask writes rows a vector(768) column rejects —
	// or, worse, vectors the rest of the corpus cannot be compared against.
	AIEmbedDimensions int `koanf:"ai_embed_dimensions"`

	// AILogLevel overrides how much of the model traffic is logged,
	// independently of LOG_LEVEL: "silent" (nothing), "error", "warn" (slow
	// calls and failures — the default), "info"/"debug" (every call). It is the
	// model counterpart of DB_LOG_LEVEL and HTTP_LOG_LEVEL.
	AILogLevel string `koanf:"ai_log_level"`
	// AILogPrompt writes the system prompt, the messages and the arguments the
	// model passed to a tool into those lines — everything going *to* the model.
	//
	// Off by default and meant for a development machine: a prompt carries
	// whatever the user typed, and a log store is the easiest place for that to
	// end up somewhere nobody meant it to be. Same reasoning as HTTP_LOG_BODY.
	AILogPrompt bool `koanf:"ai_log_prompt"`
	// AILogCompletion writes what came *back*: the model's reply, and what each
	// tool returned to it.
	//
	// It is a separate key from AI_LOG_PROMPT rather than the same one because
	// the two expose different things. The prompt is what the user typed; the
	// completion is what the model said and what a tool read out of the
	// database to tell it — so a service may reasonably want one and not the
	// other, and turning on prompt logging must not start writing query results
	// nobody asked for.
	//
	// Off by default, for the same reason.
	AILogCompletion bool `koanf:"ai_log_completion"`
	// AILogSlow is when a generation is slow enough to warn about, in seconds
	// (default 30). Tens of seconds rather than the hundreds of milliseconds an
	// HTTP call is judged by, because a reasoning model genuinely takes that
	// long — warning sooner would mark every normal call as slow.
	AILogSlow int `koanf:"ai_log_slow"`
}

type env struct {
	k      *koanf.Koanf
	config *ENVConfig
}

// NewEnv loads configuration from the current directory (.env, or test.env when
// APP_ENV=test) plus APP_-prefixed environment variables.
func NewEnv() (IENV, IError) {
	return NewEnvPath(".")
}

// NewEnvPath loads configuration from a specific directory.
func NewEnvPath(path string) (IENV, IError) {
	k := koanf.New(".")

	fileName := envFileName
	if os.Getenv("APP_ENV") == "test" {
		fileName = envTestFileName
	}
	fullPath := strings.TrimRight(path, "/") + "/" + fileName

	// 1) .env file (optional) — keys lower-cased so they line up with env vars.
	if _, err := os.Stat(fullPath); err == nil {
		if err := k.Load(file.Provider(fullPath), dotenv.ParserEnv("", ".", strings.ToLower)); err != nil {
			return nil, Wrapf(err, "env: load %s", fullPath)
		}
	}

	// 2) APP_-prefixed OS env vars override the file. No manual key list needed.
	envProvider := kenv.Provider(".", kenv.Opt{
		Prefix: envPrefix,
		TransformFunc: func(key, value string) (string, any) {
			return strings.ToLower(strings.TrimPrefix(key, envPrefix)), value
		},
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, Wrap(err, "env: load os environment")
	}

	cfg := &ENVConfig{}
	if err := k.Unmarshal("", cfg); err != nil {
		return nil, Wrap(err, "env: unmarshal")
	}

	e := &env{k: k, config: cfg}
	if err := e.validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// validate fails fast on required/invalid values instead of surfacing at runtime.
func (e *env) validate() IError {
	valid := map[string]bool{"dev": true, "test": true, "mock": true, "prod": true}
	if e.config.ENV != "" && !valid[e.config.ENV] {
		return Newf(400, "INVALID_CONFIG", "env: APP_ENV %q must be one of dev|test|mock|prod", e.config.ENV)
	}
	return nil
}

func (e *env) Config() *ENVConfig { return e.config }

func (e *env) IsDev() bool  { return e.config.ENV == "dev" }
func (e *env) IsTest() bool { return e.config.ENV == "test" }
func (e *env) IsMock() bool { return e.config.ENV == "mock" }
func (e *env) IsProd() bool { return e.config.ENV == "prod" }

func (e *env) Bool(key string) bool       { return e.k.Bool(strings.ToLower(key)) }
func (e *env) Int(key string) int         { return e.k.Int(strings.ToLower(key)) }
func (e *env) Float64(key string) float64 { return e.k.Float64(strings.ToLower(key)) }
func (e *env) String(key string) string   { return e.k.String(strings.ToLower(key)) }

func (e *env) All() map[string]string {
	out := make(map[string]string)
	for key, value := range e.k.All() {
		out[key] = fmt.Sprintf("%v", value)
	}
	return out
}
