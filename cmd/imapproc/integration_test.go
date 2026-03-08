package main

// Integration tests for imapproc core behavior using an in-process IMAP server
// (github.com/emersion/go-imap/v2 imapserver + imapmemserver). No TLS, no
// external processes or network services required.
//
// Scenarios covered:
//   - Connecting and logging in to the IMAP server
//   - Only unread (NOT \Seen) messages are processed; already-read messages
//     are skipped
//   - A message is marked \Seen after the external program exits with code 0
//   - A message is NOT marked \Seen when the external program exits non-zero
//   - OnSuccessDelete: message is deleted after successful processing
//   - OnSuccessDelete: message is NOT deleted when program exits non-zero

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
)

const (
	testUser     = "testuser"
	testPass     = "testpass"
	testMailbox  = "INBOX"
	testRawEmail = "From: sender@example.com\r\nTo: testuser@example.com\r\nSubject: Test\r\n\r\nHello, world!\r\n"
)

// newTestServer starts an in-process, plain-TCP IMAP server backed by
// imapmemserver. It returns the server, the pre-created user (so tests can
// append messages directly), and the listener address (host:port). The server
// is shut down via t.Cleanup.
func newTestServer(t *testing.T) (srv *imapserver.Server, user *imapmemserver.User, addr string) {
	t.Helper()

	memSrv := imapmemserver.New()
	user = imapmemserver.NewUser(testUser, testPass)
	if err := user.Create(testMailbox, nil); err != nil {
		t.Fatalf("create mailbox: %v", err)
	}
	memSrv.AddUser(user)

	srv = imapserver.New(&imapserver.Options{
		NewSession: func(_ *imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return memSrv.NewSession(), nil, nil
		},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
		},
		InsecureAuth: true, // allow LOGIN without TLS
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr = ln.Addr().String()

	go func() {
		// Serve returns when Close() is called.
		_ = srv.Serve(ln)
	}()

	t.Cleanup(func() { srv.Close() })
	return srv, user, addr
}

// appendMessage adds a raw RFC 5322 message to the user's mailbox via the
// exported User.Append method. Pass imap.FlagSeen in flags to pre-mark as
// read.
func appendMessage(t *testing.T, user *imapmemserver.User, mailbox, raw string, flags ...imap.Flag) {
	t.Helper()
	opts := &imap.AppendOptions{Flags: flags}
	_, err := user.Append(mailbox, bytes.NewReader([]byte(raw)), opts)
	if err != nil {
		t.Fatalf("append message: %v", err)
	}
}

// dialInsecure connects an IMAP client to addr without TLS. The client is
// closed via t.Cleanup.
func dialInsecure(t *testing.T, addr string) *imapclient.Client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	c := imapclient.New(conn, nil)
	if err := c.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// countUnread opens a fresh client connection and returns the number of
// messages in the mailbox that do NOT have \Seen. This is used to assert
// state after runWithClient has finished.
func countUnread(t *testing.T, addr, mailbox string) int {
	t.Helper()
	c := dialInsecure(t, addr)
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		t.Fatalf("select: %v", err)
	}
	criteria := &imap.SearchCriteria{NotFlag: []imap.Flag{imap.FlagSeen}}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	return len(data.AllUIDs())
}

// writeTempScript creates an executable shell script in a temp dir and returns
// its path.
func writeTempScript(t *testing.T, script string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "handler.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// runOnce calls runWithClient with a cancelled context so that it processes
// all current unread messages and returns without entering IDLE. The context
// is cancelled immediately after processUnread would have returned; the
// run-loop checks ctx.Err() before going into IDLE and exits cleanly.
func runOnce(t *testing.T, addr, mailbox, program string) error {
	t.Helper()
	cfg := &Config{
		Addr:    addr,
		User:    testUser,
		Pass:    testPass,
		Mailbox: mailbox,
		Exec:    program,
	}
	return runOnceCfg(t, addr, cfg)
}

// runOnceCfg is like runOnce but accepts a full Config for tests that need to
// set fields beyond the basic connection parameters (e.g. OnSuccess).
func runOnceCfg(t *testing.T, addr string, cfg *Config) error {
	t.Helper()
	c := dialInsecure(t, addr)

	// Cancel immediately so the loop exits after the first processUnread pass
	// without blocking in IDLE.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return runWithClient(ctx, c, cfg, nil)
}

// countMessages opens a fresh client connection and returns the total number
// of messages in the mailbox (regardless of \Seen flag). Used to verify
// that messages were deleted.
func countMessages(t *testing.T, addr, mailbox string) int {
	t.Helper()
	c := dialInsecure(t, addr)
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	data, err := c.Select(mailbox, nil).Wait()
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	return int(data.NumMessages)
}

// -----------------------------------------------------------------------------
// Tests
// -----------------------------------------------------------------------------

// TestIntegration_ConnectLoginSelect verifies that runWithClient can connect,
// log in, and select the mailbox without error when no messages are present.
func TestIntegration_ConnectLoginSelect(t *testing.T) {
	_, _, addr := newTestServer(t)
	script := writeTempScript(t, "exit 0")

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce returned unexpected error: %v", err)
	}
}

