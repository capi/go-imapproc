// imapproc connects to an IMAP server, processes unread emails by invoking
// an external program for each one, and on success either marks them as read
// or deletes them (configurable). It uses IMAP IDLE to wait for new messages
// and runs until Ctrl-C is received.
package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

func main() {
	cfg, configPath, err := parseConfig(os.Args[1:], os.Stderr)
	if err != nil {
		// parseConfig already wrote usage/error details to stderr.
		os.Exit(1)
	}

	if configPath != "" {
		log.Printf("using config file: %s", configPath)
	}
	r := cfg.redacted()
	log.Printf("config: addr=%s user=%s mailbox=%s exec=%s on_success=%s only_new=%v once=%v password=%s",
		r.Addr, r.User, r.Mailbox, r.Exec, r.OnSuccess, r.OnlyNew, r.Once, r.Pass)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

// parseConfig builds the effective Config from args and any config file found.
// It returns the config, the path of the config file that was loaded (empty if
// none), and an error if the config is incomplete or --help was requested.
// Any usage/error messages are written to w.
func parseConfig(args []string, w io.Writer) (*Config, string, error) {
	fs := pflag.NewFlagSet("imapproc", pflag.ContinueOnError)
	fs.SetOutput(w)

	configFile := fs.String("config", "", "Path to config file (default: imapproc.yaml, ~/.imapproc.yaml, /etc/imapproc/config.yaml)")
	addr := fs.String("addr", "", "IMAP server address (e.g. imap.gmail.com:993)")
	user := fs.String("user", "", "IMAP username")
	pass := fs.String("pass", "", "IMAP password")
	mailbox := fs.String("mailbox", "", "Mailbox to monitor (default: INBOX)")
	execProg := fs.String("exec", "", "Program to run for each unread message (receives raw email on stdin)")
	onSuccess := fs.String("on-success", "", `Action on successful processing: "seen" (default) or "delete"`)
	help := fs.Bool("help", false, "Show this help text")
	once := fs.Bool("once", false, "Process all unread messages once and exit (skip IDLE)")
	onlyNew := fs.Bool("only-new", false, "Skip existing unread messages; only process messages that arrive via IMAP IDLE after startup")

	fs.Usage = func() {
		fmt.Fprintf(w, "Usage: imapproc [flags] [program [args...]]\n\n")
		fmt.Fprintf(w, "  Positional program and args override --exec.\n\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}

	if *help {
		fs.Usage()
		return nil, "", fmt.Errorf("help requested")
	}

	cfg, configPath, err := findAndLoadConfig(*configFile)
	if err != nil {
		fmt.Fprintln(w, err)
		return nil, "", err
	}

	// CLI flags override config file values when explicitly set.
	if *addr != "" {
		cfg.Addr = *addr
	}
	if *user != "" {
		cfg.User = *user
	}
	if *pass != "" {
		cfg.Pass = *pass
	}
	if *mailbox != "" {
		cfg.Mailbox = *mailbox
	}
	if *execProg != "" {
		cfg.Exec = *execProg
	}
	if *onSuccess != "" {
		cfg.OnSuccess = OnSuccessAction(*onSuccess)
	}
	// Boolean flags: only override the config file when explicitly set on the
	// command line (i.e. the flag was actually passed), so that a true value in
	// the config file is not silently clobbered by the flag's zero value.
	if fs.Changed("only-new") {
		cfg.OnlyNew = *onlyNew
	}
	if fs.Changed("once") {
		cfg.Once = *once
	}
	// Positional args override --exec.
	if fs.NArg() > 0 {
		cfg.Exec = fs.Args()[0]
	}

	// Apply defaults after merging.
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.OnSuccess == "" {
		cfg.OnSuccess = OnSuccessSeen
	}

	if err := cfg.validate(); err != nil {
		// Only show usage hint when no config file was found; if a file was
		// found but is incomplete, just report the specific error.
		if configPath == "" {
			fs.Usage()
			fmt.Fprintln(w)
		}
		fmt.Fprintln(w, err)
		return nil, "", err
	}

	return cfg, configPath, nil
}

// run connects to the IMAP server, processes existing unread messages, then
// uses IDLE to wait for new ones until ctx is cancelled. When cfg.Once is true,
// it exits after the first processUnread pass without entering IDLE.
func run(ctx context.Context, cfg *Config) error {
	// newMail receives a value whenever the server pushes a notification during
	// IDLE that warrants a processUnread pass: either a new message has arrived
	// (EXISTS / NumMessages push) or an existing message has been marked unread
	// (unilateral FETCH FLAGS without \Seen). idle() listens on this channel
	// and sends DONE to break out of IDLE so the main loop can respond.
	newMail := make(chan struct{}, 1)

	// notify sends to newMail without blocking; safe to call from the handler.
	notify := func(reason string) {
		log.Printf("%s", reason)
		select {
		case newMail <- struct{}{}:
		default:
		}
	}

	options := &imapclient.Options{
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
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
				buf, err := msg.Collect()
				if err != nil {
					return
				}
				if buf.Flags == nil {
					// No flags in this push; nothing to act on.
					return
				}
				for _, f := range buf.Flags {
					if f == imap.FlagSeen {
						return // message is (still) read — ignore
					}
				}
				notify("message marked unread notification received")
			},
		},
	}

	log.Printf("connecting to %s", cfg.Addr)
	c, err := imapclient.DialTLS(cfg.Addr, options)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer c.Close()

	return runWithClient(ctx, c, cfg, newMail)
}

// runWithClient logs in, selects the configured mailbox, and then runs the
// process-idle loop using an already-connected (but not yet authenticated)
// IMAP client. Separating dial from logic enables integration tests to inject
// a plain-TCP in-process client without TLS. When cfg.Once is true, the
// function returns after the first processUnread pass without entering IDLE.
// newMail is a channel that receives a value whenever the server pushes a new
// message notification during IDLE; pass nil to disable IDLE wakeup (tests
// that cancel the context before IDLE don't need it).
func runWithClient(ctx context.Context, c *imapclient.Client, cfg *Config, newMail <-chan struct{}) error {
	if err := c.Login(cfg.User, cfg.Pass).Wait(); err != nil {
		return fmt.Errorf("login: %w", err)
	}
	log.Printf("logged in as %s", cfg.User)

	if _, err := c.Select(cfg.Mailbox, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", cfg.Mailbox, err)
	}

	program := cfg.Exec
	var programArgs []string

	// skipScan starts as true when OnlyNew is set, so the very first
	// processUnread pass is skipped. After the first IDLE wakeup (a new
	// message arrived) we clear it so subsequent passes process normally.
	skipScan := cfg.OnlyNew

	for {
		if !skipScan {
			if err := processUnread(c, program, programArgs, cfg.OnSuccess); err != nil {
				return err
			}
		}
		skipScan = false

		if cfg.Once || ctx.Err() != nil {
			return nil
		}

		if err := idle(ctx, c, newMail); err != nil {
			return err
		}

		if ctx.Err() != nil {
			return nil
		}
	}
}

// processUnread searches for all unread messages in the selected mailbox and
// invokes the external program for each one.
func processUnread(c *imapclient.Client, program string, programArgs []string, onSuccess OnSuccessAction) error {
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
		if err := processMessage(c, uid, program, programArgs, onSuccess); err != nil {
			// Log and continue; a single message failure should not abort the run.
			log.Printf("error processing UID %d: %v", uid, err)
		}
	}
	return nil
}

// processMessage fetches the raw RFC822 content of a message, pipes it to the
// external program, and on success performs the configured OnSuccess action
// (mark as \Seen or delete).
func processMessage(c *imapclient.Client, uid imap.UID, program string, programArgs []string, onSuccess OnSuccessAction) error {
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

// idle starts IMAP IDLE and blocks until the server sends a notification
// (e.g. new mail) or ctx is cancelled. newMail, when non-nil, is a channel
// that the UnilateralDataHandler signals on new EXISTS pushes; idle() sends
// DONE and returns so the main loop can call processUnread.
func idle(ctx context.Context, c *imapclient.Client, newMail <-chan struct{}) error {
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
	case <-newMail:
		// A new message arrived via unilateral EXISTS push. Send DONE so the
		// server ends IDLE, then let the main loop call processUnread.
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
