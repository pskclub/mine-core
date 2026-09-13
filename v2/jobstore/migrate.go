package jobstore

import (
	core "github.com/pskclub/mine-core/v2"
	"gorm.io/gorm"
)

// Migrate creates (or updates) the two job tables, including the indexes the
// runner relies on: (job_name, status) for listings, (status, scheduled_at) for
// the queue poll, and (run_id, seq) for log paging.
//
// core never runs this for you — most services own their migrations. Call it at
// boot in small services; elsewhere run it once against a scratch database and
// take the resulting DDL into your own migration tool.
func Migrate(db *gorm.DB, opts ...Option) core.IError {
	cfg := newConfig(opts...)
	if err := db.Table(cfg.runsTable).AutoMigrate(&core.JobRun{}); err != nil {
		return core.Wrap(err, "jobstore: migrate runs table")
	}
	if err := db.Table(cfg.logsTable).AutoMigrate(&core.JobLog{}); err != nil {
		return core.Wrap(err, "jobstore: migrate logs table")
	}
	return nil
}
