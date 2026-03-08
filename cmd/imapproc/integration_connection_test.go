package main

// Integration tests: connecting to the IMAP server and basic login/mailbox
// selection.

import (
	"testing"
)

// TestIntegration_ConnectLoginSelect verifies that imapproc.Run can connect,
// log in, and select the mailbox without error when no messages are present.
func TestIntegration_ConnectLoginSelect(t *testing.T) {
	_, _, addr := newTestServer(t)
	script := writeTempScript(t, "exit 0")

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce returned unexpected error: %v", err)
	}
}

// TestIntegration_EmptyMailbox verifies that running against an empty mailbox
// succeeds without crashing.
func TestIntegration_EmptyMailbox(t *testing.T) {
	_, _, addr := newTestServer(t)
	script := writeTempScript(t, "exit 0")

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce on empty mailbox: %v", err)
	}
}
