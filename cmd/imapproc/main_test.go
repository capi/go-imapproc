package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fullArgs returns a slice of flag args that satisfies all required fields.
func fullArgs(overrides ...string) []string {
	base := []string{
		"--addr", "imap.example.com:993",
		"--user", "alice",
		"--pass", "secret",
		"--exec", "/bin/process",
	}
	return append(base, overrides...)
}

// writeYAML creates a temporary YAML config file and returns its path.
func writeYAML(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "imapproc-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func TestParseConfig_AllFlags(t *testing.T) {
	cfg, configPath, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configPath != "" {
		t.Errorf("expected no config file, got %q", configPath)
	}
	if cfg.Addr != "imap.example.com:993" {
		t.Errorf("addr = %q", cfg.Addr)
	}
	if cfg.User != "alice" {
		t.Errorf("user = %q", cfg.User)
	}
	if cfg.Pass != "secret" {
		t.Errorf("pass = %q", cfg.Pass)
	}
	if cfg.Exec != "/bin/process" {
		t.Errorf("exec = %q", cfg.Exec)
	}
	if cfg.Mailbox != "INBOX" {
		t.Errorf("mailbox default = %q, want INBOX", cfg.Mailbox)
	}
}

func TestParseConfig_DefaultMailbox(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mailbox != "INBOX" {
		t.Errorf("mailbox = %q, want INBOX", cfg.Mailbox)
	}
}

func TestParseConfig_MailboxOverride(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs("--mailbox", "Sent"), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Mailbox != "Sent" {
		t.Errorf("mailbox = %q, want Sent", cfg.Mailbox)
	}
}

func TestParseConfig_MissingRequired_NoConfigFile(t *testing.T) {
	_, _, err := parseConfig([]string{}, io.Discard)
	if err == nil {
		t.Fatal("expected error for missing required fields, got nil")
	}
}

func TestParseConfig_HelpFlag(t *testing.T) {
	_, _, err := parseConfig([]string{"--help"}, io.Discard)
	if err == nil {
		t.Fatal("expected error when --help is passed")
	}
}

func TestParseConfig_ConfigFile(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
mailbox: Work
`)
	cfg, configPath, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configPath != path {
		t.Errorf("configPath = %q, want %q", configPath, path)
	}
	if cfg.User != "bob" {
		t.Errorf("user = %q", cfg.User)
	}
	if cfg.Mailbox != "Work" {
		t.Errorf("mailbox = %q", cfg.Mailbox)
	}
}

func TestParseConfig_FlagOverridesConfigFile(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
`)
	cfg, _, err := parseConfig([]string{"--config", path, "--user", "alice"}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.User != "alice" {
		t.Errorf("user = %q, want alice (flag should override config)", cfg.User)
	}
}

