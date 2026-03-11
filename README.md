# imapproc

A lightweight Go daemon that monitors an IMAP mailbox, processes unread emails with external programs, and automatically marks them as read on success.

## Overview

`imapproc` connects to an IMAP server (e.g., Gmail), watches for new messages using IMAP IDLE, and invokes a custom executable for each unread email. The raw RFC 2822 message is piped to your program's stdin. If the program exits successfully (code 0), the email is marked as read (or deleted, if configured).

## AI Usage Disclaimer

This project was built using AI tools, but was closely monitored and tested throughout development. All commits by AI agents are clearly marked with the agent's name in the commit message. Significant effort has been invested in unit testing, integration testing, and manual testing to ensure code quality and reliability. This codebase was built for personal use and will be used to process and access personal emails and Gmail account data. I trust this code with my email infrastructure and personal data.

## Use Cases

- Process incoming emails with custom logic
- Integrate with email workflows and automation systems
- Build email-to-action pipelines

## Installation

```bash
make build
```

## Configuration

Configure `imapproc` using either a YAML config file or command-line flags.

### Config File Locations (searched in order)

1. `./imapproc.yaml`
2. `~/.imapproc.yaml`
3. `/etc/imapproc/config.yaml`

### Example Config

```yaml
addr: imap.gmail.com:993
user: you@gmail.com
pass: your-app-password
mailbox: INBOX
exec: /usr/local/bin/process-email

# Action after successful processing: "seen" (default), "delete", or "move"
on_success: seen

# Process all unread messages once and exit without entering IMAP IDLE.
# Useful for cron-style one-shot invocations.
once: false
```

### Important Command-Line Flags

```
--config string                   Path to config file
--addr string                     IMAP server address (e.g. imap.gmail.com:993)
--user string                     IMAP username
--pass string                     IMAP password
--mailbox string                  Mailbox to monitor (default: INBOX)
--exec string                     Program to run for each unread message
--on-success string               Action on success: "seen" (default), "delete", or "move"
--on-success-target string        Destination mailbox when --on-success=move (default: "Trash")
--once                            Process all unread messages once and exit (skip IDLE)
--idle-refresh-interval duration  How often to refresh IMAP IDLE (default: 25m)
--reconnect                       Reconnect automatically when the connection is lost
--reconnect-initial-delay dur     Initial backoff delay before first reconnect (default: 5s)
--reconnect-max-delay duration    Maximum backoff delay between reconnects (default: 5m)
--web-enabled                     Enable the HTTP monitoring server
--web-addr string                 Listen address for the monitoring server (default: :8080)
```

CLI flags override config file values. Positional arguments override `--exec`:
the first positional argument is the program to run, and any additional
positional arguments are passed as arguments to that program.

See [`imapproc.example.yaml`](imapproc.example.yaml) for all available options and their defaults.

## Usage

```bash
# Using a config file
imapproc

# Using CLI flags
imapproc --addr imap.gmail.com:993 --user me@gmail.com --pass mypass --mailbox INBOX /path/to/processor

# Using positional program argument
imapproc /usr/local/bin/process-email arg1 arg2

# One-shot: process current unread messages and exit (no IDLE loop)
imapproc --once
```

## How It Works

1. Connects to the IMAP server and authenticates
2. Searches for all unread messages in the specified mailbox
3. For each unread message, pipes the raw RFC 2822 content to your program
4. On success (exit code 0), performs the configured `on_success` action
   (`seen`: marks as read; `delete`: expunges the message; `move`: moves to target mailbox)
5. If `once` is set, exits after the first pass without entering IDLE
6. Otherwise, uses IMAP IDLE to efficiently wait for new messages
7. Continues until interrupted (Ctrl-C or SIGTERM)

## Requirements

- Go 1.25+ (for building)
- IMAP server access
- For Gmail: [app-specific password](https://support.google.com/accounts/answer/185833)

## Security

- **Config file permissions** — the config file contains your IMAP password in plain text. Restrict access: `chmod 600 imapproc.yaml`.
- **CLI password flag** — passing `--pass` on the command line exposes the password in the process list (`ps aux`). Prefer using a config file.
- **Exec handler** — the subprocess invoked via `exec` runs with the same privileges as the daemon. Only point `exec` at scripts you trust.
- **TLS** — connections to the IMAP server always use TLS; plain-text connections are not supported.

## Monitoring

Enable the built-in HTTP server with `--web-enabled` (or `web_enabled: true` in the config file):

```bash
imapproc --web-enabled --web-addr :8080
```

**`GET /api/health`** — JSON health endpoint suitable for uptime monitors and health checks.

- Returns **HTTP 200** when healthy, **HTTP 503** when unhealthy.
- Reports connection status (`UP` / `DOWN`), the outcome of the last processing pass, and cumulative message counters.

```json
{
  "status": "UP",
  "details": {
    "connection": { "status": "UP", "healthy": true },
    "lastPoll": { "healthy": true, "timestamp": "2025-06-15T12:00:00Z",
                  "messagesReceived": 3, "messagesSuccess": 3, "messagesFailed": 0 }
  },
  "stats": { "messagesReceived": 42, "messagesSuccess": 41, "messagesFailed": 1 }
}
```

**`GET /`** — HTML status dashboard that auto-refreshes every 5 seconds.

## Scripts

The [`scripts/`](scripts/README.md) directory contains helper scripts designed to work with `imapproc` as `exec` handlers. These scripts read raw RFC 2822 emails from stdin and can be used to integrate `imapproc` with external systems and workflows.

## Testing

```bash
make test
```
