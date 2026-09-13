package coretest_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/coretest"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/repository"
	"github.com/pskclub/mine-core/v2/valid"
)

type user struct {
	ID    uint   `gorm:"primarykey" json:"id"`
	Email string `json:"email"`
}

func (user) TableName() string { return "users" }

type createUser struct {
	Email *string `json:"email"`
	Note  *string `query:"note"`
}

func (r *createUser) Valid(ctx core.IContext) core.IError {
	v := valid.New(ctx)
	v.Str("email", r.Email).Required().Email()
	v.Str("note", r.Note).Required()
	return v.Error()
}

func TestNewContext_isReadyForRepositories(t *testing.T) {
	ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&user{}))

	require.Nil(t, repository.New[user](ctx).Create(&user{Email: "a@b.co"}))

	found, err := repository.New[user](ctx).Where("email = ?", "a@b.co").FindOne()
	require.Nil(t, err)
	assert.Equal(t, "a@b.co", found.Email)
}

// Each call must get its own database, or tests start depending on order.
func TestNewContext_isolatesEachTest(t *testing.T) {
	first := coretest.NewContext(t, coretest.WithAutoMigrate(&user{}))
	require.Nil(t, repository.New[user](first).Create(&user{Email: "only@here.co"}))

	second := coretest.NewContext(t, coretest.WithAutoMigrate(&user{}))
	n, err := repository.New[user](second).Count()

	require.Nil(t, err)
	assert.Equal(t, int64(0), n, "a second fixture must not see the first's rows")
}

func TestNewContext_envIsScopedToTheTest(t *testing.T) {
	ctx := coretest.NewContext(t, coretest.WithEnv(map[string]string{"jwt_secret": "s3cret"}))

	assert.Equal(t, "s3cret", ctx.ENV().Config().JWTSecret)
	assert.True(t, ctx.ENV().IsTest(), "the fixture runs as ENV=test")
}

func TestServer_drivesTheRealStack(t *testing.T) {
	srv := coretest.NewServer(t, coretest.WithAutoMigrate(&user{}))
	srv.GET("/users/:id", func(c core.IHTTPContext) error {
		return c.JSON(http.StatusOK, map[string]string{"id": c.Param("id")})
	})

	body := srv.Get("/users/42").RequireStatus(http.StatusOK).Map()

	assert.Equal(t, "42", body["id"])
}

// A rejected request is decoded as the framework's error shape, including where
// each field was bound from.
func TestServer_decodesValidationErrors(t *testing.T) {
	srv := coretest.NewServer(t, coretest.WithAutoMigrate(&user{}))
	srv.POST("/users", func(c core.IHTTPContext) error {
		var req createUser
		return c.BindWithValidate(&req)
	})

	res := srv.Post("/users", `{"email":"nope"}`).RequireStatus(http.StatusBadRequest)

	body := res.Error()
	assert.Equal(t, "INVALID_PARAMS", body.Code)
	assert.Equal(t, "INVALID_EMAIL", body.Fields["email"].Code)
	assert.Equal(t, "body", body.Fields["email"].In)
	assert.Equal(t, "query", body.Fields["note"].In, "sources survive the round trip")

	assert.Equal(t, map[string]string{"email": "INVALID_EMAIL", "note": "REQUIRED"}, res.FieldCodes())
}

// A panic must surface as a 500 rather than taking the test process down.
func TestServer_recoversPanics(t *testing.T) {
	srv := coretest.NewServer(t)
	srv.GET("/boom", func(c core.IHTTPContext) error { panic("kaboom") })

	srv.Get("/boom").RequireStatus(http.StatusInternalServerError)
}

func TestServer_seedThroughTheSameDatabase(t *testing.T) {
	srv := coretest.NewServer(t, coretest.WithAutoMigrate(&user{}))
	srv.GET("/count", func(c core.IHTTPContext) error {
		n, err := repository.New[user](c).Count()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]int64{"count": n})
	})

	require.Nil(t, repository.New[user](srv.Context()).Create(&user{Email: "seeded@b.co"}))

	body := srv.Get("/count").RequireStatus(http.StatusOK).Map()
	assert.Equal(t, float64(1), body["count"], "the request sees rows the test seeded")
}

func TestJob_runsAndRecordsSuccess(t *testing.T) {
	j := coretest.NewJob(t, coretest.WithAutoMigrate(&user{}))

	ran := false
	j.Register("noop", func(c core.ICronjobContext) error {
		ran = true
		return repository.New[user](c).Create(&user{Email: "from-job@b.co"})
	})

	run := j.Run("noop", nil)

	assert.True(t, ran, "the handler was called")
	assert.Equal(t, core.RunSucceeded, run.Status)

	n, err := repository.New[user](j.Context()).Count()
	require.Nil(t, err)
	assert.Equal(t, int64(1), n, "what the job wrote is visible afterwards")
}

// A failing job is a recorded outcome, not a test failure — that distinction is
// what lets a test assert on failure handling.
func TestJob_recordsFailure(t *testing.T) {
	j := coretest.NewJob(t)
	j.Register("bad", func(c core.ICronjobContext) error { return errmsgs.BadRequest })

	run := j.Run("bad", nil)

	assert.Equal(t, core.RunFailed, run.Status)
	require.NotNil(t, run.Error)
}

func TestAssertions(t *testing.T) {
	t.Run("RequireStatus and RequireCode read the error", func(t *testing.T) {
		err := core.New(http.StatusConflict, "CONFLICT", "taken")

		coretest.RequireStatus(t, err, http.StatusConflict)
		coretest.RequireCode(t, err, "CONFLICT")
	})

	t.Run("RequireIs matches a sentinel through wrapping", func(t *testing.T) {
		coretest.RequireIs(t, core.Wrap(errmsgs.NotFound, "loading user"), errmsgs.NotFound)
	})

	t.Run("Fields reads a validation error", func(t *testing.T) {
		ctx := coretest.NewContext(t)

		v := valid.New(ctx)
		v.Str("email", nil).Required()

		assert.Equal(t, map[string]string{"email": "REQUIRED"}, coretest.FieldCodes(t, v.Error()))
	})
}

// The postgres path only runs when TEST_DATABASE_URL is set; on sqlite this
// records that it was skipped rather than passing silently.
func TestPostgresBackend(t *testing.T) {
	if !coretest.IsPostgres() {
		t.Skipf("set %s to exercise the postgres backend", coretest.EnvDatabaseURL)
	}

	ctx := coretest.NewContext(t, coretest.WithAutoMigrate(&user{}))
	require.Nil(t, repository.New[user](ctx).Create(&user{Email: "pg@b.co"}))

	n, err := repository.New[user](ctx).Count()
	require.Nil(t, err)
	assert.Equal(t, int64(1), n)
}
