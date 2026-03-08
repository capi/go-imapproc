package main

// Integration tests: IMAP IDLE behaviour — waking on new mail and on flag
// changes pushed by another client.

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"context"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// TestIntegration_IdleWakesOnNewMail verifies that when the server pushes a
// unilateral EXISTS notification during IDLE, imapproc.Run wakes up, exits
// IDLE, processes the newly arrived message, and then re-enters IDLE.
//
// The test signals the newMail channel directly (as the UnilateralDataHandler
// does in production) to avoid depending on real TLS connections in tests.
func TestIntegration_IdleWakesOnNewMail(t *testing.T) {
	_, user, addr := newTestServer(t)

	captureDir := t.TempDir()
	captureFile := captureDir + "/captured.txt"
	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "cat >> "+captureFile),
		OnSuccess: imapproc.OnSuccessSeen,
	}

	newMail := make(chan struct{}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	c := dialInsecure(t, addr)

	done := make(chan error, 1)
	go func() {
		done <- imapproc.Run(ctx, c, cfg, newMail)
	}()

	// Wait for the initial ProcessUnread pass to complete and for Run to enter
	// IDLE (mailbox is empty so this happens quickly).
	time.Sleep(200 * time.Millisecond)

	// Deliver a new message to simulate an incoming email.
	appendMessage(t, user, testMailbox, testRawEmail)

	// Signal newMail as the UnilateralDataHandler would.
	newMail <- struct{}{}

	// Allow time for IDLE to wake, ProcessUnread to run, and the script to execute.
	time.Sleep(500 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	if !strings.Contains(string(got), "Hello, world!") {
		t.Errorf("expected program to receive new message; capture file:\n%s", got)
	}

	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread after IDLE wakeup processing, got %d", n)
	}
}

// TestIntegration_IdleWakesOnFlagChange verifies that when a message is marked
// unread (\Seen flag removed) during IDLE, the UnilateralDataHandler.Fetch
// callback fires, signals newMail, and imapproc.Run wakes up and processes the
// now-unread message.
//
// This test wires the real UnilateralDataHandler (same as dial() does) into
// the client so that the unilateral FETCH push from the server triggers the
// notify path end-to-end — including the case where FLAGS () is sent.
func TestIntegration_IdleWakesOnFlagChange(t *testing.T) {
	_, user, addr := newTestServer(t)

	// Seed a message that is already read.
	appendMessage(t, user, testMailbox, testRawEmail, imap.FlagSeen)

	captureDir := t.TempDir()
	captureFile := captureDir + "/captured.txt"
	cfg := imapproc.Config{
		User:      testUser,
		Pass:      testPass,
		Mailbox:   testMailbox,
		Exec:      writeTempScript(t, "cat >> "+captureFile),
		OnSuccess: imapproc.OnSuccessSeen,
	}

	// Build the same newMail channel + UnilateralDataHandler that dial() uses.
	newMail := make(chan struct{}, 1)
	handler, _ := imapproc.NewUnilateralDataHandler(newMail)
	options := &imapclient.Options{UnilateralDataHandler: handler}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Dial with handler options so unilateral FETCH pushes are received.
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
		done <- imapproc.Run(ctx, c, cfg, newMail)
	}()

	// Wait for the initial ProcessUnread pass (no unread → enters IDLE quickly).
	time.Sleep(200 * time.Millisecond)

	// Mark the message as unread via a separate client, simulating another
	// mail client removing the \Seen flag. The server will push an unsolicited
	// FETCH FLAGS response to c, which the handler will convert to a newMail signal.
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
	// IDLE to wake, and ProcessUnread to run.
	time.Sleep(500 * time.Millisecond)

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}

	got, err := os.ReadFile(captureFile)
	if err != nil {
		t.Fatalf("reading capture file: %v", err)
	}
	if !strings.Contains(string(got), "Hello, world!") {
		t.Errorf("expected program to receive re-unread message; capture file:\n%s", got)
	}

	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread after flag-change wakeup processing, got %d", n)
	}
}
