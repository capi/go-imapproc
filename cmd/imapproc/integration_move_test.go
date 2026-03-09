package main

// Integration tests: OnSuccess=move behaviour.

import (
	"io"
	"testing"

	"github.com/emersion/go-imap/v2"

	"github.com/capi/go-imapproc/internal/imapproc"
)

const testTrashMailbox = "Trash"
const testArchiveMailbox = "Archive"

// TestIntegration_MoveOnSuccess verifies that when OnSuccess is
// OnSuccessMove, a successfully processed message is moved to the target
// mailbox and removed from the source mailbox.
func TestIntegration_MoveOnSuccess(t *testing.T) {
	_, user, addr := newTestServer(t)
	// Create the destination mailbox.
	if err := user.Create(testTrashMailbox, nil); err != nil {
		t.Fatalf("create trash mailbox: %v", err)
	}
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:       testUser,
		Pass:       testPass,
		Mailbox:    testMailbox,
		Exec:       writeTempScript(t, "exit 0"),
		OnSuccess:  imapproc.OnSuccessMove,
		MoveTarget: testTrashMailbox,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// Source mailbox must be empty.
	if n := countMessages(t, addr, testMailbox); n != 0 {
		t.Errorf("expected source mailbox to be empty after move, got %d message(s)", n)
	}
	// Target mailbox must contain the message.
	if n := countMessages(t, addr, testTrashMailbox); n != 1 {
		t.Errorf("expected target mailbox to contain 1 message after move, got %d", n)
	}
}

// TestIntegration_MoveNotPerformedOnFailure verifies that when OnSuccess is
// OnSuccessMove, a message whose program exits non-zero is NOT moved.
func TestIntegration_MoveNotPerformedOnFailure(t *testing.T) {
	_, user, addr := newTestServer(t)
	if err := user.Create(testTrashMailbox, nil); err != nil {
		t.Fatalf("create trash mailbox: %v", err)
	}
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:       testUser,
		Pass:       testPass,
		Mailbox:    testMailbox,
		Exec:       writeTempScript(t, "exit 1"),
		OnSuccess:  imapproc.OnSuccessMove,
		MoveTarget: testTrashMailbox,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// Source mailbox must still contain the message.
	if n := countMessages(t, addr, testMailbox); n != 1 {
		t.Errorf("expected message to remain in source mailbox on failure, got %d message(s)", n)
	}
	// Target mailbox must be empty.
	if n := countMessages(t, addr, testTrashMailbox); n != 0 {
		t.Errorf("expected target mailbox to be empty on failure, got %d message(s)", n)
	}
}

// TestIntegration_MoveToCustomFolder verifies that the move target can be
// configured to a mailbox other than Trash.
func TestIntegration_MoveToCustomFolder(t *testing.T) {
	_, user, addr := newTestServer(t)
	if err := user.Create(testArchiveMailbox, nil); err != nil {
		t.Fatalf("create archive mailbox: %v", err)
	}
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:       testUser,
		Pass:       testPass,
		Mailbox:    testMailbox,
		Exec:       writeTempScript(t, "exit 0"),
		OnSuccess:  imapproc.OnSuccessMove,
		MoveTarget: testArchiveMailbox,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	if n := countMessages(t, addr, testMailbox); n != 0 {
		t.Errorf("expected source mailbox to be empty, got %d message(s)", n)
	}
	if n := countMessages(t, addr, testArchiveMailbox); n != 1 {
		t.Errorf("expected Archive to contain 1 message, got %d", n)
	}
}

// TestParseConfig_OnSuccessMoveFromConfigFile verifies that on_success: move
// with an explicit on_success_target is parsed from the YAML config file.
func TestParseConfig_OnSuccessMoveFromConfigFile(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
on_success: move
on_success_target: Archive
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OnSuccess != imapproc.OnSuccessMove {
		t.Errorf("OnSuccess = %q, want %q", cfg.OnSuccess, imapproc.OnSuccessMove)
	}
	if cfg.OnSuccessTarget != testArchiveMailbox {
		t.Errorf("OnSuccessTarget = %q, want %q", cfg.OnSuccessTarget, testArchiveMailbox)
	}
}

// TestParseConfig_OnSuccessMoveDefaultTarget verifies that on_success: move
// without an explicit on_success_target defaults to "Trash".
func TestParseConfig_OnSuccessMoveDefaultTarget(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
on_success: move
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OnSuccess != imapproc.OnSuccessMove {
		t.Errorf("OnSuccess = %q, want %q", cfg.OnSuccess, imapproc.OnSuccessMove)
	}
	if cfg.OnSuccessTarget != "Trash" {
		t.Errorf("OnSuccessTarget = %q, want \"Trash\" (default)", cfg.OnSuccessTarget)
	}
}

// TestIntegration_MoveMarksSeenBeforeMove verifies that on a successful
// program run, the message is marked \Seen before the move is attempted, so
// that a failed move does not cause the message to be reprocessed. We test
// this indirectly: after a successful move the message in the destination
// mailbox must carry the \Seen flag.
func TestIntegration_MoveMarksSeenBeforeMove(t *testing.T) {
	_, user, addr := newTestServer(t)
	if err := user.Create(testTrashMailbox, nil); err != nil {
		t.Fatalf("create trash mailbox: %v", err)
	}
	appendMessage(t, user, testMailbox, testRawEmail)

	cfg := imapproc.Config{
		User:       testUser,
		Pass:       testPass,
		Mailbox:    testMailbox,
		Exec:       writeTempScript(t, "exit 0"),
		OnSuccess:  imapproc.OnSuccessMove,
		MoveTarget: testTrashMailbox,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// The message was moved; verify it carries \Seen in the destination so
	// that if it were ever re-evaluated it would not be reprocessed.
	c := dialInsecure(t, addr)
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.Select(testTrashMailbox, nil).Wait(); err != nil {
		t.Fatalf("select trash: %v", err)
	}
	criteria := &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(data.AllUIDs()) != 0 {
		t.Errorf("expected moved message to be \\Seen in destination, but found %d unread message(s)", len(data.AllUIDs()))
	}
}

// TestParseConfig_OnSuccessTargetIgnoredForNonMove verifies that
// on_success_target is ignored (no error) when on_success is not "move".
func TestParseConfig_OnSuccessTargetIgnoredForNonMove(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
on_success: seen
on_success_target: Archive
`)
	_, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v (on_success_target should be ignored when on_success != move)", err)
	}
}
