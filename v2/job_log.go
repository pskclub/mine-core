package core

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"sync"
	"time"
)

// logFlushBatch is how many buffered lines trigger an early flush under
// LogAlways, so a long run's logs show up while it is still running.
const logFlushBatch = 50

// runLogSink collects one run's log lines and decides what reaches the store.
//
// Whatever the policy, three things always work:
//   - lines go to the application logger (stdout), tagged with job/run_id
//   - live tailing sees them while the run is in flight
//   - the last few lines are attached to JobRun.Error when the run fails
//
// Only *persistence* is governed by LogPolicy, because persistence is what
// costs storage.
type runLogSink struct {
	runID   string
	attempt int
	store   IJobStore
	hub     *logHub
	policy  LogPolicy
	limits  LogLimits

	mu        sync.Mutex
	seq       int64
	pending   []JobLog
	ring      []JobLog
	lines     int
	bytes     int
	truncated bool
	dropped   int
}

func newRunLogSink(runID string, attempt int, store IJobStore, hub *logHub, policy LogPolicy, limits LogLimits) *runLogSink {
	if limits.MaxLines <= 0 {
		limits.MaxLines = DefaultLogMaxLines
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultLogMaxBytes
	}
	if limits.TailLines <= 0 {
		limits.TailLines = DefaultLogTailLines
	}
	return &runLogSink{
		runID: runID, attempt: attempt, store: store, hub: hub,
		policy: policy, limits: limits, seq: logSeqBase(attempt),
	}
}

func (s *runLogSink) write(level, msg string, attrs map[string]any) {
	s.mu.Lock()
	s.seq++
	e := JobLog{
		RunID: s.runID, Seq: s.seq, Attempt: s.attempt, At: time.Now(),
		Level: level, Message: msg, Attrs: attrs,
	}

	// tail ring — kept regardless of policy, bounded by TailLines
	s.ring = append(s.ring, e)
	if len(s.ring) > s.limits.TailLines {
		s.ring = s.ring[len(s.ring)-s.limits.TailLines:]
	}

	var flush []JobLog
	if s.policy != LogOff && levelAtLeast(level, s.limits.MinLevel) {
		switch {
		case s.truncated:
			s.dropped++
		case s.lines >= s.limits.MaxLines || s.bytes >= s.limits.MaxBytes:
			s.truncated = true
			s.dropped++
			s.pending = append(s.pending, JobLog{
				RunID: s.runID, Seq: s.seq, Attempt: s.attempt, At: e.At, Level: "warn",
				Message: "log truncated: run exceeded the configured log limits",
			})
		default:
			s.lines++
			s.bytes += len(msg)
			s.pending = append(s.pending, e)
		}
		if s.policy == LogAlways && len(s.pending) >= logFlushBatch {
			flush, s.pending = s.pending, nil
		}
	}
	s.mu.Unlock()

	if s.hub != nil {
		s.hub.publish(e)
	}
	if len(flush) > 0 && s.store != nil {
		_ = s.store.AppendLogs(context.Background(), flush)
	}
}

// tail returns the last lines as plain strings, for JobRun.Error.
func (s *runLogSink) tail() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.ring))
	for _, e := range s.ring {
		out = append(out, fmt.Sprintf("%s %s %s", e.At.Format(time.RFC3339), e.Level, e.Message))
	}
	return out
}

// close flushes whatever the policy says should survive the run.
func (s *runLogSink) close(ctx context.Context, succeeded bool) {
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	dropped := s.dropped
	s.mu.Unlock()

	persist := s.policy == LogAlways || (s.policy == LogOnFailure && !succeeded)
	if !persist || s.store == nil || len(pending) == 0 {
		return
	}
	if dropped > 0 {
		pending = append(pending, JobLog{
			RunID: s.runID, Seq: s.nextSeq(), Attempt: s.attempt, At: time.Now(), Level: "warn",
			Message: fmt.Sprintf("log truncated: %d further lines were dropped", dropped),
		})
	}
	_ = s.store.AppendLogs(ctx, pending)
}

func (s *runLogSink) nextSeq() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	return s.seq
}

// runLogger is the ILogger handed to a job: it tees to the application logger
// and to the run's sink.
type runLogger struct {
	base ILogger
	sink *runLogSink
	with map[string]any
}

var _ ILogger = (*runLogger)(nil)

func newRunLogger(base ILogger, sink *runLogSink) *runLogger {
	// +2: the base logger is reached through runLogger's own Info/Warn/… and
	// log, and the job's line belongs to the job, not to this file
	return &runLogger{base: callerSkip(base, 2), sink: sink}
}

func (l *runLogger) Debug(msg string, args ...any) { l.log("debug", msg, args...) }
func (l *runLogger) Info(msg string, args ...any)  { l.log("info", msg, args...) }
func (l *runLogger) Warn(msg string, args ...any)  { l.log("warn", msg, args...) }
func (l *runLogger) Error(msg string, args ...any) { l.log("error", msg, args...) }

func (l *runLogger) log(level, msg string, args ...any) {
	switch level {
	case "debug":
		l.base.Debug(msg, args...)
	case "warn":
		l.base.Warn(msg, args...)
	case "error":
		l.base.Error(msg, args...)
	default:
		l.base.Info(msg, args...)
	}
	if l.sink == nil {
		return
	}
	attrs := map[string]any{}
	maps.Copy(attrs, l.with)
	mergeArgs(attrs, args)
	if len(attrs) == 0 {
		attrs = nil
	}
	l.sink.write(level, msg, attrs)
}

func (l *runLogger) With(args ...any) ILogger {
	next := map[string]any{}
	maps.Copy(next, l.with)
	mergeArgs(next, args)
	return &runLogger{base: l.base.With(args...), sink: l.sink, with: next}
}

func (l *runLogger) Slog() *slog.Logger { return l.base.Slog() }

// mergeArgs folds slog-style key/value pairs into a map.
func mergeArgs(dst map[string]any, args []any) {
	for i := 0; i < len(args); i++ {
		if a, ok := args[i].(slog.Attr); ok {
			dst[a.Key] = a.Value.Any()
			continue
		}
		key, ok := args[i].(string)
		if !ok {
			key = fmt.Sprintf("!badkey_%d", i)
		}
		if i+1 < len(args) {
			i++
			dst[key] = args[i]
			continue
		}
		dst[key] = nil
	}
}

// logHub broadcasts log lines of in-flight runs to live tailers. Nothing here
// touches the store, which is why tailing works even under LogOff.
type logHub struct {
	mu   sync.Mutex
	next int
	subs map[string]map[int]chan JobLog
}

func newLogHub() *logHub { return &logHub{subs: map[string]map[int]chan JobLog{}} }

// subscribe returns a channel of the run's lines and a function to stop.
func (h *logHub) subscribe(runID string, buffer int) (<-chan JobLog, func()) {
	if buffer <= 0 {
		buffer = 64
	}
	ch := make(chan JobLog, buffer)
	h.mu.Lock()
	id := h.next
	h.next++
	if h.subs[runID] == nil {
		h.subs[runID] = map[int]chan JobLog{}
	}
	h.subs[runID][id] = ch
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if m, ok := h.subs[runID]; ok {
				delete(m, id)
				if len(m) == 0 {
					delete(h.subs, runID)
				}
			}
			close(ch)
		})
	}
}

// publish delivers to subscribers without ever blocking the job.
func (h *logHub) publish(e JobLog) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subs[e.RunID] {
		select {
		case ch <- e:
		default: // slow consumer: drop rather than stall the run
		}
	}
}
