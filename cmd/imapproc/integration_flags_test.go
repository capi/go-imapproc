package main

// Integration tests: behaviour of the --once flag.

import (
	"context"
	"testing"
	"time"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// TestIntegration_OnceFlag verifies that when cfg.Once is true, imapproc.Run
// processes all unread messages and returns without blocking in IDLE, even
// when the context is not cancelled.
func TestIntegration_OnceFlag(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "exit 0"),
		OnSuccess: imapproc.OnSuccessSeen,
		Once:      true,
	}

	c := dialInsecure(t, addr)
	// Use a background context (not cancelled) to prove that cfg.Once=true
	// causes an early return without waiting for IDLE or context cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- imapproc.Run(ctx, c, cfg, nil)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("Run did not return promptly with cfg.Once=true; timed out")
	}

	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread messages after --once run, got %d", n)
	}
}

// TestIntegration_ContextCancelledBeforeIdle ensures imapproc.Run exits
// cleanly (no error) when the context is already cancelled, without entering
// IDLE.
func TestIntegration_ContextCancelledBeforeIdle(t *testing.T) {
	_, _, addr := newTestServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cancel() // cancel immediately

	c := dialInsecure(t, addr)
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    writeTempScript(t, "exit 0"),
	}
	if err := imapproc.Run(ctx, c, cfg, nil); err != nil {
		t.Fatalf("unexpected error on cancelled context: %v", err)
	}
}
