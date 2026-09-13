// Package jobstore provides durable backends for the job runner, built on the
// v2 repository (GORM underneath) rather than on raw *gorm.DB.
//
// Because core.JobRun and core.JobLog are ordinary core.IModel values, a service
// can query job history with the same repository API it already uses:
//
//	repository.New[core.JobRun](ctx).
//	    Where("job_name = ? AND status = ?", "settlement", core.RunFailed).
//	    FindAll()
package jobstore

import (
	"context"
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/errmsgs"
	"github.com/pskclub/mine-core/v2/repository"
	"gorm.io/gorm"
)

// Option configures a repository-backed backend.
type Option func(*config)

type config struct {
	runsTable string
	logsTable string
	conn      string
	poll      time.Duration
}

// WithTables overrides the table names (for services with a naming convention
// of their own).
func WithTables(runs, logs string) Option {
	return func(c *config) {
		if runs != "" {
			c.runsTable = runs
		}
		if logs != "" {
			c.logsTable = logs
		}
	}
}

// WithConnection stores job data on a named connection (core.IContext.DBS)
// instead of the default one.
func WithConnection(name string) Option { return func(c *config) { c.conn = name } }

func newConfig(opts ...Option) config {
	c := config{runsTable: core.JobRun{}.TableName(), logsTable: core.JobLog{}.TableName()}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// repoStore is an IJobStore backed by two tables through the repository.
type repoStore struct {
	app *core.App
	cfg config
}

var _ core.IJobStore = (*repoStore)(nil)

// New builds a durable store. Create the tables first with Migrate (or with your
// own migration tool — see Schema).
func New(app *core.App, opts ...Option) core.IJobStore {
	return &repoStore{app: app, cfg: newConfig(opts...)}
}

// ctxOf turns a plain context into the core context the repository needs.
func (s *repoStore) ctxOf(ctx context.Context) core.IContext {
	if ctx == nil {
		ctx = context.Background()
	}
	return s.app.NewContext(ctx, core.ModeCron)
}

// runs returns a repository scoped to the runs table on the configured
// connection. Table() is how the table name is overridden without any global
// state.
func (s *repoStore) runs(ctx context.Context) *repository.Repo[core.JobRun] {
	c := s.ctxOf(ctx)
	repo := repository.NewWithDB[core.JobRun](c, s.db(c))
	return repo.Table(s.cfg.runsTable)
}

func (s *repoStore) logs(ctx context.Context) *repository.Repo[core.JobLog] {
	c := s.ctxOf(ctx)
	repo := repository.NewWithDB[core.JobLog](c, s.db(c))
	return repo.Table(s.cfg.logsTable)
}

func (s *repoStore) db(c core.IContext) *gorm.DB {
	if s.cfg.conn == "" {
		return c.DB()
	}
	return c.DBS(s.cfg.conn)
}

func (s *repoStore) Create(ctx context.Context, run *core.JobRun) core.IError {
	return s.runs(ctx).Create(run)
}

// Update writes the run's own state. The cancellation columns are deliberately
// omitted: they are owned by RequestCancel, and a worker writing its result
// holds a copy from before the cancel was recorded — saving it wholesale would
// erase who stopped the run and why, exactly when that matters most.
func (s *repoStore) Update(ctx context.Context, run *core.JobRun) core.IError {
	run.UpdatedAt = time.Now()
	return s.runs(ctx).
		Omit("cancel_requested_at", "canceled_by", "cancel_reason").
		Where("id = ?", run.ID).
		Save(run)
}

func (s *repoStore) Get(ctx context.Context, id string) (*core.JobRun, core.IError) {
	run, err := s.runs(ctx).FindOne("id = ?", id)
	if err != nil {
		if errmsgs.IsNotFoundError(err) {
			return nil, core.ErrJobRunNotFound(id)
		}
		return nil, err
	}
	return run, nil
}

func (s *repoStore) List(ctx context.Context, f core.JobRunFilter) (*core.Page[core.JobRun], core.IError) {
	q := s.runs(ctx)
	if f.JobName != "" {
		q = q.Where("job_name = ?", f.JobName)
	}
	if f.Queue != "" {
		q = q.Where("queue = ?", f.Queue)
	}
	if f.Trigger != "" {
		q = q.Where("trigger = ?", f.Trigger)
	}
	if len(f.Statuses) > 0 {
		q = q.Where("status IN ?", f.Statuses)
	}
	if f.From != nil {
		q = q.Where("created_at >= ?", *f.From)
	}
	if f.To != nil {
		q = q.Where("created_at <= ?", *f.To)
	}
	opts := f.Page
	if opts == nil {
		opts = &core.PageOptions{}
	}
	if len(opts.OrderBy) == 0 {
		opts.OrderBy = []string{"created_at desc"}
	}
	return q.Pagination(opts)
}

func (s *repoStore) FindActive(ctx context.Context, jobName, idemKey string) (*core.JobRun, core.IError) {
	if idemKey == "" {
		return nil, nil
	}
	run, err := s.runs(ctx).
		Where("job_name = ? AND idem_key = ?", jobName, idemKey).
		Where("status IN ?", []core.RunStatus{core.RunQueued, core.RunRunning}).
		Order("created_at DESC").
		FindOne()
	if err != nil {
		if errmsgs.IsNotFoundError(err) {
			return nil, nil
		}
		return nil, err
	}
	return run, nil
}

func (s *repoStore) RunningIDs(ctx context.Context, jobName string) ([]string, core.IError) {
	var ids []string
	err := s.runs(ctx).
		Where("job_name = ? AND status = ?", jobName, core.RunRunning).
		Pluck("id", &ids)
	return ids, err
}

func (s *repoStore) RequestCancel(ctx context.Context, id, by, reason string) core.IError {
	now := time.Now()
	return s.runs(ctx).Where("id = ?", id).Updates(map[string]any{
		"cancel_requested_at": now,
		"canceled_by":         by,
		"cancel_reason":       reason,
		"updated_at":          now,
	})
}

// IsCancelRequested reads just the one column — running jobs poll this.
func (s *repoStore) IsCancelRequested(ctx context.Context, id string) (bool, core.IError) {
	run, err := s.runs(ctx).Select("id", "cancel_requested_at").FindOne("id = ?", id)
	if err != nil {
		if errmsgs.IsNotFoundError(err) {
			return false, core.ErrJobRunNotFound(id)
		}
		return false, err
	}
	return run.CancelRequestedAt != nil, nil
}

func (s *repoStore) AppendLogs(ctx context.Context, entries []core.JobLog) core.IError {
	if len(entries) == 0 {
		return nil
	}
	return s.logs(ctx).CreateInBatches(entries, 100)
}

func (s *repoStore) Logs(ctx context.Context, runID string, afterSeq int64, limit int) ([]core.JobLog, core.IError) {
	q := s.logs(ctx).Where("run_id = ? AND seq > ?", runID, afterSeq).Order("seq ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	return q.FindAll()
}

func (s *repoStore) Purge(ctx context.Context, runsBefore, logsBefore time.Time) (int64, core.IError) {
	var deleted int64

	logs := s.logs(ctx).Where("at < ?", logsBefore).DB().Delete(&core.JobLog{})
	if logs.Error != nil {
		return deleted, core.Wrap(logs.Error, "jobstore: purge logs")
	}
	deleted += logs.RowsAffected

	runs := s.runs(ctx).
		Where("finished_at IS NOT NULL AND finished_at < ?", runsBefore).
		DB().Delete(&core.JobRun{})
	if runs.Error != nil {
		return deleted, core.Wrap(runs.Error, "jobstore: purge runs")
	}
	deleted += runs.RowsAffected
	return deleted, nil
}

// Close is a no-op: the store does not own the connection pool, the App does.
func (s *repoStore) Close() core.IError { return nil }
