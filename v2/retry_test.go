package core

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errFlaky = errors.New("flaky")

func fastPolicy(attempts int) RetryPolicy {
	return RetryPolicy{Attempts: attempts, Base: time.Millisecond, Max: 2 * time.Millisecond, NoJitter: true}
}

func TestRetry_succeedsOnceTheCallDoes(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), fastPolicy(5), func(attempt int) error {
		calls++
		assert.Equal(t, calls, attempt, "the attempt number counts from one")
		if calls < 3 {
			return errFlaky
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 3, calls)
}

func TestRetry_givesUpAfterTheAttemptsAndReturnsTheLastError(t *testing.T) {
	calls := 0
	last := New(503, "UPSTREAM_DOWN", "down")
	err := Retry(context.Background(), fastPolicy(3), func(int) error {
		calls++
		return last
	})
	assert.Equal(t, 3, calls)
	var ie IError
	require.ErrorAs(t, err, &ie, "an IError comes back as itself, not wrapped")
	assert.Equal(t, "UPSTREAM_DOWN", ie.GetCode())
}

func TestRetry_stopEndsItAtOnce(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), fastPolicy(5), func(int) error {
		calls++
		return RetryStop(errFlaky)
	})
	assert.Equal(t, 1, calls, "a permanent failure is not tried again")
	assert.True(t, errors.Is(err, errFlaky), "the marker is removed before the error is returned")
}

func TestRetry_retryIfDecides(t *testing.T) {
	calls := 0
	err := Retry(context.Background(), RetryPolicy{Attempts: 5, Base: time.Millisecond, NoJitter: true,
		RetryIf: func(err error) bool { return false },
	}, func(int) error {
		calls++
		return errFlaky
	})
	assert.Equal(t, 1, calls)
	assert.ErrorIs(t, err, errFlaky)
}

func TestRetry_stopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Retry(ctx, RetryPolicy{Attempts: 10, Base: time.Hour, NoJitter: true}, func(int) error {
		calls++
		cancel() // the caller goes away while we would be waiting an hour
		return errFlaky
	})
	assert.Equal(t, 1, calls)
	assert.ErrorIs(t, err, errFlaky, "the last failure is what the caller sees, not a bare context error")
}

func TestRetryPolicy_waitDoublesAndCaps(t *testing.T) {
	p := RetryPolicy{Base: 100 * time.Millisecond, Max: 350 * time.Millisecond, NoJitter: true}
	assert.Equal(t, 100*time.Millisecond, p.Wait(1, errFlaky))
	assert.Equal(t, 200*time.Millisecond, p.Wait(2, errFlaky))
	assert.Equal(t, 350*time.Millisecond, p.Wait(3, errFlaky), "capped at Max")
	assert.Equal(t, 350*time.Millisecond, p.Wait(9, errFlaky))

	jittered := RetryPolicy{Base: 100 * time.Millisecond, Max: time.Second}
	for range 20 {
		assert.Less(t, jittered.Wait(3, errFlaky), 400*time.Millisecond, "full jitter never exceeds the exponential wait")
	}
}

// A 429 that said how long to wait is believed over the computed backoff.
func TestRetryPolicy_honoursRetryAfter(t *testing.T) {
	err := New(429, "TOO_MANY_REQUESTS", "slow down").WithRetryAfter(750 * time.Millisecond)
	p := RetryPolicy{Base: time.Millisecond, NoJitter: true}
	assert.Equal(t, 750*time.Millisecond, p.Wait(1, err))

	hint, ok := RetryAfterOf(Wrap(err, "upstream"))
	assert.True(t, ok, "the hint survives wrapping")
	assert.Equal(t, 750*time.Millisecond, hint)
	_, ok = RetryAfterOf(errFlaky)
	assert.False(t, ok)
}
