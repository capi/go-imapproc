package main

// Integration tests: message processing behaviour — which messages are
// processed, what the program receives on stdin, and how success/failure
// outcomes affect message state.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emersion/go-imap/v2"
)

// TestIntegration_OnlyUnreadProcessed verifies that only messages without
// \Seen are passed to the external program; already-read messages are skipped.
func TestIntegration_OnlyUnreadProcessed(t *testing.T) {
	_, user, addr := newTestServer(t)

	// Seed: one unread and one already-read message.
	appendMessage(t, user, testMailbox, testRawEmail)                // unread
	appendMessage(t, user, testMailbox, testRawEmail, imap.FlagSeen) // already read

	captureDir := t.TempDir()
	captureFile := filepath.Join(captureDir, "captured.txt")
	// Script appends its stdin to a file so we can count invocations.
	script := writeTempScript(t, "cat >> "+captureFile)

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	if count := strings.Count(string(got), "Hello, world!"); count != 1 {
		t.Errorf("expected exactly 1 message delivered to program, body contained %d occurrence(s)", count)
	}

	// processUnread should have marked the originally-unread one as seen.
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("unread count after run = %d, want 0", n)
	}
}

// TestIntegration_ProgramReceivesFullEmailOnStdin confirms the complete raw
// RFC 5322 email is delivered verbatim to the program's stdin.
func TestIntegration_ProgramReceivesFullEmailOnStdin(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	captureDir := t.TempDir()
	captureFile := filepath.Join(captureDir, "email.txt")
	script := writeTempScript(t, "cat > "+captureFile)

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading captured email: %v", err)
	}
	if !strings.Contains(string(got), "Hello, world!") {
		t.Errorf("program did not receive full email body; got:\n%s", got)
	}
}

// TestIntegration_MarkedReadOnSuccess verifies that a message receives the
// \Seen flag when the external program exits with code 0.
func TestIntegration_MarkedReadOnSuccess(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	script := writeTempScript(t, "exit 0")

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected message to be marked read, but unread count = %d", n)
	}
}

// TestIntegration_NotMarkedReadOnFailure verifies that a message keeps its
// unread status when the external program exits with a non-zero code.
func TestIntegration_NotMarkedReadOnFailure(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	script := writeTempScript(t, "exit 1")

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	if n := countUnread(t, addr, testMailbox); n != 1 {
		t.Errorf("expected message to remain unread, but unread count = %d", n)
	}
}

// TestIntegration_MultipleMessages_MixedOutcomes processes three messages:
// two succeed (exit 0) and one fails (exit 1 based on content). Verifies the
// correct subset gets marked read.
func TestIntegration_MultipleMessages_MixedOutcomes(t *testing.T) {
	_, user, addr := newTestServer(t)

	emailOK := "From: a@example.com\r\nSubject: ok\r\n\r\nSUCCESS\r\n"
	emailFail := "From: b@example.com\r\nSubject: fail\r\n\r\nFAILURE\r\n"

	appendMessage(t, user, testMailbox, emailOK)   // should be marked read
	appendMessage(t, user, testMailbox, emailFail) // should remain unread
	appendMessage(t, user, testMailbox, emailOK)   // should be marked read

	// Exit 1 if stdin contains "FAILURE", else exit 0.
	script := writeTempScript(t, `
body=$(cat)
if echo "$body" | grep -q FAILURE; then
  exit 1
fi
exit 0
`)

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	// Exactly 1 message (the FAILURE one) must remain unread.
	if n := countUnread(t, addr, testMailbox); n != 1 {
		t.Errorf("expected 1 unread message remaining, got %d", n)
	}
}
