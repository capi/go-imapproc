package main

// Integration tests: OnSuccess=delete behaviour.

import (
	"testing"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// TestIntegration_DeleteOnSuccess verifies that when OnSuccess is
// OnSuccessDelete, a successfully processed message is deleted from the
// mailbox.
func TestIntegration_DeleteOnSuccess(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "exit 0"),
		OnSuccess: imapproc.OnSuccessDelete,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	if n := countMessages(t, addr, testMailbox); n != 0 {
		t.Errorf("expected message to be deleted, but mailbox contains %d message(s)", n)
	}
}

// TestIntegration_DeleteNotPerformedOnFailure verifies that when OnSuccess is
// OnSuccessDelete, a message whose program exits non-zero is NOT deleted.
func TestIntegration_DeleteNotPerformedOnFailure(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "exit 1"),
		OnSuccess: imapproc.OnSuccessDelete,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	if n := countMessages(t, addr, testMailbox); n != 1 {
		t.Errorf("expected message to remain, but mailbox contains %d message(s)", n)
	}
}

// TestIntegration_MarkSeenOnSuccess_ExplicitConfig verifies that the default
// OnSuccessSeen action still marks messages as \Seen (regression guard).
func TestIntegration_MarkSeenOnSuccess_ExplicitConfig(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "exit 0"),
		OnSuccess: imapproc.OnSuccessSeen,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected message to be marked read, but unread count = %d", n)
	}
	// The message must still exist (only seen, not deleted).
	if n := countMessages(t, addr, testMailbox); n != 1 {
		t.Errorf("expected message to remain in mailbox, but count = %d", n)
	}
}
