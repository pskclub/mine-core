package repository_test

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/repository"
)

type User struct {
	ID        uint `gorm:"primarykey"`
	Name      string
	Status    string
	Age       int
	DeletedAt gorm.DeletedAt `gorm:"index"`
}

func (User) TableName() string { return "users" }

func newCtx(t *testing.T) core.IContext {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	for _, u := range []User{
		{Name: "alice", Status: "active"},
		{Name: "bob", Status: "active"},
		{Name: "carol", Status: "inactive"},
	} {
		db.Create(&u)
	}

	t.Setenv("APP_ENV", "test")
	t.Setenv("APP_SERVICE", "test")
	env, err := core.NewEnvPath(t.TempDir())
	require.NoError(t, err)
	app, err := core.NewApp(env, core.WithSQL("default", db))
	require.NoError(t, err)
	return app.NewContext(context.Background())
}

func TestRepo_FindOneAndAll(t *testing.T) {
	ctx := newCtx(t)
	repo := repository.New[User](ctx)

	u, err := repo.Where("name = ?", "alice").FindOne()
	require.Nil(t, err)
	assert.Equal(t, "alice", u.Name)

	all, err := repo.Where("status = ?", "active").FindAll()
	require.Nil(t, err)
	assert.Len(t, all, 2)
}

func TestRepo_FindOne_notFound(t *testing.T) {
	ctx := newCtx(t)
	_, err := repository.New[User](ctx).Where("name = ?", "nobody").FindOne()
	require.NotNil(t, err)
	assert.ErrorIs(t, err, errmsgs.NotFound)
}

func TestRepo_copyOnChain_doesNotLeak(t *testing.T) {
	ctx := newCtx(t)
	base := repository.New[User](ctx)

	active, err := base.Where("status = ?", "active").Count()
	require.Nil(t, err)
	inactive, err := base.Where("status = ?", "inactive").Count()
	require.Nil(t, err)

	assert.Equal(t, int64(2), active)
	assert.Equal(t, int64(1), inactive, "chain leaked if this is 0/2")
}

func TestRepo_CreateUpdateDelete(t *testing.T) {
	ctx := newCtx(t)
	repo := repository.New[User](ctx)

	require.Nil(t, repo.Create(&User{Name: "dave", Status: "active"}))

	total, err := repository.New[User](ctx).Count()
	require.Nil(t, err)
	assert.Equal(t, int64(4), total)

	require.Nil(t, repository.New[User](ctx).Where("name = ?", "dave").Delete())

	_, ferr := repository.New[User](ctx).Where("name = ?", "dave").FindOne()
	assert.ErrorIs(t, ferr, errmsgs.NotFound, "dave should be deleted")
}

func TestRepo_Pagination(t *testing.T) {
	ctx := newCtx(t)
	page, err := repository.New[User](ctx).Order("id asc").Pagination(&core.PageOptions{Limit: 2, Page: 1})
	require.Nil(t, err)
	assert.Equal(t, int64(3), page.Total)
	assert.Equal(t, int64(2), page.Count)
	assert.Len(t, page.Items, 2)
}

// --- expanded GORM surface ---

func TestRepo_Or_Not(t *testing.T) {
	ctx := newCtx(t)
	got, err := repository.New[User](ctx).
		Where("status = ?", "active").Or("name = ?", "carol").FindAll()
	require.Nil(t, err)
	assert.Len(t, got, 3) // alice, bob (active) + carol (or)

	notCarol, err := repository.New[User](ctx).Not("name = ?", "carol").Count()
	require.Nil(t, err)
	assert.Equal(t, int64(2), notCarol)
}

func TestRepo_TakeAndLast(t *testing.T) {
	ctx := newCtx(t)
	first, err := repository.New[User](ctx).Take("name = ?", "alice")
	require.Nil(t, err)
	assert.Equal(t, "alice", first.Name)

	last, err := repository.New[User](ctx).Last() // orders by PK desc → highest id
	require.Nil(t, err)
	assert.Equal(t, "carol", last.Name)
}

func TestRepo_ExistsSaveUpdate(t *testing.T) {
	ctx := newCtx(t)
	repo := repository.New[User](ctx)

	ok, err := repo.Where("name = ?", "alice").Exists()
	require.Nil(t, err)
	assert.True(t, ok)

	ok, _ = repo.Where("name = ?", "nobody").Exists()
	assert.False(t, ok)

	// Save (full upsert)
	u, _ := repository.New[User](ctx).Where("name = ?", "alice").FindOne()
	u.Status = "vip"
	require.Nil(t, repository.New[User](ctx).Save(u))
	reloaded, _ := repository.New[User](ctx).Where("name = ?", "alice").FindOne()
	assert.Equal(t, "vip", reloaded.Status)

	// Update single column on a scope
	require.Nil(t, repository.New[User](ctx).Where("name = ?", "bob").Update("age", 40))
	bob, _ := repository.New[User](ctx).Where("name = ?", "bob").FindOne()
	assert.Equal(t, 40, bob.Age)
}