// TestIntegration_OnlyUnreadProcessed verifies that only messages without
// \Seen are passed to the external program. A pre-read message must not be
// processed; its \Seen flag must remain set.
func TestIntegration_OnlyUnreadProcessed(t *testing.T) {
	_, user, addr := newTestServer(t)

	// Seed: one unread and one already-read message.
	appendMessage(t, user, testMailbox, testRawEmail)                // unread
	appendMessage(t, user, testMailbox, testRawEmail, imap.FlagSeen) // already read

	// Capture stdin to verify only one message is delivered.
	captureDir := t.TempDir()
	captureFile := filepath.Join(captureDir, "captured.txt")
	// Script appends its stdin to a file so we can count invocations.
	script := writeTempScript(t, "cat >> "+captureFile)

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce: %v", err)
	}

	// The capture file should exist and contain exactly one copy of the email.
	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	count := strings.Count(string(got), "Hello, world!")
	if count != 1 {
		t.Errorf("expected exactly 1 message delivered to program, body contained %d occurrences", count)
	}

	// The pre-read message must still be read (unread count == 0 after run).
	// processUnread should have marked the originally-unread one as seen too.
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("unread count after run = %d, want 0", n)
	}
}

// TestIntegration_MarkedReadOnSuccess verifies that a message receives the
// \Seen flag when the external program exits with code 0.
func TestIntegration_MarkedReadOnSuccess(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail) // unread

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
	appendMessage(t, user, testMailbox, testRawEmail) // unread

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
//
// To vary program behavior per-message we use a script that exits 1 if its
// stdin contains a specific marker, and 0 otherwise.
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

// TestIntegration_EmptyMailbox verifies that running against an empty mailbox
// succeeds and does not crash.
func TestIntegration_EmptyMailbox(t *testing.T) {
	_, _, addr := newTestServer(t)
	script := writeTempScript(t, "exit 0")

	if err := runOnce(t, addr, testMailbox, script); err != nil {
		t.Fatalf("runOnce on empty mailbox: %v", err)
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

	wantSubstr := "Hello, world!"
	if !strings.Contains(string(got), wantSubstr) {
		t.Errorf("program did not receive full email body; got:\n%s", got)
	}
}

// TestIntegration_ContextCancelledBeforeIdle ensures runWithClient exits
// cleanly (no error) when the context is already cancelled, without entering
// IDLE.
func TestIntegration_ContextCancelledBeforeIdle(t *testing.T) {
	_, _, addr := newTestServer(t)
	script := writeTempScript(t, "exit 0")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cancel() // cancel immediately

	c := dialInsecure(t, addr)
	cfg := &Config{
		Addr:    addr,
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    script,
	}
	if err := runWithClient(ctx, c, cfg, nil); err != nil {
		t.Fatalf("unexpected error on cancelled context: %v", err)
	}
}

// TestIntegration_DeleteOnSuccess verifies that when OnSuccess is set to
// OnSuccessDelete, a successfully processed message is deleted from the
// mailbox.
func TestIntegration_DeleteOnSuccess(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail) // unread

	script := writeTempScript(t, "exit 0")

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessDelete,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// The message must have been deleted.
	if n := countMessages(t, addr, testMailbox); n != 0 {
		t.Errorf("expected message to be deleted, but mailbox contains %d message(s)", n)
	}
}

// TestIntegration_DeleteNotPerformedOnFailure verifies that when OnSuccess is
// set to OnSuccessDelete, a message whose program exits non-zero is NOT deleted.
func TestIntegration_DeleteNotPerformedOnFailure(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail) // unread

	script := writeTempScript(t, "exit 1")

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessDelete,
	}
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// The message must still be present.
	if n := countMessages(t, addr, testMailbox); n != 1 {
		t.Errorf("expected message to remain, but mailbox contains %d message(s)", n)
	}
}

// TestIntegration_MarkSeenOnSuccess_ExplicitConfig verifies that the default
// OnSuccessSeen action still marks messages as \Seen (regression guard).
func TestIntegration_MarkSeenOnSuccess_ExplicitConfig(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail) // unread

	script := writeTempScript(t, "exit 0")

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
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

