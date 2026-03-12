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

// execValue holds the executable path and optional arguments parsed from the
// YAML "exec" field. It accepts either a plain string (executable only) or a
// sequence of strings (executable followed by arguments), mirroring the Docker
// CMD / Dockerfile ENTRYPOINT convention.
//
//	exec: /usr/local/bin/process-email          # string form – no args
//	exec: ["/usr/local/bin/process-email", "-v"] # sequence form – with args
type execValue struct {
	cmd  string   // executable path
	args []string // optional arguments
}

// UnmarshalYAML implements yaml.Unmarshaler for execValue, accepting both a
// scalar string and a sequence of strings.
func (e *execValue) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		e.cmd = value.Value
		e.args = nil
		return nil
	case yaml.SequenceNode:
		var items []string
		if err := value.Decode(&items); err != nil {
			return fmt.Errorf("exec: %w", err)
		}
		if len(items) == 0 {
			return fmt.Errorf("exec: sequence must not be empty")
		}
		e.cmd = items[0]
		e.args = items[1:]
		return nil
	default:
		return fmt.Errorf("exec: must be a string or a sequence of strings")
	}
}

// Config holds all runtime settings for the imapproc CLI. It is the
// authoritative representation after merging config-file values with CLI flags.
type Config struct {
	Addr      string                   `yaml:"addr"`
	User      string                   `yaml:"user"`
	Pass      string                   `yaml:"pass"`
	Mailbox   string                   `yaml:"mailbox"`
	Exec      string                   `yaml:"-"` // resolved executable; set from exec field or CLI flag/positional args
	ExecArgs  []string                 `yaml:"-"` // resolved args; set from exec field or CLI positional args
	OnSuccess imapproc.OnSuccessAction `yaml:"on_success"`
	// OnSuccessTarget is the destination mailbox when OnSuccess is
	// OnSuccessMove. Defaults to imapproc.DefaultMoveTarget ("Trash").
	OnSuccessTarget string `yaml:"on_success_target"`
	// Once processes all unread messages once and exits without entering IMAP
	// IDLE. Useful for one-shot/cron-style invocations. Defaults to false.
	Once bool `yaml:"once"`
	// IdleRefreshInterval is how often the IDLE command is refreshed. A zero
	// value means use the library default (25 minutes). Stored as a duration
	// string in YAML (e.g. "25m").
	IdleRefreshInterval time.Duration `yaml:"idle_refresh_interval"`

	// Reconnect enables automatic reconnection when the connection is lost
	// (during IDLE or initial connect). Defaults to false.
	Reconnect bool `yaml:"reconnect"`

	// ReconnectInitialDelay is the first backoff delay before the first retry.
	// A zero value uses the library default (5s). Stored as a duration string
	// in YAML (e.g. "5s").
	ReconnectInitialDelay time.Duration `yaml:"reconnect_initial_delay"`

	// ReconnectMaxDelay caps the exponential backoff delay. A zero value uses
	// the library default (5m). Stored as a duration string in YAML (e.g. "5m").
	ReconnectMaxDelay time.Duration `yaml:"reconnect_max_delay"`

	// WebEnabled enables the built-in HTTP monitoring server (dashboard +
	// /api/health). Disabled by default.
	WebEnabled bool `yaml:"web_enabled"`

	// WebAddr is the listen address for the HTTP monitoring server.
	// Defaults to ":8080" when WebEnabled is true.
	WebAddr string `yaml:"web_addr"`

	// InstanceName is an optional human-readable label for this instance.
	// When non-empty it is included in the /api/health response.
	InstanceName string `yaml:"instance_name"`
}

// toRunConfig converts the CLI Config into an imapproc.Config for the run loop.
// stats may be nil when monitoring is disabled.
func (c *Config) toRunConfig(stats *imapproc.Stats) imapproc.Config {
	return imapproc.Config{
		User:                c.User,
		Pass:                c.Pass,
		Mailbox:             c.Mailbox,
		Exec:                c.Exec,
		ExecArgs:            c.ExecArgs,
		OnSuccess:           c.OnSuccess,
		MoveTarget:          c.OnSuccessTarget,
		Once:                c.Once,
		IdleRefreshInterval: c.IdleRefreshInterval,
		Stats:               stats,
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

// yamlConfig is the on-disk YAML representation. It uses execValue for the
// "exec" field so that both string and sequence forms are accepted.
type yamlConfig struct {
	Addr                  string                   `yaml:"addr"`
	User                  string                   `yaml:"user"`
	Pass                  string                   `yaml:"pass"`
	Mailbox               string                   `yaml:"mailbox"`
	Exec                  execValue                `yaml:"exec"`
	OnSuccess             imapproc.OnSuccessAction `yaml:"on_success"`
	OnSuccessTarget       string                   `yaml:"on_success_target"`
	Once                  bool                     `yaml:"once"`
	IdleRefreshInterval   time.Duration            `yaml:"idle_refresh_interval"`
	Reconnect             bool                     `yaml:"reconnect"`
	ReconnectInitialDelay time.Duration            `yaml:"reconnect_initial_delay"`
	ReconnectMaxDelay     time.Duration            `yaml:"reconnect_max_delay"`
	WebEnabled            bool                     `yaml:"web_enabled"`
	WebAddr               string                   `yaml:"web_addr"`
	InstanceName          string                   `yaml:"instance_name"`
}

// loadConfig reads and parses a YAML config file.
func loadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var yc yamlConfig
	if err := yaml.NewDecoder(f).Decode(&yc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg := &Config{
		Addr:                  yc.Addr,
		User:                  yc.User,
		Pass:                  yc.Pass,
		Mailbox:               yc.Mailbox,
		Exec:                  yc.Exec.cmd,
		ExecArgs:              yc.Exec.args,
		OnSuccess:             yc.OnSuccess,
		OnSuccessTarget:       yc.OnSuccessTarget,
		Once:                  yc.Once,
		IdleRefreshInterval:   yc.IdleRefreshInterval,
		Reconnect:             yc.Reconnect,
		ReconnectInitialDelay: yc.ReconnectInitialDelay,
		ReconnectMaxDelay:     yc.ReconnectMaxDelay,
		WebEnabled:            yc.WebEnabled,
		WebAddr:               yc.WebAddr,
		InstanceName:          yc.InstanceName,
	}
	return cfg, nil
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
	case imapproc.OnSuccessMove:
		// Apply the default target folder when none was specified.
		if c.OnSuccessTarget == "" {
			c.OnSuccessTarget = imapproc.DefaultMoveTarget
		}
	default:
		return fmt.Errorf("invalid on_success value %q: must be %q, %q, or %q", c.OnSuccess, imapproc.OnSuccessSeen, imapproc.OnSuccessDelete, imapproc.OnSuccessMove)
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
