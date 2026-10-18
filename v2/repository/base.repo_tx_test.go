package repository_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/repository"
)

type Order struct {
	ID     uint `gorm:"primarykey"`
	UserID uint
	Total  int
}

func (Order) TableName() string { return "orders" }

type Account struct {
	ID    uint   `gorm:"primarykey"`
	Email string `gorm:"uniqueIndex"`
}

func (Account) TableName() string { return "accounts" }

// newCtxWith opens a database the way core.NewDatabase does — driver errors
// translated — with the given models migrated.
func newCtxWith(t *testing.T, models ...any) core.IContext {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{TranslateError: true})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(models...))

	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, err := core.NewApp(env, core.WithSQL("default", db))
	require.NoError(t, err)
	return app.NewContext(context.Background())
}

// The handle fn receives must not carry the repository's Model: a tx that still
// meant "users" would write an Order through the users schema and panic inside
// GORM. The documented use — repositories for other tables built on tx — depends
// on it starting clean.
func TestRepo_transactionHandsOutACleanHandle(t *testing.T) {
	ctx := newCtxWith(t, &User{}, &Order{})
	users := repository.New[User](ctx).Where("status = ?", "active")

	err := users.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&Order{UserID: 1, Total: 10}).Error; err != nil {
			return err
		}
		return repository.NewWithDB[Order](ctx, tx).Create(&Order{UserID: 2, Total: 20})
	})
	require.Nil(t, err)

	n, cerr := repository.New[Order](ctx).Count()
	require.Nil(t, cerr)
	assert.Equal(t, int64(2), n, "both writes went to orders, and were committed")
}

// A business rule that fails inside the transaction is the caller's answer.
// Wrapping it as DATABASE_ERROR told the client a server fault had occurred
// and hid the code the frontend branches on.
func TestRepo_transactionReturnsTheCallbacksIErrorUnchanged(t *testing.T) {
	ctx := newCtxWith(t, &User{}, &Order{})

	err := repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
		return core.New(http.StatusBadRequest, "INSUFFICIENT_CREDITS", "not enough credits")
	})
	require.NotNil(t, err)
	assert.Equal(t, http.StatusBadRequest, err.GetStatus())
	assert.Equal(t, "INSUFFICIENT_CREDITS", err.GetCode())

	// a plain error is still the database layer's to classify
	plain := repository.New[User](ctx).Transaction(func(tx *gorm.DB) error {
		return errors.New("boom")
	})
	require.NotNil(t, plain)
	assert.Equal(t, errmsgs.DBError.GetCode(), plain.GetCode())
}

// A unique index that fires is a conflict with what is already there, not a
// server fault — the same 409 the Mongo layer gives for the same thing.
func TestRepo_duplicateKeyIsAConflict(t *testing.T) {
	ctx := newCtxWith(t, &Account{})
	accounts := repository.New[Account](ctx)
	require.Nil(t, accounts.Create(&Account{Email: "a@b.co"}))

	err := accounts.Create(&Account{Email: "a@b.co"})
	require.NotNil(t, err)
	assert.Equal(t, http.StatusConflict, err.GetStatus())
	assert.Equal(t, "DUPLICATE_KEY", err.GetCode())
	assert.True(t, errors.Is(err, gorm.ErrDuplicatedKey), "the driver's classification stays reachable")
}
