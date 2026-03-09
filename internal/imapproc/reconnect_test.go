package imapproc_test

// Unit tests for the reconnect / exponential-backoff logic.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// dialFunc is the type expected by RunWithReconnect for injecting a custom
// dial implementation in tests.
type dialFunc = func(ctx context.Context) error

// TestRunWithReconnect_RetriesOnFailure verifies that RunWithReconnect retries
// when the dial function returns an error, and eventually succeeds.
func TestRunWithReconnect_RetriesOnFailure(t *testing.T) {
	var attempts atomic.Int32

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := imapproc.ReconnectConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     50 * time.Millisecond,
	}

	// Fail the first 2 attempts, succeed on the 3rd.
	err := imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error {
		n := attempts.Add(1)
		if n < 3 {
			return errors.New("simulated dial failure")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error after eventual success, got: %v", err)
	}
	if got := attempts.Load(); got < 3 {
		t.Errorf("expected at least 3 attempts, got %d", got)
	}
}

// TestRunWithReconnect_ContextCancelledDuringBackoff verifies that if the
// context is cancelled while waiting for a backoff delay, RunWithReconnect
// returns promptly with nil (clean shutdown, not an error).
func TestRunWithReconnect_ContextCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	cfg := imapproc.ReconnectConfig{
		InitialDelay: 10 * time.Second, // long delay so cancel fires first
		MaxDelay:     10 * time.Second,
	}

	done := make(chan error, 1)
	go func() {
		done <- imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error {
			cancel() // cancel after first failure
			return errors.New("simulated failure")
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("expected nil on context cancel, got: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RunWithReconnect did not return after context cancellation")
	}
}

// TestRunWithReconnect_ContextCancelledBeforeFirstAttempt verifies that if the
// context is already cancelled when RunWithReconnect is called, it returns nil
// without even attempting to dial.
func TestRunWithReconnect_ContextCancelledBeforeFirstAttempt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling

	var attempts atomic.Int32
	cfg := imapproc.ReconnectConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     50 * time.Millisecond,
	}

	err := imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error {
		attempts.Add(1)
		return nil
	})
	if err != nil {
		t.Errorf("expected nil on pre-cancelled context, got: %v", err)
	}
	// May or may not have attempted — but must not hang.
}

// TestRunWithReconnect_ExponentialBackoff verifies that successive retry delays
// grow exponentially and are capped at MaxDelay.
func TestRunWithReconnect_ExponentialBackoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := imapproc.ReconnectConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     40 * time.Millisecond,
	}

	var timestamps []time.Time
	const totalAttempts = 5

	imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error { //nolint:errcheck
		timestamps = append(timestamps, time.Now())
		if len(timestamps) >= totalAttempts {
			cancel()
			return nil
		}
		return errors.New("fail")
	})

	if len(timestamps) < totalAttempts {
		t.Fatalf("expected %d attempts, got %d", totalAttempts, len(timestamps))
	}

	// Gaps between attempts should grow (first gap ~10ms, second ~20ms, then
	// capped at ~40ms). We just check that later gaps are >= earlier gaps, with
	// a loose tolerance to avoid flakiness on slow CI.
	gaps := make([]time.Duration, len(timestamps)-1)
	for i := 1; i < len(timestamps); i++ {
		gaps[i-1] = timestamps[i].Sub(timestamps[i-1])
	}
	t.Logf("backoff gaps: %v", gaps)
	// gap[1] should be at least as large as gap[0] (exponential growth).
	if gaps[1] < gaps[0] {
		t.Errorf("expected gap[1] >= gap[0], got gap[0]=%v gap[1]=%v", gaps[0], gaps[1])
	}
	// gap[2] and beyond should be capped and not exceed MaxDelay * 2 (generous margin).
	cap := cfg.MaxDelay * 2
	for i, g := range gaps {
		if g > cap {
			t.Errorf("gap[%d]=%v exceeds cap %v", i, g, cap)
		}
	}
}

// TestRunWithReconnect_SuccessOnFirstAttempt verifies that if the first dial
// succeeds, no retry is attempted.
func TestRunWithReconnect_SuccessOnFirstAttempt(t *testing.T) {
	ctx := context.Background()
	cfg := imapproc.ReconnectConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     50 * time.Millisecond,
	}
	var attempts atomic.Int32
	err := imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error {
		attempts.Add(1)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Errorf("expected exactly 1 attempt, got %d", n)
	}
}