// TestIntegration_OnceFlag verifies that when cfg.Once is true,
// runWithClient processes all unread messages and returns without blocking in
// IDLE, even when the context is not cancelled.
func TestIntegration_OnceFlag(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail) // one unread message

	script := writeTempScript(t, "exit 0")

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
		Once:      true,
	}

	c := dialInsecure(t, addr)
	// Use a background context (not cancelled) to prove that cfg.Once=true
	// causes an early return without waiting for IDLE or context cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runWithClient(ctx, c, cfg, nil)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWithClient returned error: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("runWithClient did not return promptly with cfg.Once=true; timed out")
	}

	// The message must have been processed (marked read).
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread messages after --once run, got %d", n)
	}
}

// TestIntegration_OnlyNew_SkipsExistingUnread verifies that when OnlyNew is
// true, runWithClient skips the initial processUnread scan — pre-existing
// unread messages are not processed. The function should still enter IDLE and
// only process messages that arrive after startup.
func TestIntegration_OnlyNew_SkipsExistingUnread(t *testing.T) {
	_, user, addr := newTestServer(t)
	// Seed one unread message that was already present before startup.
	appendMessage(t, user, testMailbox, testRawEmail)

	captureDir := t.TempDir()
	captureFile := filepath.Join(captureDir, "captured.txt")
	script := writeTempScript(t, "cat >> "+captureFile)

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
		OnlyNew:   true,
	}

	// Run with a cancelled context so it exits without entering IDLE.
	// Because OnlyNew=true, the initial processUnread pass must be skipped.
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// The capture file must not have been created — no message was delivered
	// to the program.
	if _, err := os.Stat(captureFile); !os.IsNotExist(err) {
		t.Errorf("expected program not to be called, but capture file exists (err=%v)", err)
	}

	// The pre-existing unread message must remain unread.
	if n := countUnread(t, addr, testMailbox); n != 1 {
		t.Errorf("expected 1 unread (untouched) message, got %d", n)
	}
}

// TestIntegration_OnlyNew_ProcessesNewArrival verifies that when OnlyNew is
// true, messages that arrive after the initial IDLE entry ARE processed on the
// next processUnread pass (i.e. the skip-scan flag is only applied once).
//
// We simulate a "second pass" by running two sequential runWithClient calls:
//   - First call: OnlyNew=true, context already cancelled — verifies that the
//     pre-existing message is NOT processed.
//   - Second call: OnlyNew=false (default), context already cancelled — verifies
//     that the same pre-existing message IS now processed (as it would be on any
//     subsequent pass after IDLE wakes).
//
// This confirms that skipScan is only applied to the very first iteration.
func TestIntegration_OnlyNew_ProcessesNewArrival(t *testing.T) {
	_, user, addr := newTestServer(t)
	// Seed one unread message that represents a "pre-existing" email.
	appendMessage(t, user, testMailbox, testRawEmail)

	script := writeTempScript(t, "exit 0")

	// First run: OnlyNew=true — initial processUnread is skipped, message remains unread.
	cfgOnlyNew := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
		OnlyNew:   true,
	}
	if err := runOnceCfg(t, addr, cfgOnlyNew); err != nil {
		t.Fatalf("first run (OnlyNew=true): %v", err)
	}
	if n := countUnread(t, addr, testMailbox); n != 1 {
		t.Errorf("after OnlyNew=true run: expected 1 unread (skipped), got %d", n)
	}

	// Second run: OnlyNew=false — simulates what happens after IDLE wakes (skipScan
	// is false on all subsequent passes). The message must now be processed.
	cfgNormal := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
		OnlyNew:   false,
	}
	if err := runOnceCfg(t, addr, cfgNormal); err != nil {
		t.Fatalf("second run (OnlyNew=false): %v", err)
	}
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("after normal run: expected 0 unread, got %d", n)
	}
}

// TestIntegration_IdleWakesOnNewMail verifies that when the server pushes a
// unilateral EXISTS notification during IDLE, runWithClient wakes up, exits
// IDLE, processes the newly arrived message, and then re-enters IDLE.
//
// The test uses the newMail channel directly (bypassing TLS dial) to simulate
// what the UnilateralDataHandler does in production: it sends to newMail
// whenever the server pushes NumMessages during IDLE.
func TestIntegration_IdleWakesOnNewMail(t *testing.T) {
	_, user, addr := newTestServer(t)

	captureDir := t.TempDir()
	captureFile := filepath.Join(captureDir, "captured.txt")
	script := writeTempScript(t, "cat >> "+captureFile)

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
	}

	newMail := make(chan struct{}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := dialInsecure(t, addr)

	done := make(chan error, 1)
	go func() {
		done <- runWithClient(ctx, c, cfg, newMail)
	}()

	// Wait a moment for the initial processUnread pass to complete and for
	// runWithClient to enter IDLE (mailbox is empty so it enters IDLE quickly).
	time.Sleep(200 * time.Millisecond)

	// Deliver a new message to the mailbox to simulate an incoming email.
	appendMessage(t, user, testMailbox, testRawEmail)

	// Signal the newMail channel as the UnilateralDataHandler would.
	newMail <- struct{}{}

	// Allow time for IDLE to wake, processUnread to run, and the program to execute.
	time.Sleep(500 * time.Millisecond)

	// Cancel the context to shut down cleanly.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWithClient returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithClient did not return after context cancellation")
	}

	// The message must have been delivered to the program.
	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	if !strings.Contains(string(got), "Hello, world!") {
		t.Errorf("expected program to receive new message, but capture file contents:\n%s", got)
	}

	// The message must have been marked as read.
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread after IDLE wakeup processing, got %d", n)
	}
}

