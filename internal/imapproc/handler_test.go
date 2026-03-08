package imapproc_test

// Unit tests for the UnilateralDataHandler returned by NewUnilateralDataHandler,
// and for exported constants. These tests exercise the handler logic that can
// be driven without a live IMAP connection.
//
// Flag-change and Fetch-based wakeup scenarios require a live IMAP push and
// are covered by the integration tests in cmd/imapproc.

import (
	"testing"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// drainNewMail returns the number of items currently buffered in newMail
// without blocking.
func drainNewMail(ch <-chan struct{}) int {
	n := 0
	for {
		select {
		case <-ch:
			n++
		default:
			return n
		}
	}
}

// TestNewUnilateralDataHandler_MailboxNumMessages verifies that a Mailbox push
// with NumMessages set signals newMail exactly once.
func TestNewUnilateralDataHandler_MailboxNumMessages(t *testing.T) {
	newMail := make(chan struct{}, 1)
	handler, _ := imapproc.NewUnilateralDataHandler(newMail)

	numMsgs := uint32(5)
	handler.Mailbox(&imapclient.UnilateralDataMailbox{NumMessages: &numMsgs})

	if n := drainNewMail(newMail); n != 1 {
		t.Errorf("expected 1 signal, got %d", n)
	}
}

// TestNewUnilateralDataHandler_MailboxSignalDroppedWhenFull verifies that
// when the newMail channel is already full, additional Mailbox pushes do not
// block or panic — the signal is dropped (channel already has a pending wake).
func TestNewUnilateralDataHandler_MailboxSignalDroppedWhenFull(t *testing.T) {
	newMail := make(chan struct{}, 1)
	handler, _ := imapproc.NewUnilateralDataHandler(newMail)

	numMsgs := uint32(5)
	// Fill the channel.
	handler.Mailbox(&imapclient.UnilateralDataMailbox{NumMessages: &numMsgs})
	// These two should drop silently (channel is at capacity).
	handler.Mailbox(&imapclient.UnilateralDataMailbox{NumMessages: &numMsgs})
	handler.Mailbox(&imapclient.UnilateralDataMailbox{NumMessages: &numMsgs})

	// Only one signal should be buffered.
	if n := drainNewMail(newMail); n != 1 {
		t.Errorf("expected exactly 1 buffered signal, got %d", n)
	}
}

// TestNewUnilateralDataHandler_MailboxNoNumMessages verifies that a Mailbox
// push without NumMessages does NOT signal newMail.
func TestNewUnilateralDataHandler_MailboxNoNumMessages(t *testing.T) {
	newMail := make(chan struct{}, 1)
	handler, _ := imapproc.NewUnilateralDataHandler(newMail)

	handler.Mailbox(&imapclient.UnilateralDataMailbox{}) // NumMessages == nil

	if n := drainNewMail(newMail); n != 0 {
		t.Errorf("expected 0 signals, got %d", n)
	}
}

// TestOnSuccessActionConstants verifies the string values of the exported
// constants match the expected configuration file strings.
func TestOnSuccessActionConstants(t *testing.T) {
	tests := []struct {
		action imapproc.OnSuccessAction
		want   string
	}{
		{imapproc.OnSuccessSeen, "seen"},
		{imapproc.OnSuccessDelete, "delete"},
	}
	for _, tt := range tests {
		if string(tt.action) != tt.want {
			t.Errorf("OnSuccessAction %q: string representation = %q, want %q",
				tt.action, string(tt.action), tt.want)
		}
	}
}
