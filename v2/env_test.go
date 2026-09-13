package core

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewEnvPath_bindsPrefixedEnvVars(t *testing.T) {
	t.Setenv("APP_ENV", "prod")
	t.Setenv("APP_SERVICE", "billing")
	t.Setenv("APP_DB_HOST", "db.internal")
	t.Setenv("APP_EMAIL_PORT", "587")
	t.Setenv("APP_DB_MONGO_TLS", "true")

	e, err := NewEnvPath(t.TempDir()) // no .env file → env vars only
	require.NoError(t, err)

	cfg := e.Config()
	assert.Equal(t, "billing", cfg.Service)
	assert.Equal(t, "db.internal", cfg.DBHost)
	assert.Equal(t, 587, cfg.EmailPort)
	assert.True(t, cfg.DBMongoTLS)
	assert.True(t, e.IsProd())
	assert.False(t, e.IsDev())
}

func TestNewEnvPath_loadsDotEnvFile(t *testing.T) {
	dir := t.TempDir()
	content := "ENV=dev\nSERVICE=from-file\nDB_HOST=file-host\nLOG_SIMPLE=true\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600))

	e, err := NewEnvPath(dir)
	require.NoError(t, err)
	assert.Equal(t, "from-file", e.Config().Service)
	assert.Equal(t, "file-host", e.Config().DBHost)
	assert.True(t, e.Config().LogSimple)
	assert.True(t, e.IsDev())
}

func TestNewEnvPath_envOverridesFile(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("SERVICE=from-file\nDB_HOST=file-host\n"), 0o600))
	t.Setenv("APP_DB_HOST", "env-host")

	e, err := NewEnvPath(dir)
	require.NoError(t, err)
	assert.Equal(t, "env-host", e.Config().DBHost, "env must override file")
	assert.Equal(t, "from-file", e.Config().Service, "untouched by env")
}

func TestEnv_accessors(t *testing.T) {
	t.Setenv("APP_SOME_FLAG", "true")
	t.Setenv("APP_SOME_COUNT", "42")
	t.Setenv("APP_SOME_RATE", "1.5")
	t.Setenv("APP_SOME_TEXT", "hello")

	e, err := NewEnvPath(t.TempDir())
	require.NoError(t, err)
	assert.True(t, e.Bool("SOME_FLAG"))
	assert.Equal(t, 42, e.Int("SOME_COUNT"))
	assert.Equal(t, 1.5, e.Float64("SOME_RATE"))
	assert.Equal(t, "hello", e.String("SOME_TEXT"))
	assert.Contains(t, e.All(), "some_text")
}

func TestNewEnvPath_rejectsInvalidEnv(t *testing.T) {
	t.Setenv("APP_ENV", "staging") // not one of dev|test|mock|prod
	_, err := NewEnvPath(t.TempDir())
	require.Error(t, err)
	var ie *Error
	require.ErrorAs(t, err, &ie)
	assert.Equal(t, "INVALID_CONFIG", ie.GetCode())
}
