package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// Config holds all runtime settings for the imapproc CLI. It is the
// authoritative representation after merging config-file values with CLI flags.
type Config struct {
	Addr      string                   `yaml:"addr"`
	User      string                   `yaml:"user"`
	Pass      string                   `yaml:"pass"`
	Mailbox   string                   `yaml:"mailbox"`
	Exec      string                   `yaml:"exec"`
	OnSuccess imapproc.OnSuccessAction `yaml:"on_success"`
	// Once processes all unread messages once and exits without entering IMAP
	// IDLE. Useful for one-shot/cron-style invocations. Defaults to false.
	Once bool `yaml:"once"`
	// IdleRefreshInterval is how often the IDLE command is refreshed. A zero
	// value means use the library default (25 minutes). Stored as a duration
	// string in YAML (e.g. "25m").
	IdleRefreshInterval time.Duration `yaml:"idle_refresh_interval"`
}

// toRunConfig converts the CLI Config into an imapproc.Config for the run loop.
func (c *Config) toRunConfig() imapproc.Config {
	return imapproc.Config{
		User:                c.User,
		Pass:                c.Pass,
		Mailbox:             c.Mailbox,
		Exec:                c.Exec,
		OnSuccess:           c.OnSuccess,
		Once:                c.Once,
		IdleRefreshInterval: c.IdleRefreshInterval,
	}
}

// defaultConfigPaths returns the ordered list of candidate config file locations.
func defaultConfigPaths() []string {
	paths := []string{"imapproc.yaml"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".imapproc.yaml"))
	}
	paths = append(paths, "/etc/imapproc/config.yaml")
	return paths
}

// loadConfig reads and parses a YAML config file.
func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cfg Config
	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// findAndLoadConfig locates a config file using the given explicit path (may be
// empty) or the default search order. It returns the parsed config and the path
// that was loaded (empty string if no file was found).
// Returns a zero-value Config (not an error) when no file is found, so that
// flag-only usage still works.
func findAndLoadConfig(explicit string) (*Config, string, error) {
	if explicit != "" {
		cfg, err := loadConfig(explicit)
		if err != nil {
			return nil, "", fmt.Errorf("config: %w", err)
		}
		return cfg, explicit, nil
	}

	for _, p := range defaultConfigPaths() {
		cfg, err := loadConfig(p)
		if err == nil {
			return cfg, p, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, "", fmt.Errorf("config: %w", err)
		}
	}

	return &Config{}, "", nil
}

// validate returns an error if any required field is empty or invalid.
func (c *Config) validate() error {
	var missing []string
	if c.Addr == "" {
		missing = append(missing, "--addr")
	}
	if c.User == "" {
		missing = append(missing, "--user")
	}
	if c.Pass == "" {
		missing = append(missing, "--pass")
	}
	if c.Exec == "" {
		missing = append(missing, "--exec")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required settings: %v (set in config file or via flags)", missing)
	}
	switch c.OnSuccess {
	case imapproc.OnSuccessSeen, imapproc.OnSuccessDelete:
		// valid
	default:
		return fmt.Errorf("invalid on_success value %q: must be %q or %q", c.OnSuccess, imapproc.OnSuccessSeen, imapproc.OnSuccessDelete)
	}
	return nil
}

// redacted returns a copy of the config safe for logging (password masked).
func (c Config) redacted() Config {
	if c.Pass != "" {
		c.Pass = "******"
	}
	return c
}
