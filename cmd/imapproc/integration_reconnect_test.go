package main

// Integration tests: reconnect behaviour — automatic re-connection with
// exponential backoff when the IMAP server is temporarily unavailable.

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// dialInsecureConn wraps an existing net.Conn as an imapclient.Client without
// TLS, waiting for the server greeting. Used in reconnect tests that manage
// the connection lifetime explicitly.
func dialInsecureConn(conn net.Conn) (*imapclient.Client, error) {
	c := imapclient.New(conn, nil)
	if err := c.WaitGreeting(); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// TestIntegration_ReconnectAfterRunFailure verifies that RunWithReconnect
// retries the dial function when it returns an error, and processes mail on
// the second attempt.
func TestIntegration_ReconnectAfterRunFailure(t *testing.T) {
	_, user, addr := newTestServer(t)
	appendMessage(t, user, testMailbox, testRawEmail)

	script := writeTempScript(t, "exit 0")

	var connectCount atomic.Int32

	cfg := imapproc.ReconnectConfig{
		InitialDelay: 10 * time.Millisecond,
		MaxDelay:     50 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error {
		n := connectCount.Add(1)

		conn, dialErr := net.Dial("tcp", addr)
		if dialErr != nil {
			return dialErr
		}
		c, dialErr := dialInsecureConn(conn)
		if dialErr != nil {
			return dialErr
		}
		defer c.Close()

		runCfg := imapproc.Config{
			User:      testUser,
			Pass:      testPass,
			Mailbox:   testMailbox,
			Exec:      script,
			OnSuccess: imapproc.OnSuccessSeen,
			Once:      true,
		}

		if n == 1 {
			// Simulate a failure on the first attempt (e.g. login error
			// or IDLE disconnect) by returning an error without running.
			return imapproc.ErrConnectionLost
		}

		// Second attempt: succeed.
		cancel() // done after this run
		return imapproc.Run(ctx, c, runCfg, nil)
	})

	if err != nil {
		t.Fatalf("RunWithReconnect returned error: %v", err)
	}
	if n := connectCount.Load(); n < 2 {
		t.Errorf("expected at least 2 attempts, got %d", n)
	}

	if n := countUnread(t, addr, testMailbox); n != 0 {
		t.Errorf("expected 0 unread after reconnect processing, got %d", n)
	}
}

// TestIntegration_ReconnectInitialFailure verifies that when the initial
// connection fails (server not yet ready), RunWithReconnect retries until
// the server becomes available.
func TestIntegration_ReconnectInitialFailure(t *testing.T) {
	var serverAddr atomic.Value
	serverAddr.Store("")

	var connectAttempts atomic.Int32

	cfg := imapproc.ReconnectConfig{
		InitialDelay: 20 * time.Millisecond,
		MaxDelay:     100 * time.Millisecond,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	script := writeTempScript(t, "exit 0")

	// Start the server after some retries have already happened.
	go func() {
		time.Sleep(60 * time.Millisecond)
		_, _, addr := newTestServer(t)
		serverAddr.Store(addr)
	}()

	err := imapproc.RunWithReconnect(ctx, cfg, func(ctx context.Context) error {
		connectAttempts.Add(1)

		addr := serverAddr.Load().(string)
		if addr == "" {
			return &net.OpError{Op: "dial", Err: &net.AddrError{Err: "connection refused", Addr: "127.0.0.1"}}
		}

		conn, dialErr := net.Dial("tcp", addr)
		if dialErr != nil {
			return dialErr
		}
		c, dialErr := dialInsecureConn(conn)
		if dialErr != nil {
			return dialErr
		}
		defer c.Close()

		runCfg := imapproc.Config{
			User:      testUser,
			Pass:      testPass,
			Mailbox:   testMailbox,
			Exec:      script,
			OnSuccess: imapproc.OnSuccessSeen,
			Once:      true,
		}
		cancel()
		return imapproc.Run(ctx, c, runCfg, nil)
	})
	if err != nil {
		t.Fatalf("RunWithReconnect returned error: %v", err)
	}
	if n := connectAttempts.Load(); n < 2 {
		t.Errorf("expected at least 2 attempts (at least one failure before server start), got %d", n)
	}
}

// TestParseConfig_ReconnectDefault verifies that reconnect defaults to false.
func TestParseConfig_ReconnectDefault(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Reconnect {
		t.Error("Reconnect = true, want false by default")
	}
}

// TestParseConfig_ReconnectFlag verifies --reconnect sets Reconnect=true.
func TestParseConfig_ReconnectFlag(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs("--reconnect"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Reconnect {
		t.Error("Reconnect = false, want true when --reconnect passed")
	}
}

// TestParseConfig_ReconnectFromConfigFile verifies reconnect: true in YAML.
func TestParseConfig_ReconnectFromConfigFile(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
reconnect: true
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Reconnect {
		t.Error("Reconnect = false, want true from config file")
	}
}

// TestParseConfig_ReconnectInitialDelayDefault verifies the default initial
// delay is zero (run loop applies the 5s default).
func TestParseConfig_ReconnectInitialDelayDefault(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReconnectInitialDelay != 0 {
		t.Errorf("ReconnectInitialDelay = %v, want 0 (uses default)", cfg.ReconnectInitialDelay)
	}
}

// TestParseConfig_ReconnectInitialDelayFlag verifies --reconnect-initial-delay.
func TestParseConfig_ReconnectInitialDelayFlag(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs("--reconnect-initial-delay", "2s"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReconnectInitialDelay != 2*time.Second {
		t.Errorf("ReconnectInitialDelay = %v, want 2s", cfg.ReconnectInitialDelay)
	}
}

// TestParseConfig_ReconnectMaxDelayDefault verifies the default max delay is
// zero (run loop applies the 5m default).
func TestParseConfig_ReconnectMaxDelayDefault(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReconnectMaxDelay != 0 {
		t.Errorf("ReconnectMaxDelay = %v, want 0 (uses default)", cfg.ReconnectMaxDelay)
	}
}

// TestParseConfig_ReconnectMaxDelayFlag verifies --reconnect-max-delay.
func TestParseConfig_ReconnectMaxDelayFlag(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs("--reconnect-max-delay", "3m"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ReconnectMaxDelay != 3*time.Minute {
		t.Errorf("ReconnectMaxDelay = %v, want 3m", cfg.ReconnectMaxDelay)
	}
}

// TestParseConfig_ReconnectFromYAML_AllFields verifies all reconnect fields
// parsed from a YAML config file.
func TestParseConfig_ReconnectFromYAML_AllFields(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
reconnect: true
reconnect_initial_delay: 3s
reconnect_max_delay: 2m
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Reconnect {
		t.Error("Reconnect = false, want true")
	}
	if cfg.ReconnectInitialDelay != 3*time.Second {
		t.Errorf("ReconnectInitialDelay = %v, want 3s", cfg.ReconnectInitialDelay)
	}
	if cfg.ReconnectMaxDelay != 2*time.Minute {
		t.Errorf("ReconnectMaxDelay = %v, want 2m", cfg.ReconnectMaxDelay)
	}
}
