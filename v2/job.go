package core

import (
	"encoding/json"
	"time"
)

// Trigger records what caused a run to be created.
type Trigger string

const (
	TriggerSchedule Trigger = "schedule"
	TriggerManual   Trigger = "manual"
	TriggerRetry    Trigger = "retry"
	TriggerReplay   Trigger = "replay"
	TriggerEvent    Trigger = "event"
)

// RunStatus is the lifecycle state of a single run.
type RunStatus string

const (
	RunQueued    RunStatus = "queued"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
	RunCanceled  RunStatus = "canceled"
	RunSkipped   RunStatus = "skipped"
)

// IsTerminal reports whether no further transition is possible from s.
func (s RunStatus) IsTerminal() bool {
	switch s {
	case RunSucceeded, RunFailed, RunCanceled, RunSkipped:
		return true
	}
	return false
}

// ConcurrencyPolicy decides what happens when a run cannot get a concurrency
// slot for its job or queue.
type ConcurrencyPolicy uint8

const (
	// ConcurrencyEnqueue puts the run back on the queue with a short backoff
	// until a slot frees up. Nothing is lost — the default.
	//
	// Careful with frequent cron schedules: if the job takes longer than its
	// interval the queue grows without bound. Use ConcurrencySkip there.
	ConcurrencyEnqueue ConcurrencyPolicy = iota
	// ConcurrencySkip finishes the run immediately as RunSkipped.
	ConcurrencySkip
	// ConcurrencyReplace cancels the currently running run(s) of the job and
	// takes their place ("latest wins").
	ConcurrencyReplace
)

// LogPolicy controls whether a run's logs are persisted to the store. Logs are
// by far the largest consumer of storage (one row per line, per run), so the
// default is off — see the notes on JobDef.Logs.
type LogPolicy uint8

const (
	// LogOff never writes logs to the store. Logs still go to the application
	// logger (stdout), live tailing still works while the run is in flight, and
	// the tail of the buffer is still attached to JobRun.Error on failure.
	LogOff LogPolicy = iota
	// LogOnFailure buffers logs in memory and flushes them to the store only if
	// the run does not succeed. Successful runs cost nothing.
	LogOnFailure
	// LogAlways persists every line of every run. Reserve it for important,
	// infrequent jobs.
	LogAlways
)

// LogLimits bounds what a single run may persist.
type LogLimits struct {
	MaxLines int    // 0 = DefaultLogMaxLines
	MaxBytes int    // 0 = DefaultLogMaxBytes
	MinLevel string // "debug" (default) | "info" | "warn" | "error"
	// TailLines is how many trailing lines are attached to JobRun.Error when a
	// run fails, even under LogOff. 0 = DefaultLogTailLines.
	TailLines int
}

// Job runner defaults.
const (
	DefaultQueue        = "default"
	DefaultJobTimeout   = 5 * time.Minute
	DefaultStopGrace    = 30 * time.Second
	DefaultRetainRuns   = 30 * 24 * time.Hour
	DefaultRetainLogs   = 7 * 24 * time.Hour
	DefaultLogMaxLines  = 1000
	DefaultLogMaxBytes  = 256 << 10
	DefaultLogTailLines = 50
	// DefaultSlotBackoff is how long a run waits before retrying to get a
	// concurrency slot under ConcurrencyEnqueue.
	DefaultSlotBackoff = 2 * time.Second
)

// BackoffFunc returns how long to wait before the given attempt (1-based, so
// attempt 2 is the first retry).
type BackoffFunc func(attempt int) time.Duration

// ExponentialBackoff doubles base each attempt, capped at max.
func ExponentialBackoff(base, max time.Duration) BackoffFunc {
	return func(attempt int) time.Duration {
		d := base
		for i := 1; i < attempt-1; i++ {
			d *= 2
			if d >= max {
				return max
			}
		}
		if d > max {
			return max
		}
		return d
	}
}

