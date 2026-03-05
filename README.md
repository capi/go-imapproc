# imapproc

A lightweight Go daemon that monitors an IMAP mailbox, processes unread emails with external programs, and automatically marks them as read on success.

## Overview

`imapproc` connects to an IMAP server (e.g., Gmail), watches for new messages using IMAP IDLE, and invokes a custom executable for each unread email. The raw RFC822 message is piped to your program's stdin. If the program exits successfully (code 0), the email is marked as read.

## Use Cases

- Process incoming emails with custom logic
- Integrate with email workflows and automation systems
- Filter and categorize emails using external tools
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
```

### Command-Line Flags

```
--config string    Path to config file
--addr string      IMAP server address (e.g. imap.gmail.com:993)
--user string      IMAP username
--pass string      IMAP password
--mailbox string   Mailbox to monitor (default: INBOX)
--exec string      Program to run for each unread message
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
```

## How It Works

1. Connects to the IMAP server and authenticates
2. Searches for all unread messages in the specified mailbox
3. For each unread message, pipes the raw RFC822 content to your program
4. Marks the message as read if your program exits with code 0
5. Uses IMAP IDLE to efficiently wait for new messages
6. Continues until interrupted (Ctrl-C or SIGTERM)

## Requirements

- Go 1.25+ (for building)
- IMAP server access
- For Gmail: [app-specific password](https://support.google.com/accounts/answer/185833)

## Testing

```bash
make test
```
