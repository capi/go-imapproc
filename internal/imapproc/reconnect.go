package imapproc

import (
	"context"
	"errors"
	"log"
	"time"
)

// DefaultReconnectInitialDelay is the starting backoff delay for reconnection
// attempts. Each successive failure doubles the delay up to
// DefaultReconnectMaxDelay.
const DefaultReconnectInitialDelay = 5 * time.Second

// DefaultReconnectMaxDelay is the maximum backoff delay between reconnection
// attempts.
const DefaultReconnectMaxDelay = 5 * time.Minute

// ErrConnectionLost is a sentinel error that the dial function can return to
// indicate that the connection was lost and should be retried. It behaves
// identically to any other non-nil error returned from the dial function —
// RunWithReconnect will retry. It is provided as a convenience so callers can
// be explicit about the reason for the retry.
var ErrConnectionLost = errors.New("connection lost")

// ReconnectConfig holds the exponential backoff parameters for RunWithReconnect.
type ReconnectConfig struct {
	// InitialDelay is the first backoff delay. A zero value uses
	// DefaultReconnectInitialDelay.
	InitialDelay time.Duration

	// MaxDelay caps the backoff delay. A zero value uses
	// DefaultReconnectMaxDelay.
	MaxDelay time.Duration
}

// RunWithReconnect calls dialAndRun repeatedly until it returns nil or the
// context is cancelled. Between successive failures it waits an exponentially
// growing delay, starting at cfg.InitialDelay and capped at cfg.MaxDelay.
//
// dialAndRun is expected to establish a connection, call imapproc.Run, and
// return the result. Any non-nil return value triggers a retry (after the
// backoff delay). A nil return value — or a cancelled context — stops the
// loop.
//
// RunWithReconnect returns nil when the context is cancelled (clean shutdown)
// or when dialAndRun returns nil.
func RunWithReconnect(ctx context.Context, cfg ReconnectConfig, dialAndRun func(context.Context) error) error {
	initialDelay := cfg.InitialDelay
	if initialDelay <= 0 {
		initialDelay = DefaultReconnectInitialDelay
	}
	maxDelay := cfg.MaxDelay
	if maxDelay <= 0 {
		maxDelay = DefaultReconnectMaxDelay
	}

	delay := initialDelay

	for {
		if ctx.Err() != nil {
			return nil
		}

		err := dialAndRun(ctx)
		if err == nil {
			return nil
		}

		if ctx.Err() != nil {
			// Context was cancelled inside dialAndRun; treat as clean shutdown.
			return nil
		}

		log.Printf("connection error: %v; reconnecting in %s", err, delay)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}

		// Exponential backoff.
		delay *= 2
		if delay > maxDelay {
			delay = maxDelay
		}
	}
}
