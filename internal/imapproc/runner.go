package imapproc

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"
)

// DefaultIdleRefreshInterval is the interval at which IMAP IDLE is
// periodically refreshed (DONE + re-IDLE). RFC 2177 notes that servers may
// drop an IDLE connection after 30 minutes; refreshing well before that limit
// keeps the connection alive.
const DefaultIdleRefreshInterval = 25 * time.Minute

// Config holds all runtime settings used by the run loop. It mirrors the
// fields from the CLI config that the imapproc package needs at runtime.
type Config struct {
	User      string
	Pass      string
	Mailbox   string
	Exec      string
	OnSuccess OnSuccessAction
	OnlyNew   bool
	Once      bool

	// IdleRefreshInterval is how often the IDLE command is refreshed by
	// sending DONE and immediately re-issuing IDLE. A zero value uses
	// DefaultIdleRefreshInterval.
	IdleRefreshInterval time.Duration

	// OnIdleEntered is an optional callback invoked each time IDLE is entered.
	// It is intended for tests that need to observe the IDLE refresh cycle.
	OnIdleEntered func()
}

// Run logs in, selects the configured mailbox, and then runs the
// process-idle loop using an already-connected (but not yet authenticated)
// IMAP client. Separating dial from logic enables integration tests to inject
// a plain-TCP in-process client without TLS.
//
// When cfg.Once is true, Run returns after the first ProcessUnread pass
// without entering IDLE.
//
// newMail is a channel that receives a value whenever the server pushes a new
// message notification during IDLE; pass nil to disable IDLE wakeup (tests
// that cancel the context before IDLE don't need it).
func Run(ctx context.Context, c *imapclient.Client, cfg Config, newMail <-chan struct{}) error {
	if err := c.Login(cfg.User, cfg.Pass).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	log.Printf("logged in as %s", cfg.User)

	if _, err := c.Select(cfg.Mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", cfg.Mailbox, err)
	}

	program := cfg.Exec
	var programArgs []string

	refreshInterval := cfg.IdleRefreshInterval
	if refreshInterval <= 0 {
		refreshInterval = DefaultIdleRefreshInterval
	}

	// skipScan starts as true when OnlyNew is set, so the very first
	// ProcessUnread pass is skipped. After the first IDLE wakeup (a new
	// message arrived) we clear it so subsequent passes process normally.
	skipScan := cfg.OnlyNew

	for {
		if !skipScan {
			if err := ProcessUnread(c, program, programArgs, cfg.OnSuccess); err != nil {
				return err
			}
		}
		skipScan = false

		if cfg.Once || ctx.Err() != nil {
			return nil
		}

		if err := Idle(ctx, c, newMail, refreshInterval, cfg.OnIdleEntered); err != nil {
			return err
		}

		if ctx.Err() != nil {
			return nil
		}
	}
}
