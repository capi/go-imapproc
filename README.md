# imapproc

A lightweight Go daemon that monitors an IMAP mailbox, processes unread emails with external programs, and automatically marks them as read on success.

## Overview

`imapproc` connects to an IMAP server (e.g., Gmail), watches for new messages using IMAP IDLE, and invokes a custom executable for each unread email. The raw RFC822 message is piped to your program's stdin. If the program exits successfully (code 0), the email is marked as read (or deleted, if configured).

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

# Action after successful processing: "seen" (default) or "delete"
on_success: seen

# Process all unread messages once and exit without entering IMAP IDLE.
# Useful for cron-style one-shot invocations.
once: false
```

### Important Command-Line Flags

```
--config string      Path to config file
--addr string        IMAP server address (e.g. imap.gmail.com:993)
--user string        IMAP username
--pass string        IMAP password
--mailbox string     Mailbox to monitor (default: INBOX)
--exec string        Program to run for each unread message
--on-success string  Action on success: "seen" (default) or "delete"
--once               Process all unread messages once and exit (skip IDLE)
```

CLI flags override config file values. Positional arguments override `--exec`.

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
3. For each unread message, pipes the raw RFC822 content to your program
4. On success (exit code 0), performs the configured `on_success` action
   (`seen`: marks as read; `delete`: expunges the message)
5. If `once` is set, exits after the first pass without entering IDLE
6. Otherwise, uses IMAP IDLE to efficiently wait for new messages
7. Continues until interrupted (Ctrl-C or SIGTERM)

## Requirements

- Go 1.25+ (for building)
- IMAP server access
- For Gmail: [app-specific password](https://support.google.com/accounts/answer/185833)

## Testing

```bash
make test
```
