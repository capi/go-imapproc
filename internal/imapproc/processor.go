// Package imapproc contains the core logic for fetching, processing, and
// acting on IMAP messages. It is separate from the main package so that each
// unit of behaviour can be tested independently without a full CLI setup.
package imapproc

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// OnSuccessAction controls what happens to a message after it is successfully
// processed by the external program.
type OnSuccessAction string

const (
	// OnSuccessSeen marks the message as \Seen (read). This is the default.
	OnSuccessSeen OnSuccessAction = "seen"
	// OnSuccessDelete expunges the message from the mailbox.
	OnSuccessDelete OnSuccessAction = "delete"
	// OnSuccessMove moves the message to the configured target mailbox.
	// The target defaults to "Trash" when not specified.
	OnSuccessMove OnSuccessAction = "move"
)

// DefaultMoveTarget is the mailbox used when OnSuccessMove is configured but
// no explicit target folder is provided.
const DefaultMoveTarget = "Trash"

// ProcessUnread searches for all unread messages in the selected mailbox and
// invokes the external program for each one. stats may be nil (monitoring
// disabled), in which case no counters are updated.
func ProcessUnread(c *imapclient.Client, program string, programArgs []string, onSuccess OnSuccessAction, moveTarget string, stats *Stats) error {
	criteria := &imap.SearchCriteria{
		// NotFlag \Seen means "unread"
		NotFlag: []imap.Flag{imap.FlagSeen},
	}
	data, err := c.UIDSearch(criteria, nil).Wait()
	if err != nil {
		return fmt.Errorf("search: %w", err)
	}

	uids := data.AllUIDs()
	if len(uids) == 0 {
		log.Printf("no unread messages")
		if stats != nil {
			stats.SetLastPoll(PollSnapshot{Healthy: true, Time: timeNow()})
		}
		return nil
	}
	log.Printf("found %d unread message(s)", len(uids))

	var pollReceived, pollSuccess, pollFailed int64
	for _, uid := range uids {
		ok, err := processMessage(c, uid, program, programArgs, onSuccess, moveTarget, stats)
		if err != nil {
			// IMAP-level error: log and continue; a single message failure
			// should not abort the run.
			log.Printf("error processing UID %d: %v", uid, err)
			pollFailed++
		} else if ok {
			pollSuccess++
		} else {
			pollFailed++
		}
		pollReceived++
	}
	if stats != nil {
		stats.SetLastPoll(PollSnapshot{
			Received: pollReceived,
			Success:  pollSuccess,
			Failed:   pollFailed,
			Healthy:  pollFailed == 0,
			Time:     timeNow(),
		})
	}
	return nil
}

// timeNow is a package-level var so tests can override it.
var timeNow = func() time.Time { return time.Now() }

// processMessage fetches the raw RFC822 content of a message, pipes it to the
// external program, and on success performs the configured OnSuccess action
// (mark as \Seen, delete, or move). stats may be nil.
//
// Returns (true, nil) on program success, (false, nil) when the program exited
// with a non-zero status (skip, no IMAP action taken), and (false, err) on
// IMAP-level errors.
func processMessage(c *imapclient.Client, uid imap.UID, program string, programArgs []string, onSuccess OnSuccessAction, moveTarget string, stats *Stats) (bool, error) {
	uidSet := imap.UIDSetNum(uid)
	// Peek: true prevents the server from implicitly marking the message \Seen
	// on fetch. We set \Seen explicitly only after the external program exits
	// with code 0, which is the intended semantics.
	bodySection := &imap.FetchItemBodySection{Peek: true}
	fetchOptions := &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}

	fetchCmd := c.Fetch(uidSet, fetchOptions)
	defer fetchCmd.Close()

	msg := fetchCmd.Next()
	if msg == nil {
		return false, fmt.Errorf("UID %d: no message returned by FETCH", uid)
	}

	// Find the body section item in the streamed response.
	var bodyData imapclient.FetchItemDataBodySection
	found := false
	for {
		item := msg.Next()
		if item == nil {
			break
		}
		if bd, ok := item.(imapclient.FetchItemDataBodySection); ok {
			bodyData = bd
			found = true
			break
		}
	}
	if !found {
		return false, fmt.Errorf("UID %d: no body section in FETCH response", uid)
	}

	// Invoke the external program with the raw email on stdin.
	cmd := exec.Command(program, programArgs...)
	cmd.Stdin = bodyData.Literal
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("processing UID %d: running %s", uid, program)
	if stats != nil {
		stats.IncReceived()
	}
	runErr := cmd.Run()

	// Drain the remaining fetch response regardless of program outcome.
	if err := fetchCmd.Close(); err != nil {
		log.Printf("UID %d: fetch close: %v", uid, err)
	}

	if runErr != nil {
		log.Printf("UID %d: program exited with error: %v, skipping", uid, runErr)
		if stats != nil {
			stats.IncFailed()
		}
		return false, nil
	}

	if stats != nil {
		stats.IncSuccess()
	}
	return true, applyOnSuccess(c, uid, uidSet, onSuccess, moveTarget)
}

// applyOnSuccess performs the configured post-processing action on a message
// that was handled successfully.
func applyOnSuccess(c *imapclient.Client, uid imap.UID, uidSet imap.UIDSet, onSuccess OnSuccessAction, moveTarget string) error {
	switch onSuccess {
	case OnSuccessDelete:
		// Mark as \Deleted and then expunge.
		storeFlags := &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Flags:  []imap.Flag{imap.FlagDeleted},
			Silent: true,
		}
		if err := c.Store(uidSet, storeFlags, nil).Close(); err != nil {
			return fmt.Errorf("UID %d: mark as deleted: %w", uid, err)
		}
		if err := c.UIDExpunge(uidSet).Close(); err != nil {
			return fmt.Errorf("UID %d: expunge: %w", uid, err)
		}
		log.Printf("UID %d: deleted", uid)
	case OnSuccessMove:
		// Mark as \Seen first so that a failed move does not leave the message
		// unread in the source mailbox, which would cause it to be reprocessed
		// on the next pass.
		storeSeen := &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Flags:  []imap.Flag{imap.FlagSeen},
			Silent: true,
		}
		if err := c.Store(uidSet, storeSeen, nil).Close(); err != nil {
			return fmt.Errorf("UID %d: mark as read before move: %w", uid, err)
		}
		// Move the message to the target mailbox.
		target := moveTarget
		if target == "" {
			target = DefaultMoveTarget
		}
		if _, err := c.Move(uidSet, target).Wait(); err != nil {
			return fmt.Errorf("UID %d: move to %q: %w", uid, target, err)
		}
		log.Printf("UID %d: moved to %q", uid, target)
	default: // OnSuccessSeen
		// Mark as read (\Seen).
		storeFlags := &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Flags:  []imap.Flag{imap.FlagSeen},
			Silent: true,
		}
		if err := c.Store(uidSet, storeFlags, nil).Close(); err != nil {
			return fmt.Errorf("UID %d: mark as read: %w", uid, err)
		}
		log.Printf("UID %d: marked as read", uid)
	}
	return nil
}
