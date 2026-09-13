package core

import (
	"context"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// IJobStore persists runs and (optionally) their logs. It is the extension
// point for "where do job records live": the in-memory implementation below is
// the default, the jobstore package ships a repository/GORM-backed one, and a
// service can implement its own.
//
// Methods take a context.Context because the store is app-scoped, not
// request-scoped.
type IJobStore interface {
	Create(ctx context.Context, run *JobRun) IError
	Update(ctx context.Context, run *JobRun) IError
	Get(ctx context.Context, id string) (*JobRun, IError)
	List(ctx context.Context, f JobRunFilter) (*Page[JobRun], IError)

	// FindActive returns the non-terminal run of jobName carrying idemKey, or
	// (nil, nil) when there is none. It is what makes triggering idempotent.
	FindActive(ctx context.Context, jobName, idemKey string) (*JobRun, IError)
	// RunningIDs lists the runs of jobName currently in RunRunning.
	RunningIDs(ctx context.Context, jobName string) ([]string, IError)

	RequestCancel(ctx context.Context, id, by, reason string) IError
	IsCancelRequested(ctx context.Context, id string) (bool, IError)

	AppendLogs(ctx context.Context, entries []JobLog) IError
	Logs(ctx context.Context, runID string, afterSeq int64, limit int) ([]JobLog, IError)

	// Purge deletes runs finished before runsBefore and logs older than
	// logsBefore, returning how many rows went away.
	Purge(ctx context.Context, runsBefore, logsBefore time.Time) (int64, IError)

	Close() IError
}

// ErrJobRunNotFound is returned when a run id does not exist.
func ErrJobRunNotFound(id string) *Error {
	return Newf(http.StatusNotFound, "JOB_RUN_NOT_FOUND", "job run %s not found", id)
}

// memoryJobStore keeps runs in memory. It is the zero-config default: perfect
// for tests and single-process services that do not need runs to survive a
// restart. Swap it for the repository-backed store when you do.
type memoryJobStore struct {
	mu   sync.RWMutex
	runs map[string]*JobRun
	logs map[string][]JobLog
}

var _ IJobStore = (*memoryJobStore)(nil)

// NewMemoryJobStore creates an in-memory store.
func NewMemoryJobStore() IJobStore {
	return &memoryJobStore{runs: map[string]*JobRun{}, logs: map[string][]JobLog{}}
}

func (s *memoryJobStore) Create(_ context.Context, run *JobRun) IError {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.runs[run.ID]; dup {
		return Newf(http.StatusConflict, "DUPLICATE_JOB_RUN", "job run %s already exists", run.ID)
	}
	s.runs[run.ID] = run.clone()
	return nil
}

func (s *memoryJobStore) Update(_ context.Context, run *JobRun) IError {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.runs[run.ID]
	if !ok {
		return ErrJobRunNotFound(run.ID)
	}
	cp := run.clone()
	// cancellation is written by another caller (Cancel), never lost on update
	if cur.CancelRequestedAt != nil && cp.CancelRequestedAt == nil {
		cp.CancelRequestedAt = cur.CancelRequestedAt
		cp.CanceledBy, cp.CancelReason = cur.CanceledBy, cur.CancelReason
	}
	cp.UpdatedAt = time.Now()
	s.runs[run.ID] = cp
	return nil
}

func (s *memoryJobStore) Get(_ context.Context, id string) (*JobRun, IError) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[id]
	if !ok {
		return nil, ErrJobRunNotFound(id)
	}
	return run.clone(), nil
}

func (s *memoryJobStore) List(_ context.Context, f JobRunFilter) (*Page[JobRun], IError) {
	s.mu.RLock()
	matched := make([]JobRun, 0, len(s.runs))
	for _, run := range s.runs {
		if matchesFilter(run, f) {
			matched = append(matched, *run.clone())
		}
	}
	s.mu.RUnlock()

	sort.Slice(matched, func(i, j int) bool {
		if matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].ID > matched[j].ID
		}
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})

	opts := f.Page
	if opts == nil {
		opts = &PageOptions{}
	}
	opts.normalize()
	total := int64(len(matched))
	start := min((opts.Page-1)*opts.Limit, total)
	end := min(start+opts.Limit, total)
	items := matched[start:end]
	return &Page[JobRun]{
		Items: items, Total: total, Count: int64(len(items)),
		Page: opts.Page, Limit: opts.Limit,
	}, nil
}

func matchesFilter(run *JobRun, f JobRunFilter) bool {
	if f.JobName != "" && run.JobName != f.JobName {
		return false
	}
	if f.Queue != "" && run.Queue != f.Queue {
		return false
	}
	if f.Trigger != "" && run.Trigger != f.Trigger {
		return false
	}
	if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, run.Status) {
		return false
	}
	if f.From != nil && run.CreatedAt.Before(*f.From) {
		return false
	}
	if f.To != nil && run.CreatedAt.After(*f.To) {
		return false
	}
	return true
}

func (s *memoryJobStore) FindActive(_ context.Context, jobName, idemKey string) (*JobRun, IError) {
	if idemKey == "" {
		return nil, nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, run := range s.runs {
		if run.JobName == jobName && run.IdemKey == idemKey && !run.Status.IsTerminal() {
			return run.clone(), nil
		}
	}
	return nil, nil
}

func (s *memoryJobStore) RunningIDs(_ context.Context, jobName string) ([]string, IError) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for _, run := range s.runs {
		if run.JobName == jobName && run.Status == RunRunning {
			ids = append(ids, run.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *memoryJobStore) RequestCancel(_ context.Context, id, by, reason string) IError {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return ErrJobRunNotFound(id)
	}
	now := time.Now()
	run.CancelRequestedAt, run.CanceledBy, run.CancelReason = &now, by, reason
	run.UpdatedAt = now
	return nil
}

func (s *memoryJobStore) IsCancelRequested(_ context.Context, id string) (bool, IError) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	run, ok := s.runs[id]
	if !ok {
		return false, ErrJobRunNotFound(id)
	}
	return run.CancelRequestedAt != nil, nil
}

func (s *memoryJobStore) AppendLogs(_ context.Context, entries []JobLog) IError {
	if len(entries) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		s.logs[e.RunID] = append(s.logs[e.RunID], e)
	}
	return nil
}

func (s *memoryJobStore) Logs(_ context.Context, runID string, afterSeq int64, limit int) ([]JobLog, IError) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	all := s.logs[runID]
	out := make([]JobLog, 0, len(all))
	for _, e := range all {
		if e.Seq > afterSeq {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *memoryJobStore) Purge(_ context.Context, runsBefore, logsBefore time.Time) (int64, IError) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, run := range s.runs {
		if run.Status.IsTerminal() && run.FinishedAt != nil && run.FinishedAt.Before(runsBefore) {
			delete(s.runs, id)
			delete(s.logs, id)
			n++
		}
	}
	for id, entries := range s.logs {
		kept := entries[:0]
		for _, e := range entries {
			if e.At.Before(logsBefore) {
				n++
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(s.logs, id)
			continue
		}
		s.logs[id] = kept
	}
	return n, nil
}

func (s *memoryJobStore) Close() IError { return nil }

// levelAtLeast reports whether level passes the minimum configured level.
func levelAtLeast(level, min string) bool {
	rank := func(s string) int {
		switch strings.ToLower(s) {
		case "debug":
			return 0
		case "warn", "warning":
			return 2
		case "error":
			return 3
		default: // info
			return 1
		}
	}
	if min == "" {
		return true
	}
	return rank(level) >= rank(min)
}