// TestIntegration_IdleWakesOnFlagChange verifies that when a message is marked
// unread (\\Seen flag removed) during IDLE, the UnilateralDataHandler.Fetch
// callback fires, signals newMail, and runWithClient wakes up, exits IDLE, and
// processes the now-unread message.
//
// Unlike TestIntegration_IdleWakesOnNewMail this test wires the real
// UnilateralDataHandler (same as run() does) into the client so that the
// unilateral FETCH push from the server actually triggers the notify path,
// including the case where FLAGS () is sent (empty flag list, i.e. all flags
// removed). The test does NOT manually signal newMail.
func TestIntegration_IdleWakesOnFlagChange(t *testing.T) {
	_, user, addr := newTestServer(t)

	// Seed a message that is already read.
	appendMessage(t, user, testMailbox, testRawEmail, imap.FlagSeen)

	captureDir := t.TempDir()
	captureFile := filepath.Join(captureDir, "captured.txt")
	script := writeTempScript(t, "cat >> "+captureFile)

	cfg := &Config{
		Addr:      addr,
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      script,
		OnSuccess: OnSuccessSeen,
	}

	// Build the same newMail channel + UnilateralDataHandler that run() uses,
	// so the test exercises the real notification path end-to-end.
	newMail := make(chan struct{}, 1)
	notify := func() {
		select {
		case newMail <- struct{}{}:
		default:
		}
	}
	options := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					notify()
				}
			},
			Fetch: func(msg *imapclient.FetchMessageData) {
				flagsFound := false
				seen := false
				for {
					item := msg.Next()
					if item == nil {
						break
					}
					if flagData, ok := item.(imapclient.FetchItemDataFlags); ok {
						flagsFound = true
						for _, f := range flagData.Flags {
							if f == imap.FlagSeen {
								seen = true
								break
							}
						}
					}
				}
				if flagsFound && !seen {
					notify()
				}
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Dial with the handler options so unilateral FETCH pushes are received.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := imapclient.New(conn, options)
	if err := c.WaitGreeting(); err != nil {
		t.Fatalf("WaitGreeting: %v", err)
	}
	t.Cleanup(func() { c.Close() })

	done := make(chan error, 1)
	go func() {
		done <- runWithClient(ctx, c, cfg, newMail)
	}()

	// Wait for the initial processUnread pass (no unread → enters IDLE quickly).
	time.Sleep(200 * time.Millisecond)

	// Mark the message as unread via a separate client, simulating Thunderbird
	// (or any client) removing the \Seen flag. The server will push an
	// unsolicited FETCH FLAGS response to c, which the handler above will
	// convert into a newMail signal.
	func() {
		c2 := dialInsecure(t, addr)
		if err := c2.Login(testUser, testPass).Wait(); err != nil {
			t.Fatalf("c2 login: %v", err)
		}
		if _, err := c2.Select(testMailbox, nil).Wait(); err != nil {
			t.Fatalf("c2 select: %v", err)
		}
		criteria := &imap.SearchCriteria{Flag: []imap.Flag{imap.FlagSeen}}
		data, err := c2.UIDSearch(criteria, nil).Wait()
		if err != nil {
			t.Fatalf("c2 search: %v", err)
		}
		if len(data.AllUIDs()) == 0 {
			t.Fatal("c2: no seen message to unmark")
		}
		uidSet := imap.UIDSetNum(data.AllUIDs()[0])
		storeFlags := &imap.StoreFlags{
			Op:    imap.StoreFlagsDel,
			Flags: []imap.Flag{imap.FlagSeen},
		}
		if err := c2.Store(uidSet, storeFlags, nil).Close(); err != nil {
			t.Fatalf("c2 store: %v", err)
		}
	}()

	// Allow time for the unilateral FETCH push to arrive, the handler to fire,
	// IDLE to wake, and processUnread to run.
	time.Sleep(500 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runWithClient returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runWithClient did not return after context cancellation")
	}

	// The now-unread message must have been delivered to the program.
	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	if !strings.Contains(string(got), "Hello, world!") {
		t.Errorf("expected program to receive re-unread message, capture file:\n%s", got)
	}

	// After processing, the message must be marked read again.
	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread after flag-change wakeup processing, got %d", n)
	}
}