func TestRepo_PluckAndScan(t *testing.T) {
	ctx := newCtx(t)

	var names []string
	require.Nil(t, repository.New[User](ctx).Order("name asc").Pluck("name", &names))
	assert.Equal(t, []string{"alice", "bob", "carol"}, names)

	type row struct {
		Status string
		Total  int64
	}
	var rows []row
	require.Nil(t, repository.New[User](ctx).
		Select("status, count(*) as total").Group("status").Scan(&rows))
	assert.NotEmpty(t, rows)
}

func TestRepo_HardVsSoftDelete(t *testing.T) {
	ctx := newCtx(t)

	// soft delete → row hidden from normal queries but still present unscoped
	require.Nil(t, repository.New[User](ctx).Where("name = ?", "alice").Delete())
	_, err := repository.New[User](ctx).Where("name = ?", "alice").FindOne()
	assert.ErrorIs(t, err, errmsgs.NotFound)
	stillThere, cerr := repository.New[User](ctx).Unscoped().Where("name = ?", "alice").Count()
	require.Nil(t, cerr)
	assert.Equal(t, int64(1), stillThere, "soft-deleted row remains, visible when Unscoped")

	// hard delete → gone entirely
	require.Nil(t, repository.New[User](ctx).HardDelete("name = ?", "bob"))
	gone, _ := repository.New[User](ctx).Unscoped().Where("name = ?", "bob").Count()
	assert.Equal(t, int64(0), gone)
}

func TestRepo_Scopes(t *testing.T) {
	ctx := newCtx(t)
	active := func(db *gorm.DB) *gorm.DB { return db.Where("status = ?", "active") }

	n, err := repository.New[User](ctx).Scopes(active).Count()
	require.Nil(t, err)
	assert.Equal(t, int64(2), n)
}

func TestRepo_Scopes_nilIsSkipped(t *testing.T) {
	ctx := newCtx(t)
	active := func(db *gorm.DB) *gorm.DB { return db.Where("status = ?", "active") }

	// A caller assembling scopes from optional filters ends up with holes in the
	// slice; gorm's own Scopes panics on one at execution time, which is a long
	// way from the line that built it.
	n, err := repository.New[User](ctx).Scopes(nil, active, nil).Count()
	require.Nil(t, err)
	assert.Equal(t, int64(2), n)
}

// A scope that orders must not reach Pagination's count query.
//
// This is why Repo.Scopes applies its functions itself instead of handing them
// to gorm. gorm keeps scopes on the statement and runs them inside Execute,
// which is after Count has stripped ORDER BY and rewritten SELECT — so a
// deferred scope adds its clause to the count query after Count has already
// decided what to do about it:
//
//	SELECT count(*) FROM users ORDER BY name desc
//
// sqlite runs that happily and postgres answers 42803, which is the combination
// that keeps a test suite green while every list route 500s. Applying scopes on
// the spot is what puts the clause in front of Count, and this test is what
// keeps it there.
func TestRepo_Scopes_orderDoesNotReachTheCountQuery(t *testing.T) {
	ctx := newCtx(t)

	statements := make([]string, 0, 2)
	require.NoError(t, ctx.DB().Callback().Query().After("gorm:query").
		Register("test:capture_sql", func(db *gorm.DB) {
			statements = append(statements, db.Statement.SQL.String())
		}))

	ordered := func(db *gorm.DB) *gorm.DB { return db.Order("name desc") }

	page, err := repository.New[User](ctx).Scopes(ordered).
		Pagination(&core.PageOptions{Limit: 2, Page: 1})
	require.Nil(t, err)
	assert.Equal(t, int64(3), page.Total)

	require.Len(t, statements, 2, "Pagination runs a count and then a find")
	assert.NotContains(t, statements[0], "ORDER BY", "count query: %s", statements[0])
	assert.Contains(t, statements[1], "ORDER BY", "find query lost its order: %s", statements[1])
}

func TestRepo_RawAndExec(t *testing.T) {
	ctx := newCtx(t)

	var count int64
	require.Nil(t, repository.New[User](ctx).
		Raw(&count, "SELECT count(*) FROM users WHERE status = ?", "active"))
	assert.Equal(t, int64(2), count)

	require.Nil(t, repository.New[User](ctx).
		Exec("UPDATE users SET age = ? WHERE status = ?", 99, "active"))
	var ages []int
	repository.New[User](ctx).Where("status = ?", "active").Pluck("age", &ages)
	for _, a := range ages {
		assert.Equal(t, 99, a)
	}
}

func TestRepo_DBEscapeHatch(t *testing.T) {
	ctx := newCtx(t)
	// any GORM feature via the raw handle, still ctx-bound + scoped
	var n int64
	err := repository.New[User](ctx).Where("status = ?", "active").DB().Count(&n).Error
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)
}
