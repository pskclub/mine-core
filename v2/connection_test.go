package core

import (
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveDSN_postgresURI(t *testing.T) {
	dsn, driver, err := resolveDSN(&ENVConfig{
		DBConnectionString: "postgres://u:p@dbhost:5432/mydb?sslmode=disable",
	})
	require.NoError(t, err)
	assert.Equal(t, DriverPostgres, driver)
	assert.Equal(t, "postgres://u:p@dbhost:5432/mydb?sslmode=disable", dsn, "postgres URI passes through unchanged")
}

func TestResolveDSN_mysqlURIConverted(t *testing.T) {
	dsn, driver, err := resolveDSN(&ENVConfig{
		DBDriver:           "mysql",
		DBConnectionString: "mysql://u:p@dbhost:3306/mydb",
	})
	require.NoError(t, err)
	assert.Equal(t, DriverMySQL, driver)
	assert.True(t, strings.HasPrefix(dsn, "u:p@tcp(dbhost:3306)/mydb?"), "mysql URI converted to DSN: %s", dsn)
	assert.Contains(t, dsn, "parseTime=True")
	assert.Contains(t, dsn, "charset=utf8mb4")
	assert.Contains(t, dsn, "loc=UTC")
}

func TestResolveDSN_discreteFields(t *testing.T) {
	dsn, driver, err := resolveDSN(&ENVConfig{
		DBDriver: "postgres", DBHost: "h", DBUser: "u", DBPassword: "p",
		DBName: "db", DBPort: "5432",
	})
	require.NoError(t, err)
	assert.Equal(t, DriverPostgres, driver)
	assert.Contains(t, dsn, "host=h")
	assert.Contains(t, dsn, "sslmode=disable")
}

func TestResolveDSN_unknownDriver(t *testing.T) {
	_, _, err := resolveDSN(&ENVConfig{DBDriver: "cassandra"})
	assert.Error(t, err, "unsupported driver without connection string")
}

func TestNewRedisClient_fromURI(t *testing.T) {
	c, err := newRedisClient(&ENVConfig{
		CacheConnectionString: "redis://:secret@cachehost:6380/3",
	})
	require.NoError(t, err)
	rc, ok := c.(*redis.Client)
	require.True(t, ok, "expected *redis.Client")

	opt := rc.Options()
	assert.Equal(t, "cachehost:6380", opt.Addr)
	assert.Equal(t, 3, opt.DB)
	assert.Equal(t, "secret", opt.Password)
}

func TestNewRedisClient_badURI(t *testing.T) {
	_, err := newRedisClient(&ENVConfig{CacheConnectionString: "://not-a-url"})
	assert.Error(t, err)
}

func TestNewRedisClient_discreteFields(t *testing.T) {
	c, err := newRedisClient(&ENVConfig{
		CacheHost: "h", CachePort: "6379", CachePassword: "pw", CacheDB: 1,
	})
	require.NoError(t, err)
	rc := c.(*redis.Client)
	assert.Equal(t, "h:6379", rc.Options().Addr)
	assert.Equal(t, 1, rc.Options().DB)
	assert.Equal(t, "pw", rc.Options().Password)
}

func TestMQURL(t *testing.T) {
	assert.Equal(t, "amqps://u:p@host/vh",
		mqURL(&ENVConfig{MQConnectionString: "amqps://u:p@host/vh"}), "connection string should win")
	assert.Equal(t, "amqp://u:p@h:5672/",
		mqURL(&ENVConfig{MQUser: "u", MQPassword: "p", MQHost: "h", MQPort: "5672"}))
}

func TestResolveDSN_sqlserverDiscreteFields(t *testing.T) {
	dsn, driver, err := resolveDSN(&ENVConfig{
		DBDriver: DriverSQLServer, DBHost: "h", DBPort: "1433",
		DBUser: "sa", DBPassword: "p@ss/word", DBName: "app",
	})
	require.NoError(t, err)
	assert.Equal(t, DriverSQLServer, driver)
	assert.Equal(t, "sqlserver://sa:p%40ss%2Fword@h:1433?database=app", dsn,
		"a password containing @ or / must not split the DSN")
}

func TestResolveDSN_sqlserverURI(t *testing.T) {
	for _, scheme := range []string{"sqlserver", "mssql"} {
		conn := scheme + "://sa:pw@h:1433?database=app"
		_, driver, err := resolveDSN(&ENVConfig{DBConnectionString: conn})
		require.NoError(t, err)
		assert.Equal(t, DriverSQLServer, driver, "scheme %q", scheme)
	}
}

func TestResolveDSN_oracleDiscreteFields(t *testing.T) {
	dsn, driver, err := resolveDSN(&ENVConfig{
		DBDriver: DriverOracle, DBHost: "h", DBPort: "1521",
		DBUser: "u", DBPassword: "pw", DBName: "ORCLPDB1", DBSid: "ORCL",
	})
	require.NoError(t, err)
	assert.Equal(t, DriverOracle, driver)
	assert.True(t, strings.HasPrefix(dsn, "oracle://"), "got %s", dsn)
	assert.Contains(t, dsn, "h:1521")
	assert.Contains(t, dsn, "ORCLPDB1", "DB_NAME is the service name")
	assert.Contains(t, dsn, "sid=ORCL", "DB_SID is passed through separately")
}

// Oracle's default port is not the caller's to remember.
func TestResolveDSN_oracleDefaultsThePort(t *testing.T) {
	dsn, _, err := resolveDSN(&ENVConfig{
		DBDriver: DriverOracle, DBHost: "h", DBUser: "u", DBPassword: "pw", DBName: "ORCLPDB1",
	})
	require.NoError(t, err)
	assert.Contains(t, dsn, "h:1521")
}
