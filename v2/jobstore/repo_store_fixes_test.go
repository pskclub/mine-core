package jobstore_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/jobstore"
)

// The listing filter comes off an HTTP query in devtools, so its order-by is
// untrusted input: anything that is not a run column is dropped, and what the
// caller passed is not rewritten underneath them.
func TestRepoStore_listOrderByIsCheckedNotTrusted(t *testing.T) {
	app := sqliteApp(t)
	store := jobstore.New(app)
	ctx := ctxT(t)
	require.Nil(t, store.Create(ctx, newRun("a")))
	require.Nil(t, store.Create(ctx, newRun("b")))

	page := &core.PageOptions{OrderBy: []string{"(SELECT 1); --", "job_name asc"}}
	got, err := store.List(ctx, core.JobRunFilter{Page: page})
	require.Nil(t, err, "an injected expression is dropped, not executed")
	assert.Equal(t, []string{"job_name asc"}, got.OrderBy)
	assert.Equal(t, []string{"(SELECT 1); --", "job_name asc"}, page.OrderBy, "the caller's options are left alone")

	// nothing usable falls back to the default, and the trigger filter still
	// works although TRIGGER is a reserved word on some engines
	got, err = store.List(ctx, core.JobRunFilter{
		Trigger: core.TriggerManual,
		Page:    &core.PageOptions{OrderBy: []string{"not_a_column"}},
	})
	require.Nil(t, err)
	assert.Equal(t, []string{"created_at desc"}, got.OrderBy)
}

// A worker finishing a run that Purge removed in the meantime writes its final
// status with Update. GORM's Save would insert the row again when nothing
// matched; a purged run has to stay purged.
func TestRepoStore_updateDoesNotResurrectAPurgedRun(t *testing.T) {
	app := sqliteApp(t)
	store := jobstore.New(app)
	ctx := ctxT(t)

	finished := time.Now().Add(-48 * time.Hour)
	run := newRun("gone", func(r *core.JobRun) {
		r.Status = core.RunSucceeded
		r.FinishedAt = &finished
	})
	require.Nil(t, store.Create(ctx, run))
	purged, perr := store.Purge(ctx, time.Now().Add(-24*time.Hour), time.Now())
	require.Nil(t, perr)
	require.EqualValues(t, 1, purged, "precondition: the run was purged")

	run.ProgressMessage = "late write from the worker"
	require.Nil(t, store.Update(ctx, run), "updating a missing row is not an error, just a no-op")

	_, err := store.Get(ctx, run.ID)
	require.NotNil(t, err, "the run must not come back")
}
