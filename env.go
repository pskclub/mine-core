package core

import (
	"fmt"
	"github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"strings"
)

const EnvFileName = ".env"
const EnvTestFileName = "env_test"

type IENV interface {
	Config() *ENVConfig
	IsDev() bool
	IsTest() bool
	IsMock() bool
	IsProd() bool
	Bool(key string) bool
	Int(key string) int
	String(key string) string
	All() map[string]string
}

type ENVConfig struct {
	LogLevel logrus.Level
	LogHost  string `mapstructure:"log_host"`

	Host    string `mapstructure:"host"`
	ENV     string `mapstructure:"env"`
	Service string `mapstructure:"service"`

	SentryDSN string `mapstructure:"sentry_dsn"`

	DBDriver   string `mapstructure:"db_driver"`
	DBHost     string `mapstructure:"db_host"`
	DBName     string `mapstructure:"db_name"`
	DBUser     string `mapstructure:"db_user"`
	DBPassword string `mapstructure:"db_password"`
	DBPort     string `mapstructure:"db_port"`

	DBMongoHost     string `mapstructure:"db_mongo_host"`
	DBMongoName     string `mapstructure:"db_mongo_name"`
	DBMongoUserName string `mapstructure:"db_mongo_username"`
	DBMongoPassword string `mapstructure:"db_mongo_password"`
	DBMongoPort     string `mapstructure:"db_mongo_port"`

	MQHost     string `mapstructure:"mq_host"`
	MQUser     string `mapstructure:"mq_user"`
	MQPassword string `mapstructure:"mq_password"`
	MQPort     string `mapstructure:"mq_port"`

	S3Endpoint  string `mapstructure:"s3_endpoint"`
	S3AccessKey string `mapstructure:"s3_access_key"`
	S3SecretKey string `mapstructure:"s3_secret_key"`
	S3Bucket    string `mapstructure:"s3_bucket"`
	S3Region    string `mapstructure:"s3_region"`
	S3IsHTTPS   bool   `mapstructure:"s3_https"`

	CachePort string `mapstructure:"cache_port"`
	CacheHost string `mapstructure:"cache_host"`

	ELSAddress  string `mapstructure:"els_address"`
	ELSUser     string `mapstructure:"els_user"`
	ELSPassword string `mapstructure:"els_password"`
}

type ENVType struct {
	config *ENVConfig
}

func NewEnv() IENV {
	return NewENVPath(".")
}

func NewENVPath(path string) IENV {
	viper.SetConfigName(EnvFileName)

	viper.SetConfigType("env")
	viper.AddConfigPath(path)
	viper.SetEnvPrefix("APP")
	viper.AutomaticEnv()
	viper.ReadInConfig()
	envKeys := []string{
		"LOG_HOST",
		"HOST", "ENV", "SERVICE",
		"SENTRY_DSN", "DB_DRIVER", "DB_HOST", "DB_HOST",
		"DB_NAME", "DB_USER", "DB_PASSWORD", "DB_PORT", "DB_MONGO_HOST",
		"DB_MONGO_NAME", "DB_MONGO_USERNAME", "DB_MONGO_PASSWORD", "DB_MONGO_PORT",
		"MQ_HOST", "MQ_USER", "MQ_PASSWORD", "MQ_PORT", "S3_ENDPOINT",
		"S3_ACCESS_KEY", "S3_SECRET_KEY", "S3_BUCKET", "S3_HTTPS", "S3_REGION",
		"CACHE_PORT", "CACHE_HOST", "ELS_ADDRESS", "ELS_USER", "ELS_PASSWORD",
	}

	for _, key := range envKeys {
		viper.BindEnv(key)
	}

	env := &ENVConfig{}
	err := viper.Unmarshal(env)
	if err != nil {
		NewLoggerSimple().Debug(err.Error())
	}

	env.LogLevel, _ = logrus.ParseLevel(viper.GetString("log_level"))
	return &ENVType{
		config: env,
	}
}

func (e ENVType) Config() *ENVConfig {
	return e.config
}

// config  is Dev config
func (e ENVType) IsDev() bool {
	return e.String("env") == "dev"
}

func (e ENVType) IsMock() bool {
	return e.String("env") == "mock"
}

// config  is Test config
func (e ENVType) IsTest() bool {
	return e.String("env") == "test"
}

// config  is production config
func (e ENVType) IsProd() bool {
	return e.String("env") == "prod"
}

func (e ENVType) Bool(key string) bool {
	return viper.GetBool(strings.ToLower(key))
}

func (e ENVType) Int(key string) int {
	return viper.GetInt(strings.ToLower(key))
}

func (e ENVType) String(key string) string {
	return viper.GetString(strings.ToLower(key))
}
func (e ENVType) All() map[string]string {
	mapEnvs := make(map[string]string, 0)
	envs := viper.AllSettings()
	for key, value := range envs {
		mapEnvs[key] = fmt.Sprintf("%v", value)
	}

	return mapEnvs
}
