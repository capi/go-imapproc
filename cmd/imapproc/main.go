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
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// DefaultWebAddr is the listen address used when --web-enabled is set but
// --web-addr is not specified.
const DefaultWebAddr = ":8080"

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
	reconnectInitialDelay := r.ReconnectInitialDelay
	if reconnectInitialDelay == 0 {
		reconnectInitialDelay = imapproc.DefaultReconnectInitialDelay
	}
	reconnectMaxDelay := r.ReconnectMaxDelay
	if reconnectMaxDelay == 0 {
		reconnectMaxDelay = imapproc.DefaultReconnectMaxDelay
	}
	idleRefreshInterval := r.IdleRefreshInterval
	if idleRefreshInterval == 0 {
		idleRefreshInterval = imapproc.DefaultIdleRefreshInterval
	}
	log.Printf("config: addr=%s user=%s mailbox=%s exec=%s on_success=%s on_success_target=%s once=%v idle_refresh_interval=%s reconnect=%v reconnect_initial_delay=%s reconnect_max_delay=%s password=%s web_enabled=%v web_addr=%s instance_name=%s",
		r.Addr, r.User, r.Mailbox, r.Exec, r.OnSuccess, r.OnSuccessTarget, r.Once, idleRefreshInterval,
		r.Reconnect, reconnectInitialDelay, reconnectMaxDelay, r.Pass,
		r.WebEnabled, r.WebAddr, r.InstanceName)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := dial(ctx, cfg); err != nil {
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
	onSuccess := fs.String("on-success", "", `Action on successful processing: "seen" (default), "delete", or "move"`)
	onSuccessTarget := fs.String("on-success-target", "", `Destination mailbox when --on-success=move (default: "Trash")`)
	help := fs.Bool("help", false, "Show this help text")
	once := fs.Bool("once", false, "Process all unread messages once and exit (skip IDLE)")
	idleRefreshInterval := fs.Duration("idle-refresh-interval", 0, fmt.Sprintf("How often to refresh IMAP IDLE (default: %s); must be a Go duration string, e.g. 20m", imapproc.DefaultIdleRefreshInterval))
	reconnect := fs.Bool("reconnect", false, "Reconnect automatically when the connection is lost (default: false)")
	reconnectInitialDelay := fs.Duration("reconnect-initial-delay", 0, fmt.Sprintf("Initial backoff delay before first reconnect attempt (default: %s)", imapproc.DefaultReconnectInitialDelay))
	reconnectMaxDelay := fs.Duration("reconnect-max-delay", 0, fmt.Sprintf("Maximum backoff delay between reconnect attempts (default: %s)", imapproc.DefaultReconnectMaxDelay))
	webEnabled := fs.Bool("web-enabled", false, "Enable the HTTP monitoring server (dashboard + /api/health)")
	webAddr := fs.String("web-addr", "", fmt.Sprintf("Listen address for the HTTP monitoring server (default: %s)", DefaultWebAddr))
	name := fs.String("instance-name", "", "Optional name for this instance; shown in /api/health")

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
		cfg.OnSuccess = imapproc.OnSuccessAction(*onSuccess)
	}
	if *onSuccessTarget != "" {
		cfg.OnSuccessTarget = *onSuccessTarget
	}
	// Boolean flags: only override the config file when explicitly set on the
	// command line (i.e. the flag was actually passed), so that a true value in
	// the config file is not silently clobbered by the flag's zero value.
	if fs.Changed("once") {
		cfg.Once = *once
	}
	// Duration flag: only override when explicitly set, so that a non-zero
	// value from the config file is not silently replaced by 0.
	if fs.Changed("idle-refresh-interval") {
		cfg.IdleRefreshInterval = *idleRefreshInterval
	}
	// Reconnect flags: only override when explicitly set on the command line.
	if fs.Changed("reconnect") {
		cfg.Reconnect = *reconnect
	}
	if fs.Changed("reconnect-initial-delay") {
		cfg.ReconnectInitialDelay = *reconnectInitialDelay
	}
	if fs.Changed("reconnect-max-delay") {
		cfg.ReconnectMaxDelay = *reconnectMaxDelay
	}
	// Web monitoring flags: only override when explicitly set on the command line.
	if fs.Changed("web-enabled") {
		cfg.WebEnabled = *webEnabled
	}
	if *webAddr != "" {
		cfg.WebAddr = *webAddr
	}
	if *name != "" {
		cfg.InstanceName = *name
	}
	// Positional args override --exec.
	if fs.NArg() > 0 {
		cfg.Exec = fs.Args()[0]
		cfg.ExecArgs = fs.Args()[1:]
	}

	// Apply defaults after merging.
	if cfg.Mailbox == "" {
		cfg.Mailbox = "INBOX"
	}
	if cfg.OnSuccess == "" {
		cfg.OnSuccess = imapproc.OnSuccessSeen
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

// dial connects to the IMAP server over TLS and hands control to the run loop.
// When cfg.Reconnect is true it wraps the entire connect+run sequence in
// RunWithReconnect so that transient failures (unreachable server, login
// failure, dropped IDLE connection) are retried with exponential backoff.
func dial(ctx context.Context, cfg *Config) error {
	reconnectCfg := imapproc.ReconnectConfig{
		InitialDelay: cfg.ReconnectInitialDelay,
		MaxDelay:     cfg.ReconnectMaxDelay,
	}

	// Set up monitoring stats and optionally start the web server.
	var stats *imapproc.Stats
	if cfg.WebEnabled {
		stats = imapproc.NewStats()
		webAddr := cfg.WebAddr
		if webAddr == "" {
			webAddr = DefaultWebAddr
		}
		webErrCh := make(chan error, 1)
		go func() {
			webErrCh <- imapproc.ServeWeb(ctx, webAddr, stats, cfg.InstanceName)
		}()
		// Surface any immediate bind error before entering the IMAP loop.
		select {
		case err := <-webErrCh:
			if err != nil {
				return fmt.Errorf("web server: %w", err)
			}
		default:
		}
	}

	runConfig := cfg.toRunConfig(stats)

	attempt := func(ctx context.Context) error {
		newMail := make(chan struct{}, 1)
		handler, _ := imapproc.NewUnilateralDataHandler(newMail)

		options := &imapclient.Options{
			UnilateralDataHandler: handler,
		}

		log.Printf("connecting to %s", cfg.Addr)
		c, err := imapclient.DialTLS(cfg.Addr, options)
		if err != nil {
			return fmt.Errorf("dial: %w", err)
		}
		defer c.Close()

		return imapproc.Run(ctx, c, runConfig, newMail)
	}

	if cfg.Reconnect {
		return imapproc.RunWithReconnect(ctx, reconnectCfg, attempt)
	}
	return attempt(ctx)
}
