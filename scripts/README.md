# scripts/

Helper scripts for use with [imapproc](../README.md) as `exec` handlers.
Each script reads a raw RFC 2822 email from **stdin** and exits with a
non-zero code on failure so imapproc can skip the `on_success` action.

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
