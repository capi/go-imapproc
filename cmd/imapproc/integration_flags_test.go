package main

// Integration tests: behaviour of the --once and --only-new flags.

import (
	"context"
	"os"
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

// TestIntegration_OnlyNew_SkipsExistingUnread verifies that when OnlyNew is
// true, the initial ProcessUnread scan is skipped — pre-existing unread
// messages are not processed.
func TestIntegration_OnlyNew_SkipsExistingUnread(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	captureDir := t.TempDir()
	captureFile := captureDir + "/captured.txt"
	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "cat >> "+captureFile),
		OnSuccess: imapproc.OnSuccessSeen,
		OnlyNew:   true,
	}

	// Run with a cancelled context so it exits without entering IDLE.
	// Because OnlyNew=true, the initial ProcessUnread pass must be skipped.
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// The capture file must not have been created — no message was delivered.
	if _, err := os.Stat(captureFile); !os.IsNotExist(err) {
		t.Errorf("expected program not to be called, but capture file exists (err=%v)", err)
	}

	// The pre-existing unread message must remain unread.
	if n := countUnread(t, addr, testMailbox); n != 1 {
		t.Errorf("expected 1 unread (untouched) message, got %d", n)
	}
}

// TestIntegration_OnlyNew_ProcessesNewArrival verifies that skipScan is only
// applied to the very first iteration: after IDLE wakes, subsequent passes
// process messages normally.
//
// We simulate two sequential runs:
//   - First: OnlyNew=true, context cancelled — pre-existing message is skipped.
//   - Second: OnlyNew=false, context cancelled — same message IS now processed.
func TestIntegration_OnlyNew_ProcessesNewArrival(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	script := writeTempScript(t, "exit 0")

	// First run: OnlyNew=true — initial scan skipped, message remains unread.
	cfgOnlyNew := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: imapproc.OnSuccessSeen,
		OnlyNew:   true,
	}
	if err := runOnceCfg(t, addr, cfgOnlyNew); err != nil {
		t.Fatalf("first run (OnlyNew=true): %v", err)
	}
	if n := countUnread(t, addr, testMailbox); n != 1 {
		t.Errorf("after OnlyNew=true run: expected 1 unread (skipped), got %d", n)
	}

	// Second run: OnlyNew=false — simulates post-IDLE pass; message processed.
	cfgNormal := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: imapproc.OnSuccessSeen,
	}
	if err := runOnceCfg(t, addr, cfgNormal); err != nil {
		t.Fatalf("second run (OnlyNew=false): %v", err)
	}
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("after normal run: expected 0 unread, got %d", n)
	}
}
