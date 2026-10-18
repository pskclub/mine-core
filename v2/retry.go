package core

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// RetryPolicy says how many times to try something and how long to wait in
// between. The zero value is usable: DefaultRetryAttempts tries, exponential
// backoff from DefaultRetryBase capped at DefaultRetryMax, with full jitter.
//
// Jitter is on by default and deliberately so. A hundred workers that all hit
// the same outage retry at the same instant without it, and that synchronised
// wave is what turns a blip into a second outage.
type RetryPolicy struct {
	// Attempts is the total number of tries, the first one included. Zero means
	// DefaultRetryAttempts; one means no retry at all.
	Attempts int
	// Base is the wait after the first failure; each later wait doubles it.
	Base time.Duration
	// Max caps a single wait, so the doubling stops somewhere sensible.
	Max time.Duration
	// NoJitter uses the exact exponential wait. Only for a test that asserts on
	// timing — see the type comment for why production keeps the jitter.
	NoJitter bool
	// RetryIf decides whether an error is worth another try. Nil retries
	// everything except a cancelled context and an error marked with
	// RetryStop.
	RetryIf func(err error) bool
	// OnRetry is told about every failure that will be retried, with the wait
	// chosen — the hook for a log line or a metric.
	OnRetry func(attempt int, err error, wait time.Duration)
}

// Defaults of RetryPolicy.
const (
	DefaultRetryAttempts = 3
	DefaultRetryBase     = 200 * time.Millisecond
	DefaultRetryMax      = 5 * time.Second
)

func (p RetryPolicy) attempts() int {
	if p.Attempts <= 0 {
		return DefaultRetryAttempts
	}
	return p.Attempts
}

// Wait is the delay before trying again after the given failed attempt (1 for
// the first failure). An error carrying a RetryAfter hint — a 429 that said
// how long — wins over the computed backoff.
func (p RetryPolicy) Wait(attempt int, err error) time.Duration {
	if hint, ok := RetryAfterOf(err); ok && hint > 0 {
		return hint
	}
	base, maxWait := p.Base, p.Max
	if base <= 0 {
		base = DefaultRetryBase
	}
	if maxWait <= 0 {
		maxWait = DefaultRetryMax
	}
	wait := base
	for i := 1; i < attempt && wait < maxWait; i++ {
		wait *= 2
	}
	if wait > maxWait {
		wait = maxWait
	}
	if p.NoJitter || wait <= 0 {
		return wait
	}
	// full jitter: anywhere between zero and the exponential wait
	return time.Duration(rand.Int64N(int64(wait)))
}

// retryStopError marks a failure that retrying cannot fix.
type retryStopError struct{ err error }

func (e retryStopError) Error() string { return e.err.Error() }
func (e retryStopError) Unwrap() error { return e.err }

// RetryStop wraps an error to tell Retry to give up at once: a 400 from the
// upstream, a message that failed to decode — anything that will fail the same
// way next time. Retry returns the wrapped error itself, so a caller does not
// have to unwrap it.
func RetryStop(err error) error {
	if err == nil {
		return nil
	}
	return retryStopError{err: err}
}

// Retry runs fn until it returns nil, the policy's attempts are used up, ctx
// ends, or fn marks the failure with RetryStop. It returns fn's last error as
// fn returned it — an IError stays an IError — and ctx.Err() when the context
// ended first.
//
//	err := core.Retry(ctx, core.RetryPolicy{Attempts: 5}, func(attempt int) error {
//	    return client.Send(msg)
//	})
//
// The retry is around the whole of fn: anything fn does before failing is
// done again, so fn has to be safe to repeat — which is the same property a
// message handler or an HTTP PUT already needs.
func Retry(ctx context.Context, p RetryPolicy, fn func(attempt int) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	attempts := p.attempts()
	var last error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			if last != nil {
				return last
			}
			return err
		}
		last = fn(attempt)
		if last == nil {
			return nil
		}
		var stop retryStopError
		if errors.As(last, &stop) {
			return stop.err
		}
		if attempt >= attempts || errors.Is(last, context.Canceled) || errors.Is(last, context.DeadlineExceeded) {
			return last
		}
		if p.RetryIf != nil && !p.RetryIf(last) {
			return last
		}
		wait := p.Wait(attempt, last)
		if p.OnRetry != nil {
			p.OnRetry(attempt, last, wait)
		}
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return last
		}
	}
}
