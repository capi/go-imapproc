// imapproc connects to an IMAP server, processes unread emails by invoking
// an external program for each one, and marks them as read on success.
// It uses IMAP IDLE to wait for new messages and runs until Ctrl-C is received.
package main

import (
	"context"
	"fmt"
	flag "github.com/spf13/pflag"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func main() {
	addr := flag.String("addr", "", "IMAP server address (e.g. imap.gmail.com:993)")
	user := flag.String("user", "", "IMAP username")
	pass := flag.String("pass", "", "IMAP password")
	mailbox := flag.String("mailbox", "INBOX", "Mailbox to monitor")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s --addr <host:port> --user <user> --pass <pass> <program> [args...]\n\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *addr == "" || *user == "" || *pass == "" || flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	program := flag.Args()[0]
	programArgs := flag.Args()[1:]

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *addr, *user, *pass, *mailbox, program, programArgs); err != nil {
		log.Fatal(err)
	}
}

// run connects to the IMAP server, processes existing unread messages, then
// uses IDLE to wait for new ones until ctx is cancelled.
func run(ctx context.Context, addr, user, pass, mailbox, program string, programArgs []string) error {
	options := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			// Mailbox is called when the server pushes a mailbox status update,
			// such as a new message arriving during IDLE.
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				if data.NumMessages != nil {
					log.Printf("new message notification received")
				}
			},
		},
	}

	log.Printf("connecting to %s", addr)
	c, err := imapclient.DialTLS(addr, options)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()

	if err := c.Login(user, pass).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	log.Printf("logged in as %s", user)

	if _, err := c.Select(mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", mailbox, err)
	}

	for {
		if err := processUnread(c, program, programArgs); err != nil {
			return err
		}

		if ctx.Err() != nil {
			return nil
		}

		if err := idle(ctx, c); err != nil {
			return err
		}

		if ctx.Err() != nil {
			return nil
		}
	}
}

// processUnread searches for all unread messages in the selected mailbox and
// invokes the external program for each one.
func processUnread(c *imapclient.Client, program string, programArgs []string) error {
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
		return nil
	}
	log.Printf("found %d unread message(s)", len(uids))

	for _, uid := range uids {
		if err := processMessage(c, uid, program, programArgs); err != nil {
			// Log and continue; a single message failure should not abort the run.
			log.Printf("error processing UID %d: %v", uid, err)
		}
	}
	return nil
}

// processMessage fetches the raw RFC822 content of a message, pipes it to the
// external program, and marks it as read if the program exits with code 0.
func processMessage(c *imapclient.Client, uid imap.UID, program string, programArgs []string) error {
	uidSet := imap.UIDSetNum(uid)
	bodySection := &imap.FetchItemBodySection{}
	fetchOptions := &imap.FetchOptions{
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}

	fetchCmd := c.Fetch(uidSet, fetchOptions)
	defer fetchCmd.Close()

	msg := fetchCmd.Next()
	if msg == nil {
		return fmt.Errorf("UID %d: no message returned by FETCH", uid)
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
		return fmt.Errorf("UID %d: no body section in FETCH response", uid)
	}

	// Invoke the external program with the raw email on stdin.
	cmd := exec.Command(program, programArgs...)
	cmd.Stdin = bodyData.Literal
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	log.Printf("processing UID %d: running %s", uid, program)
	runErr := cmd.Run()

	// Drain the remaining fetch response regardless of program outcome.
	if err := fetchCmd.Close(); err != nil {
		log.Printf("UID %d: fetch close: %v", uid, err)
	}

	if runErr != nil {
		log.Printf("UID %d: program exited with error: %v, skipping", uid, runErr)
		return nil
	}

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
	return nil
}

// idle starts IMAP IDLE and blocks until the server sends a notification
// (e.g. new mail) or ctx is cancelled.
func idle(ctx context.Context, c *imapclient.Client) error {
	log.Printf("entering IDLE")
	idleCmd, err := c.Idle()
	if err != nil {
		return fmt.Errorf("idle: %w", err)
	}

	done := make(chan error, 1)
	go func() { done <- idleCmd.Wait() }()

	select {
	case <-ctx.Done():
		log.Printf("stopping IDLE (shutdown)")
		if err := idleCmd.Close(); err != nil {
			return fmt.Errorf("idle close: %w", err)
		}
		<-done
		return nil
	case err := <-done:
		// Server terminated IDLE (e.g. new mail notification).
		if err != nil {
			return fmt.Errorf("idle: %w", err)
		}
		return nil
	}
}
