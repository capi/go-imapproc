# scripts/

Helper scripts for use with [imapproc](../README.md) as `exec` handlers.
Each script reads a raw RFC 2822 email from **stdin** and exits with a
non-zero code on failure so imapproc can skip the `on_success` action.

---

## 📌 Recommended: gws-import-to-gmail-direct.py

**Use this script by default.** It's a drop-in replacement for
`gws-import-to-gmail.py` that avoids OS-level limitations and works reliably
with emails of any size. See [Why use this script?](#why-use-gws-import-to-gmail-directpy)
below.

---

## gws-import-to-gmail.py

Imports an email into a Gmail mailbox via the
[Gmail API `users.messages.import`](https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/import)
endpoint, using the [`gws`](https://github.com/googleworkspace/cli) CLI for
authentication and API access.

### Prerequisites

- **Python 3** — no third-party packages required.
- **`gws`** — the [Google Workspace CLI](https://github.com/googleworkspace/cli)
  must be installed, available on `$PATH`, and fully authenticated for the
  target Gmail account. Follow the installation and authentication guide on
  that page before using this script.

### Usage

```
gws-import-to-gmail.py [OPTIONS]
```

The script is designed to be called by imapproc via the `exec` setting:

```yaml
# imapproc.yaml

# Minimal — import as authenticated user, mark as unread:
exec: ["scripts/gws-import-to-gmail.py"]

# Specific target account:
exec: ["scripts/gws-import-to-gmail.py", "--user", "alice@example.com"]

# Full example with an extra label and calendar processing:
exec:
  - scripts/gws-import-to-gmail.py
  - --user
  - alice@example.com
  - --never-mark-spam
  - --process-for-calendar
  - --add-label-id
  - Label_123abc
```

### Options

| Option | Default | Description |
|---|---|---|
| `--user USER` | `me` | Gmail user ID. `me` refers to the authenticated user. |
| `--do-not-mark-unread` | off | By default the `UNREAD` label is added so the message appears unread. Pass this flag to suppress that behaviour. |
| `--archive` | off | By default the `INBOX` label is added so the message appears in the inbox. Pass this flag to skip it, archiving the message instead. |
| `--add-label-id LABEL_ID` | — | Apply a Gmail label to the imported message by its label ID. Repeatable. See [Finding label IDs](#finding-label-ids) below. |
| `--never-mark-spam` | off | Tell Gmail never to classify this message as spam, bypassing the spam classifier. |
| `--process-for-calendar` | off | Extract calendar invites from the email and add the events to Google Calendar. |
| `--internal-date-source` | `dateHeader` | Source Gmail uses for the message's internal timestamp. One of `dateHeader` or `receivedTime`. |
| `--deleted` | off | Mark the message as permanently deleted (invisible except to Google Vault admins). Google Workspace accounts only. |
| `--dry-run` | off | Print the `gws` command that would be executed without actually running it. Useful for testing. |

### Finding label IDs

`--add-label-id` expects a Gmail label ID, **not** the human-readable label
name. System labels use their name as the ID (`INBOX`, `STARRED`, `UNREAD`,
etc.), but user-created labels have opaque IDs like `Label_1234567890`.

List all labels and their IDs with:

```bash
gws gmail users labels list --params '{"userId":"me"}'
```

Look up a specific label by name (requires [`jq`](https://jqlang.org)):

```bash
gws gmail users labels list --params '{"userId":"me"}' \
  | jq '.labels[] | select(.name == "some/label-name")'
```

Use the `id` field from the output as the value for `--add-label-id`.

### Exit codes

The script propagates the exit code of the `gws` subprocess directly.
Exit code `0` signals success to imapproc, which then applies the
configured `on_success` action (e.g. mark as seen, delete, or move).
Any non-zero exit code causes imapproc to skip the message.

### Limitations

**Large email handling**: The script passes the base64url-encoded email content
as an inline value to the `gws` CLI via the `--json` argument. The OS kernel
imposes a limit on the total size of a process's argument list (`ARG_MAX`).
In containerized environments (like Docker), this limit is often much lower than
the typical 2 MB on bare Linux. Emails whose base64-encoded representation exceeds
the system's ARG_MAX will cause the script to fail with:

```
OSError: [Errno 7] Argument list too long: 'gws'
```

This can occur starting at ~100 kB or even smaller, depending on the system's
ARG_MAX setting. Use [`gws-import-to-gmail-direct.py`](#gws-import-to-gmail-directpy)
for any email that encounters this error.

**Note**: This is a fundamental OS/container limitation that cannot be worked
around without using a different approach (as `gws-import-to-gmail-direct.py` does).

### Examples

```bash
# Test with a local .eml file — see the gws command without running it:
cat message.eml | scripts/gws-import-to-gmail.py --dry-run

# Import and apply an extra label (by ID):
cat message.eml | scripts/gws-import-to-gmail.py --add-label-id INBOX

# Import without marking unread (i.e. import as read):
cat message.eml | scripts/gws-import-to-gmail.py --do-not-mark-unread

# Import into archive (skip INBOX label):
cat message.eml | scripts/gws-import-to-gmail.py --archive

# Import for a specific user, process calendar invites:
cat message.eml | scripts/gws-import-to-gmail.py \
    --user alice@example.com \
    --process-for-calendar
```

---

## gws-import-to-gmail-direct.py

**Recommended** — Drop-in replacement for `gws-import-to-gmail.py` that avoids
the OS `ARG_MAX` limitation by uploading directly to the Gmail REST API instead
of via the `gws` CLI. Provides identical command-line interface and exit code
semantics.

### Why use this script?

1. **No size limits** — Works with emails of any size. The original script fails
   with `OSError: Argument list too long` on even moderately-sized emails (starting
   around 100 kB base64-encoded, depending on container/system ARG_MAX limits).
   This script has no such limitation.

2. **Identical interface** — Drop-in replacement. Same options, same behavior,
   same exit codes. Just change the script name in your imapproc config.

3. **More efficient** — Avoids spawning a separate `gws` CLI process. Uses Python
   stdlib only to make direct API calls, resulting in faster execution.

4. **Same credential access** — Reads from the same `gws` credential store, so
   no additional authentication setup is required.

5. **CI/CD friendly** — Supports `GOOGLE_WORKSPACE_CLI_TOKEN` environment variable
   for pre-authenticated environments where running `gws auth export` is not
   possible.

### How it works

1. Reads credentials via `gws auth export --unmasked` (or `GOOGLE_WORKSPACE_CLI_TOKEN` env var)
2. Exchanges the stored `refresh_token` for a fresh access token via `https://oauth2.googleapis.com/token`
3. Constructs a `multipart/related` request body with:
   - Part 1: JSON metadata (labels, flags, etc.)
   - Part 2: Raw RFC 2822 email with `Content-Type: message/rfc822`
4. POSTs directly to `https://gmail.googleapis.com/upload/gmail/v1/users/{userId}/messages/import`

No intermediate `gws` process execution, so no argument list size limits.

**Note on authentication**: For simplicity, this script still relies on `gws` for
initial authentication setup and credential storage. The key difference is that
instead of invoking `gws` as a subprocess for the API call (which hits ARG_MAX),
this script reads the stored `refresh_token` from `gws`'s credential store and
exchanges it directly for an access token via Google's OAuth2 endpoint.

### Prerequisites

- **Python 3** — no third-party packages required (uses stdlib only).
- **`gws`** — must be installed and authenticated (same as `gws-import-to-gmail.py`).
  This script reads credentials from `gws`'s encrypted credential store.

### Usage

Identical to `gws-import-to-gmail.py`:

```yaml
# imapproc.yaml

# Drop-in replacement — change 'gws-import-to-gmail.py' to 'gws-import-to-gmail-direct.py':
exec: ["scripts/gws-import-to-gmail-direct.py"]

exec: ["scripts/gws-import-to-gmail-direct.py", "--user", "alice@example.com"]

exec:
  - scripts/gws-import-to-gmail-direct.py
  - --process-for-calendar
  - --add-label-id
  - Label_123abc
```

All options are identical to `gws-import-to-gmail.py` (see table above).

### Environment variables

| Variable | Description |
|---|---|
| `GOOGLE_WORKSPACE_CLI_TOKEN` | Pre-obtained OAuth2 access token. If set, the script uses this token directly without calling `gws auth export`. Useful for CI/CD pipelines. |

### Token caching

The script caches access tokens locally to avoid unnecessary token exchanges. Cached tokens are reused until they are older than `--token-rotation-interval` (default: 50 minutes) or rejected by the API (401).

**Options**:
- `--token-rotation-interval MINUTES` — Default: 50
- `--token-cache-file PATH` — Default: `~/.config/gws/imapproc-token-cache.json`

⚠️ **Security**: The cache file contains an active OAuth2 access token and is stored with mode `0600`. Ensure `~/.config/gws/` has appropriate permissions and is not world-readable.

**Debugging**: Trace output goes to stderr with `[gws-import]` prefix:
```bash
imapproc 2>&1 | grep '\[gws-import\]'
```

### Exit codes

Same as `gws-import-to-gmail.py`:
- Exit code `0` signals success to imapproc
- Non-zero exit code causes imapproc to skip the message

### Examples

```bash
# Same as gws-import-to-gmail.py:
cat message.eml | scripts/gws-import-to-gmail-direct.py --dry-run

cat message.eml | scripts/gws-import-to-gmail-direct.py --add-label-id INBOX

# With pre-obtained token (CI environment):
export GOOGLE_WORKSPACE_CLI_TOKEN="ya29.abc123..."
cat large-email.eml | scripts/gws-import-to-gmail-direct.py
```

### Troubleshooting

**`RuntimeError: gws not found on PATH`**

Ensure `gws` is installed and available on `$PATH`. Run:

```bash
which gws
```

**`RuntimeError: gws credentials are missing required fields`**

Ensure `gws auth login` has been run to store credentials:

```bash
gws auth login
gws auth status  # Verify credentials are present
```

**`RuntimeError: Token exchange failed (400)`**

The refresh token may have been revoked. Re-authenticate:

```bash
gws auth logout
gws auth login
```
