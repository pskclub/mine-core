package core

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	oracle "github.com/godoes/gorm-oracle"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlserver"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// SQL driver identifiers.
const (
	DriverPostgres  = "postgres"
	DriverMySQL     = "mysql"
	DriverSQLServer = "sqlserver"
	DriverOracle    = "oracle"
)

// DBOption configures a database connection.
type DBOption func(*dbOptions)

type dbOptions struct {
	maxOpen      int
	maxIdle      int
	connLifetime time.Duration
	logLevel     gormlogger.LogLevel
	logLevelSet  bool
	slowQuery    time.Duration
	log          ILogger
}

// WithMaxOpenConns sets the max open connections.
func WithMaxOpenConns(n int) DBOption { return func(o *dbOptions) { o.maxOpen = n } }

// WithMaxIdleConns sets the max idle connections.
func WithMaxIdleConns(n int) DBOption { return func(o *dbOptions) { o.maxIdle = n } }

// WithConnMaxLifetime sets the max connection lifetime.
func WithConnMaxLifetime(d time.Duration) DBOption { return func(o *dbOptions) { o.connLifetime = d } }

// WithDBLogLevel overrides how much SQL is logged, independently of LOG_LEVEL:
// gormlogger.Info logs every statement, Warn only slow ones and errors, Silent
// nothing.
//
//	db, _ := core.NewDatabase(env, core.WithDBLogLevel(gormlogger.Info))
func WithDBLogLevel(level gormlogger.LogLevel) DBOption {
	return func(o *dbOptions) { o.logLevel = level; o.logLevelSet = true }
}

// WithSlowQuery sets the duration above which a statement is logged as slow
// (default 200ms).
func WithSlowQuery(d time.Duration) DBOption { return func(o *dbOptions) { o.slowQuery = d } }

// WithDBLogger sends SQL to a specific logger instead of one built from config.
func WithDBLogger(l ILogger) DBOption { return func(o *dbOptions) { o.log = l } }

// NewDatabase opens a GORM connection from configuration. The driver is taken
// from DB_DRIVER, or detected from the connection string's scheme.
func NewDatabase(env IENV, opts ...DBOption) (*gorm.DB, IError) {
	o := &dbOptions{maxOpen: 20, maxIdle: 5, connLifetime: time.Hour, logLevel: gormlogger.Warn}
	for _, opt := range opts {
		opt(o)
	}
	if !o.logLevelSet {
		// LOG_LEVEL=debug means "show me everything", and that has to include the
		// SQL — a database logger nobody can reach is a database nobody can debug.
		o.logLevel = gormLogLevel(env)
	}
	if o.log == nil {
		o.log = NewLogger(env)
	}

	cfg := env.Config()
	dsn, driver, err := resolveDSN(cfg)
	if err != nil {
		return nil, err
	}

	var dial gorm.Dialector
	switch driver {
	case DriverPostgres:
		dial = postgres.Open(dsn)
	case DriverMySQL:
		dial = mysql.Open(dsn)
	case DriverSQLServer:
		dial = sqlserver.Open(dsn)
	case DriverOracle:
		dial = oracle.Open(dsn)
	default:
		return nil, Newf(500, "INVALID_CONFIG", "database: unsupported driver %q", driver)
	}

	db, openErr := gorm.Open(dial, &gorm.Config{
		Logger:  NewGormLogger(o.log, o.logLevel, o.slowQuery),
		NowFunc: func() time.Time { return time.Now().UTC() },
	})
	if openErr != nil {
		return nil, Wrap(openErr, "database: open")
	}

	sqlDB, handleErr := db.DB()
	if handleErr != nil {
		return nil, Wrap(handleErr, "database: sql handle")
	}
	sqlDB.SetMaxOpenConns(o.maxOpen)
	sqlDB.SetMaxIdleConns(o.maxIdle)
	sqlDB.SetConnMaxLifetime(o.connLifetime)

	return db, nil
}

