package imapproc

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// NewUnilateralDataHandler returns an imapclient.UnilateralDataHandler that
// signals newMail whenever the server pushes a notification that warrants a
// processUnread pass:
//   - A new message has arrived (EXISTS / NumMessages push).
//   - An existing message has been marked unread (unilateral FETCH FLAGS without \Seen).
//
// The returned notify function can be used in tests or extended handlers.
func NewUnilateralDataHandler(newMail chan<- struct{}) (*imapclient.UnilateralDataHandler, func(reason string)) {
	notify := func(reason string) {
		log.Printf("%s", reason)
		select {
		case newMail <- struct{}{}:
		default:
		}
	}

	handler := &imapclient.UnilateralDataHandler{
		// Mailbox is called when the server pushes a mailbox status update,
		// such as a new message arriving during IDLE.
		Mailbox: func(data *imapclient.UnilateralDataMailbox) {
			if data.NumMessages != nil {
				notify("new message notification received")
			}
		},
		// Fetch is called when the server pushes an unsolicited FETCH
		// response, typically to report flag changes on an existing message.
		// If the updated flags do not include \Seen the message is (now)
		// unread and we should process it.
		Fetch: func(msg *imapclient.FetchMessageData) {
			// Iterate items directly so we can distinguish "FLAGS item
			// present but empty" (message has no flags → unread) from
			// "no FLAGS item in response" (nothing to act on).
			// Using Collect() loses this distinction because both cases
			// result in buf.Flags == nil.
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
			if !flagsFound {
				// No FLAGS item in this push; nothing to act on.
				return
			}
			if seen {
				return // message is (still) read — ignore
			}
			notify("message marked unread notification received")
		},
	}

	return handler, notify
}

// Idle starts IMAP IDLE and blocks until a reason to wake occurs:
//   - ctx is cancelled (shutdown).
//   - newMail receives a value (new mail or flag-change notification).
//   - refreshInterval elapses (periodic keepalive: send DONE, return so the
//     caller re-enters Idle immediately).
//   - The server terminates IDLE on its own.
//
// onEntered, if non-nil, is called each time IDLE is successfully started.
// This hook exists for tests that need to count IDLE cycles.
func Idle(ctx context.Context, c *imapclient.Client, newMail <-chan struct{}, refreshInterval time.Duration, onEntered func()) error {
	log.Printf("entering IDLE")
	idleCmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("idle: %w", err)
	}
	if onEntered != nil {
		onEntered()
	}

	done := make(chan error, 1)
	go func() { done <- idleCmd.Wait() }()

	refresh := time.NewTimer(refreshInterval)
	defer refresh.Stop()

	select {
	case <-ctx.Done():
		log.Printf("stopping IDLE (shutdown)")
		if err := idleCmd.Close(); err != nil {
			return fmt.Errorf("idle close: %w", err)
		}
		<-done
		return nil
	case <-newMail:
		// A new message arrived via unilateral EXISTS push. Send DONE so the
		// server ends IDLE, then let the main loop call ProcessUnread.
		if err := idleCmd.Close(); err != nil {
			return fmt.Errorf("idle close: %w", err)
		}
		<-done
		return nil
	case <-refresh.C:
		// Periodic refresh: send DONE and return so the caller re-enters Idle.
		// This prevents the server from dropping the connection after its own
		// timeout (RFC 2177 recommends servers allow at least 30 minutes).
		log.Printf("refreshing IDLE")
		if err := idleCmd.Close(); err != nil {
			return fmt.Errorf("idle close: %w", err)
		}
		<-done
		return nil
	case err := <-done:
		// Server terminated IDLE (e.g. timeout).
		if err != nil {
			return fmt.Errorf("idle: %w", err)
		}
		return nil
	}
}
