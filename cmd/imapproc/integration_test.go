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
	c := dialInsecure(t, addr)

	cfg := &Config{
		Addr:    addr,
		User:    testUser,
		Pass:    testPass,
		Mailbox: mailbox,
		Exec:    program,
	}

	// Cancel immediately so the loop exits after the first processUnread pass
	// without blocking in IDLE.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return runWithClient(ctx, c, cfg)
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
	if err := runWithClient(ctx, c, cfg); err != nil {
		t.Fatalf("unexpected error on cancelled context: %v", err)
	}
}