// resolveDSN returns the DSN and driver, preferring an explicit connection
// string (driver detected from its scheme) over discrete DB_* fields.
func resolveDSN(cfg *ENVConfig) (dsn string, driver string, err IError) {
	if cfg.DBConnectionString != "" {
		driver = cfg.DBDriver
		if driver == "" {
			driver = detectDriver(cfg.DBConnectionString)
		}
		dsn = cfg.DBConnectionString
		// gorm's MySQL driver needs the DSN form, not a mysql:// URL — convert.
		if driver == DriverMySQL && strings.Contains(dsn, "://") {
			converted, cerr := mysqlURLToDSN(dsn)
			if cerr != nil {
				return "", "", cerr
			}
			dsn = converted
		}
		return dsn, driver, nil
	}

	driver = cfg.DBDriver
	switch driver {
	case DriverPostgres:
		sslmode := cfg.DBSSLMode
		if sslmode == "" {
			sslmode = "disable"
		}
		dsn = fmt.Sprintf("host=%s user=%s password=%s dbname=%s port=%s sslmode=%s",
			cfg.DBHost, cfg.DBUser, cfg.DBPassword, cfg.DBName, cfg.DBPort, sslmode)
	case DriverMySQL:
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&parseTime=True&loc=UTC",
			cfg.DBUser, cfg.DBPassword, cfg.DBHost, cfg.DBPort, cfg.DBName)
	case DriverSQLServer:
		// the password goes through the userinfo of a URL, so one containing a
		// "@" or a "/" — which a generated password often does — does not split
		// the DSN in the wrong place
		u := url.URL{
			Scheme: "sqlserver",
			User:   url.UserPassword(cfg.DBUser, cfg.DBPassword),
			Host:   net.JoinHostPort(cfg.DBHost, cfg.DBPort),
		}
		q := url.Values{}
		if cfg.DBName != "" {
			q.Set("database", cfg.DBName)
		}
		u.RawQuery = q.Encode()
		dsn = u.String()
	case DriverOracle:
		port, _ := strconv.Atoi(cfg.DBPort)
		if port == 0 {
			port = 1521
		}
		// Oracle names a *service* or a SID, not a database: DB_NAME is the
		// service name, DB_SID the SID. BuildUrl escapes both and the password.
		opts := map[string]string{}
		if cfg.DBSid != "" {
			opts["sid"] = cfg.DBSid
		}
		dsn = oracle.BuildUrl(cfg.DBHost, port, cfg.DBName, cfg.DBUser, cfg.DBPassword, opts)
	default:
		return "", "", Newf(500, "INVALID_CONFIG", "database: unknown driver %q (set DB_DRIVER)", driver)
	}
	return dsn, driver, nil
}

// mysqlURLToDSN converts "mysql://user:pass@host:port/db?params" into the Go
// MySQL DSN "user:pass@tcp(host:port)/db?params" that gorm's driver expects.
// Sensible defaults (parseTime, utf8mb4, UTC) are added when absent.
func mysqlURLToDSN(raw string) (string, IError) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", Wrap(err, "database: parse mysql url")
	}
	user := u.User.Username()
	pass, _ := u.User.Password()
	dbName := strings.TrimPrefix(u.Path, "/")

	q := u.Query()
	if q.Get("parseTime") == "" {
		q.Set("parseTime", "True")
	}
	if q.Get("charset") == "" {
		q.Set("charset", "utf8mb4")
	}
	if q.Get("loc") == "" {
		q.Set("loc", "UTC")
	}

	dsn := fmt.Sprintf("%s:%s@tcp(%s)/%s?%s", user, pass, u.Host, dbName, q.Encode())
	return dsn, nil
}

// detectDriver reads the scheme from a connection string (e.g. "postgres://...").
func detectDriver(conn string) string {
	if u, err := url.Parse(conn); err == nil {
		switch u.Scheme {
		case "postgres", "postgresql":
			return DriverPostgres
		case "mysql":
			return DriverMySQL
		case "sqlserver", "mssql":
			return DriverSQLServer
		case "oracle":
			return DriverOracle
		}
	}
	return ""
}
