package main

// Integration test: IMAP IDLE periodic refresh behaviour.
//
// IMAP IDLE connections must be refreshed periodically; servers are permitted
// to drop an IDLE connection after 30 minutes (RFC 2177). The refresh is done
// by sending DONE and immediately re-issuing IDLE.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// TestIntegration_IdleRefreshesBeforeInterval verifies that imapproc.Run sends
// DONE and re-enters IDLE at least once within IdleRefreshInterval, even when
// no mail arrives and the server does not terminate IDLE on its own.
//
// The test sets a very short refresh interval (200 ms), waits long enough for
// at least two IDLE cycles to have occurred (600 ms), then cancels the context.
// An idle-entered counter is used to assert that IDLE was re-entered at least
// twice (initial entry + at least one refresh).
func TestIntegration_IdleRefreshesBeforeInterval(t *testing.T) {
	_, _, addr := newTestServer(t)

	cfg := imapproc.Config{
		User:                testUser,
		Pass:                testPass,
		Mailbox:             testMailbox,
		Exec:                writeTempScript(t, "true"),
		OnSuccess:           imapproc.OnSuccessSeen,
		IdleRefreshInterval: 200 * time.Millisecond,
	}

	newMail := make(chan struct{}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := dialInsecure(t, addr)

	// Count how many times IDLE is entered by wrapping the IdleEntered hook.
	var idleEntries atomic.Int32
	cfg.OnIdleEntered = func() { idleEntries.Add(1) }

	done := make(chan error, 1)
	go func() {
		done <- imapproc.Run(ctx, c, cfg, newMail)
	}()

	// Wait long enough for at least two IDLE cycles (initial + one refresh).
	time.Sleep(600 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	if n := idleEntries.Load(); n < 2 {
		t.Errorf("IDLE entered %d time(s), want at least 2 (initial + at least one refresh)", n)
	}
}