// JobDef is the definition of a job: everything that is true of *every* run of
// it. A run's own data (params, status, timing) lives in JobRun.
type JobDef struct {
	// Name uniquely identifies the job. Required.
	Name string
	// Description is shown by the admin API/UI.
	Description string
	// Schedule makes the job run automatically. nil = manual-only.
	Schedule Schedule
	// Queue routes the job to a worker pool. Empty = DefaultQueue.
	Queue string
	// Timeout bounds a single run; the run context is canceled after it.
	// 0 = DefaultJobTimeout.
	Timeout time.Duration
	// StopGrace is how long the runner waits for a handler to return after the
	// run has been canceled, before giving up on it. 0 = DefaultStopGrace.
	StopGrace time.Duration
	// MaxAttempts is the total number of tries, including the first.
	// 0 or 1 = no automatic retry.
	MaxAttempts int
	// Backoff computes the delay before each retry. nil = exponential 1s→5m.
	Backoff BackoffFunc
	// MaxConcurrent limits how many runs of this job may execute at once.
	// 0 = unlimited, 1 = singleton.
	MaxConcurrent int
	// Concurrency decides what to do when the limit is hit.
	Concurrency ConcurrencyPolicy
	// Replayable allows operators to re-run a finished run. Default true; set
	// false for jobs where replaying is dangerous (payments, real emails).
	Replayable *bool
	// Params describes the parameters the job accepts, so an admin API can list
	// them and a UI can build a form. RegisterJob fills this in from the
	// parameter type; set it explicitly only to override what reflection found.
	Params []ParamField
	// Logs is the per-job log policy, overriding the runner default.
	// nil = use the runner's policy (LogOff unless configured).
	Logs *LogPolicy
	// RetainRuns / RetainLogs are TTLs used by the purge job.
	RetainRuns time.Duration
	RetainLogs time.Duration
}

// queue returns the effective queue name.
func (d JobDef) queue() string {
	if d.Queue == "" {
		return DefaultQueue
	}
	return d.Queue
}

// timeout returns the effective run timeout.
func (d JobDef) timeout() time.Duration {
	if d.Timeout <= 0 {
		return DefaultJobTimeout
	}
	return d.Timeout
}

// stopGrace returns the effective grace period after cancellation.
func (d JobDef) stopGrace() time.Duration {
	if d.StopGrace <= 0 {
		return DefaultStopGrace
	}
	return d.StopGrace
}

// maxAttempts returns the effective attempt count (never below 1).
func (d JobDef) maxAttempts() int {
	if d.MaxAttempts < 1 {
		return 1
	}
	return d.MaxAttempts
}

// backoff returns the effective backoff function.
func (d JobDef) backoff() BackoffFunc {
	if d.Backoff != nil {
		return d.Backoff
	}
	return ExponentialBackoff(time.Second, 5*time.Minute)
}

// replayable reports whether replaying runs of this job is allowed.
func (d JobDef) replayable() bool {
	return d.Replayable == nil || *d.Replayable
}

// RunError is the failure recorded on a run.
type RunError struct {
	Code    string `json:"code,omitempty"`
	Status  int    `json:"status,omitempty"`
	Message string `json:"message"`
	// Fields carries per-field violations when the failure was a validation
	// error — the same shape the HTTP layer returns, so bad parameters are as
	// readable on a run as they are on a request.
	Fields json.RawMessage `json:"fields,omitempty"`
	Stack  string          `json:"stack,omitempty"`
	// Tail holds the last lines logged before the failure. It is kept even when
	// the log policy is LogOff, so a failed run is always debuggable.
	Tail []string `json:"tail,omitempty"`
}