func TestParseConfig_PositionalArgOverridesExec(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs("/bin/override"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Exec != "/bin/override" {
		t.Errorf("exec = %q, want /bin/override", cfg.Exec)
	}
}

func TestParseConfig_DefaultConfigFileUsed(t *testing.T) {
	// Write a valid config to a temp dir and point the working directory there
	// so the default search path picks it up.
	dir := t.TempDir()
	content := `
addr: imap.example.com:993
user: default-user
pass: default-pass
exec: /bin/default
`
	if err := os.WriteFile(filepath.Join(dir, "imapproc.yaml"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}

	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(orig) })

	cfg, configPath, err := parseConfig([]string{}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if configPath != "imapproc.yaml" {
		t.Errorf("configPath = %q, want imapproc.yaml", configPath)
	}
	if cfg.User != "default-user" {
		t.Errorf("user = %q", cfg.User)
	}
}

func TestParseConfig_ConfigFileNotFound_ExplicitPath(t *testing.T) {
	_, _, err := parseConfig([]string{"--config", "/nonexistent/path.yaml"}, io.Discard)
	if err == nil {
		t.Fatal("expected error for missing explicit config file")
	}
}

func TestParseConfig_PasswordRedacted(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r := cfg.redacted()
	if r.Pass != "******" {
		t.Errorf("redacted password = %q, want ******", r.Pass)
	}
	// Original must be unchanged.
	if cfg.Pass != "secret" {
		t.Errorf("original password modified: %q", cfg.Pass)
	}
}

func TestParseConfig_OnceFlag(t *testing.T) {
	// --once should set cfg.Once = true.
	cfg, _, err := parseConfig(fullArgs("--once"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Once {
		t.Error("Once = false, want true when --once is passed")
	}
}

func TestParseConfig_OnceFlagDefault(t *testing.T) {
	// Without --once, cfg.Once should be false.
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Once {
		t.Error("Once = true, want false when --once is not passed")
	}
}

func TestParseConfig_OnceFromConfigFile(t *testing.T) {
	// once: true in the config file should set cfg.Once = true.
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
once: true
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Once {
		t.Error("Once = false, want true when once is set in config file")
	}
}

func TestParseConfig_OnceFlagOverridesConfigFile(t *testing.T) {
	// --once flag should override once: false in the config file.
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
once: false
`)
	cfg, _, err := parseConfig([]string{"--config", path, "--once"}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Once {
		t.Error("Once = false, want true when --once flag overrides config file")
	}
}

func TestParseConfig_OnlyNewFlag(t *testing.T) {
	// --only-new should set cfg.OnlyNew = true.
	cfg, _, err := parseConfig(fullArgs("--only-new"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.OnlyNew {
		t.Error("OnlyNew = false, want true when --only-new is passed")
	}
}

func TestParseConfig_OnlyNewFlagDefault(t *testing.T) {
	// Without --only-new, cfg.OnlyNew should be false.
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OnlyNew {
		t.Error("OnlyNew = true, want false when --only-new is not passed")
	}
}

func TestParseConfig_OnlyNewFromConfigFile(t *testing.T) {
	// only_new: true in the config file should set cfg.OnlyNew = true.
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
only_new: true
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.OnlyNew {
		t.Error("OnlyNew = false, want true when only_new is set in config file")
	}
}

func TestParseConfig_OnlyNewFlagOverridesConfigFile(t *testing.T) {
	// --only-new flag should override only_new: false in the config file.
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
only_new: false
`)
	cfg, _, err := parseConfig([]string{"--config", path, "--only-new"}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.OnlyNew {
		t.Error("OnlyNew = false, want true when --only-new flag overrides config file")
	}
}

func TestParseConfig_IdleRefreshIntervalDefault(t *testing.T) {
	// Without --idle-refresh-interval, cfg.IdleRefreshInterval should be zero
	// (the run loop applies the library default).
	cfg, _, err := parseConfig(fullArgs(), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IdleRefreshInterval != 0 {
		t.Errorf("IdleRefreshInterval = %v, want 0 (use library default)", cfg.IdleRefreshInterval)
	}
}

func TestParseConfig_IdleRefreshIntervalFlag(t *testing.T) {
	cfg, _, err := parseConfig(fullArgs("--idle-refresh-interval", "10m"), io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IdleRefreshInterval != 10*time.Minute {
		t.Errorf("IdleRefreshInterval = %v, want 10m", cfg.IdleRefreshInterval)
	}
}

func TestParseConfig_IdleRefreshIntervalFromConfigFile(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
idle_refresh_interval: 15m
`)
	cfg, _, err := parseConfig([]string{"--config", path}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IdleRefreshInterval != 15*time.Minute {
		t.Errorf("IdleRefreshInterval = %v, want 15m", cfg.IdleRefreshInterval)
	}
}

func TestParseConfig_IdleRefreshIntervalFlagOverridesConfigFile(t *testing.T) {
	path := writeYAML(t, `
addr: imap.example.com:993
user: bob
pass: hunter2
exec: /bin/handler
idle_refresh_interval: 15m
`)
	cfg, _, err := parseConfig([]string{"--config", path, "--idle-refresh-interval", "5m"}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IdleRefreshInterval != 5*time.Minute {
		t.Errorf("IdleRefreshInterval = %v, want 5m (flag should override config)", cfg.IdleRefreshInterval)
	}
}
