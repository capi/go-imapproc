#!/usr/bin/env python3
"""Import an RFC 2822 email from stdin into Gmail via gws.

Designed to be used as the exec handler for imapproc:

    exec: ["scripts/gws-import-to-gmail.py"]
    exec: ["scripts/gws-import-to-gmail.py", "--user", "alice@example.com"]
    exec: ["scripts/gws-import-to-gmail.py", "--never-mark-spam", "--process-for-calendar"]

The script reads a raw email from stdin, base64url-encodes it, and calls:

    gws gmail users messages import \
        --params '{"userId": "me", ...}' \
        --json   '{"raw": "<base64url-encoded email>"}'

Exit codes mirror the gws process so imapproc can decide on_success actions.

Known limitation
----------------
The base64url-encoded email is passed as an inline value to the ``--json``
argument of ``gws``.  The OS kernel imposes a limit on the total size of a
process's argument list (``ARG_MAX``, typically 2 MB on Linux).  Emails whose
encoded representation approaches or exceeds that limit will cause ``gws`` to
fail with::

    OSError: [Errno 7] Argument list too long: 'gws'

For large emails use ``gws-import-to-gmail-direct.py`` instead, which bypasses
``gws`` for the upload step and posts the email directly to the Gmail REST API
using Python's standard ``urllib`` library.
"""

import argparse
import base64
import json
import subprocess
import sys


def base64url_encode(data: bytes) -> str:
    """Encode bytes using base64url (RFC 4648 section 5), no padding."""
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def build_argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Import an email from stdin into Gmail via gws.",
    )
    parser.add_argument(
        "--user",
        default="me",
        help='Gmail userId (default: "me" for the authenticated user).',
    )
    parser.add_argument(
        "--never-mark-spam",
        action="store_true",
        default=False,
        help="Ignore the Gmail spam classifier and never mark as SPAM.",
    )
    parser.add_argument(
        "--process-for-calendar",
        action="store_true",
        default=False,
        help="Process calendar invites and add meetings to Google Calendar.",
    )
    parser.add_argument(
        "--deleted",
        action="store_true",
        default=False,
        help="Mark email as permanently deleted (Google Vault only).",
    )
    parser.add_argument(
        "--internal-date-source",
        choices=["dateHeader", "receivedTime"],
        default=None,
        help='Source for Gmail internal date (default: API default "dateHeader").',
    )
    parser.add_argument(
        "--do-not-mark-unread",
        action="store_true",
        default=False,
        help="Do not add the UNREAD label (default is to import as unread).",
    )
    parser.add_argument(
        "--archive",
        action="store_true",
        default=False,
        help="Do not add the INBOX label (default is to import into the inbox).",
    )
    parser.add_argument(
        "--add-label-id",
        action="append",
        dest="labels",
        metavar="LABEL_ID",
        help="Apply a label to the imported message by its Gmail label ID (repeatable).",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        default=False,
        help="Print the gws command that would be executed without running it.",
    )
    return parser


def main() -> int:
    args = build_argument_parser().parse_args()

    # Read the raw RFC 2822 email from stdin.
    raw_email = sys.stdin.buffer.read()
    if not raw_email:
        print("error: no email data received on stdin", file=sys.stderr)
        return 1

    # Build the --params object (query parameters).
    params: dict = {"userId": args.user}
    if args.never_mark_spam:
        params["neverMarkSpam"] = True
    if args.process_for_calendar:
        params["processForCalendar"] = True
    if args.deleted:
        params["deleted"] = True
    if args.internal_date_source is not None:
        params["internalDateSource"] = args.internal_date_source

    # Build the --json request body.
    body: dict = {"raw": base64url_encode(raw_email)}
    label_ids = list(args.labels or [])
    if not args.do_not_mark_unread and "UNREAD" not in label_ids:
        label_ids.append("UNREAD")
    if not args.archive and "INBOX" not in label_ids:
        label_ids.append("INBOX")
    if label_ids:
        body["labelIds"] = label_ids

    # Assemble the gws command.
    cmd = [
        "gws", "gmail", "users", "messages", "import",
        "--params", json.dumps(params, separators=(",", ":")),
        "--json", json.dumps(body, separators=(",", ":")),
    ]

    if args.dry_run:
        print(" ".join(_shell_quote(c) for c in cmd))
        return 0

    # Execute gws and propagate its exit code.
    result = subprocess.run(cmd)
    return result.returncode


def _shell_quote(s: str) -> str:
    """Minimal shell quoting for --dry-run display."""
    if s and all(c.isalnum() or c in "-_./=" for c in s):
        return s
    return "'" + s.replace("'", "'\\''") + "'"


if __name__ == "__main__":
    sys.exit(main())