// JobRun is one execution of a job — the single record that answers "is it
// queued", "is it running", "did it work" and "who asked for it".
//
// It is also a repository model (see the jobstore package), so services can
// query it with repository.New[core.JobRun](ctx).
type JobRun struct {
	ID      string          `json:"id" gorm:"column:id;primaryKey;size:36"`
	JobName string          `json:"job_name" gorm:"column:job_name;size:191;index:idx_job_runs_job_status,priority:1"`
	Queue   string          `json:"queue" gorm:"column:queue;size:64"`
	Params  json.RawMessage `json:"params,omitempty" gorm:"column:params;type:text"`

	Status      RunStatus `json:"status" gorm:"column:status;size:16;index:idx_job_runs_job_status,priority:2;index:idx_job_runs_status_sched,priority:1"`
	Trigger     Trigger   `json:"trigger" gorm:"column:trigger;size:16"`
	TriggeredBy string    `json:"triggered_by,omitempty" gorm:"column:triggered_by;size:191"`
	IdemKey     string    `json:"idem_key,omitempty" gorm:"column:idem_key;size:191;index"`

	Attempt     int `json:"attempt" gorm:"column:attempt"`
	MaxAttempts int `json:"max_attempts" gorm:"column:max_attempts"`

	ScheduledAt time.Time  `json:"scheduled_at" gorm:"column:scheduled_at;index:idx_job_runs_status_sched,priority:2"`
	StartedAt   *time.Time `json:"started_at,omitempty" gorm:"column:started_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty" gorm:"column:finished_at"`
	DurationMS  int64      `json:"duration_ms" gorm:"column:duration_ms"`

	Progress        int             `json:"progress" gorm:"column:progress"`
	ProgressMessage string          `json:"progress_message,omitempty" gorm:"column:progress_message;size:255"`
	Result          json.RawMessage `json:"result,omitempty" gorm:"column:result;type:text"`
	Error           *RunError       `json:"error,omitempty" gorm:"column:error;serializer:json;type:text"`

	WorkerID string `json:"worker_id,omitempty" gorm:"column:worker_id;size:64"`

	// CaptureLogs overrides the job's log policy for this run only — what
	// "trigger it by hand and give me the full logs this once" sets.
	CaptureLogs *LogPolicy `json:"capture_logs,omitempty" gorm:"column:capture_logs"`

	CancelRequestedAt *time.Time `json:"cancel_requested_at,omitempty" gorm:"column:cancel_requested_at"`
	CanceledBy        string     `json:"canceled_by,omitempty" gorm:"column:canceled_by;size:191"`
	CancelReason      string     `json:"cancel_reason,omitempty" gorm:"column:cancel_reason;size:255"`

	// ReplayOf is the run this one was replayed from; RootID is the first run of
	// the chain (a replay of a replay still points at the original).
	ReplayOf string `json:"replay_of,omitempty" gorm:"column:replay_of;size:36"`
	RootID   string `json:"root_id,omitempty" gorm:"column:root_id;size:36"`

	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName implements IModel.
func (JobRun) TableName() string { return "job_runs" }

// clone returns a deep-enough copy so stores never hand out shared pointers.
func (r *JobRun) clone() *JobRun {
	if r == nil {
		return nil
	}
	cp := *r
	if r.Params != nil {
		cp.Params = append(json.RawMessage(nil), r.Params...)
	}
	if r.Result != nil {
		cp.Result = append(json.RawMessage(nil), r.Result...)
	}
	if r.Error != nil {
		e := *r.Error
		e.Tail = append([]string(nil), r.Error.Tail...)
		if r.Error.Fields != nil {
			e.Fields = append(json.RawMessage(nil), r.Error.Fields...)
		}
		cp.Error = &e
	}
	if r.StartedAt != nil {
		t := *r.StartedAt
		cp.StartedAt = &t
	}
	if r.FinishedAt != nil {
		t := *r.FinishedAt
		cp.FinishedAt = &t
	}
	if r.CancelRequestedAt != nil {
		t := *r.CancelRequestedAt
		cp.CancelRequestedAt = &t
	}
	return &cp
}

// JobLog is one persisted log line of a run.
//
// Seq orders the lines of a run and doubles as the paging cursor. It is offset
// by the attempt (see logSeqBase), so a retry continues after the previous
// attempt instead of colliding with it.
type JobLog struct {
	RunID   string         `json:"run_id" gorm:"column:run_id;size:36;primaryKey"`
	Seq     int64          `json:"seq" gorm:"column:seq;primaryKey"`
	Attempt int            `json:"attempt" gorm:"column:attempt"`
	At      time.Time      `json:"at" gorm:"column:at"`
	Level   string         `json:"level" gorm:"column:level;size:8"`
	Message string         `json:"message" gorm:"column:message;type:text"`
	Attrs   map[string]any `json:"attrs,omitempty" gorm:"column:attrs;serializer:json;type:text"`
}

// TableName implements IModel.
func (JobLog) TableName() string { return "job_run_logs" }

// logSeqPerAttempt is the seq range reserved for one attempt.
const logSeqPerAttempt = 1_000_000

// logSeqBase is the first seq of an attempt (1-based).
func logSeqBase(attempt int) int64 {
	if attempt < 1 {
		attempt = 1
	}
	return int64(attempt-1) * logSeqPerAttempt
}

// JobRunFilter narrows a run listing.
type JobRunFilter struct {
	JobName  string
	Statuses []RunStatus
	Trigger  Trigger
	Queue    string
	From     *time.Time
	To       *time.Time
	Page     *PageOptions
}
