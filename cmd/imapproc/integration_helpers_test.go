package main

// Integration test helpers: in-process IMAP server setup, message injection,
// client dialling, and assertion utilities shared across all scenario files.

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/capi/go-imapproc/internal/imapproc"
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

// appendMessage adds a raw RFC 5322 message to the user's mailbox. Pass
// imap.FlagSeen in flags to pre-mark the message as read.
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

// runOnce calls imapproc.Run with a cancelled context so that it processes all
// current unread messages and returns without entering IDLE.
func runOnce(t *testing.T, addr, mailbox, program string) error {
	t.Helper()
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: mailbox,
		Exec:    program,
	}
	return runOnceCfg(t, addr, cfg)
}

// runOnceCfg is like runOnce but accepts a full imapproc.Config for tests that
// need to set fields beyond the basic connection parameters (e.g. OnSuccess).
func runOnceCfg(t *testing.T, addr string, cfg imapproc.Config) error {
	t.Helper()
	c := dialInsecure(t, addr)

	// Cancel immediately so the loop exits after the first ProcessUnread pass
	// without blocking in IDLE.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return imapproc.Run(ctx, c, cfg, nil)
}

// countUnread opens a fresh client connection and returns the number of
// messages in the mailbox that do NOT have \Seen.
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

// countMessages opens a fresh client connection and returns the total number
// of messages in the mailbox (regardless of \Seen flag).
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
