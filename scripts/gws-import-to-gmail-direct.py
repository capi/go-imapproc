#!/usr/bin/env python3
"""Import an RFC 2822 email from stdin into Gmail via the REST API directly.

This script is a drop-in replacement for ``gws-import-to-gmail.py`` that
avoids the OS ``ARG_MAX`` limit by never placing the email content on a
command-line argument.  Instead it:

1. Reads credentials (client_id, client_secret, refresh_token) from ``gws``::

       gws auth export --unmasked

2. Exchanges the refresh_token for a fresh access token via the Google OAuth2
   token endpoint (``https://oauth2.googleapis.com/token``).

3. Posts a ``multipart/related`` request directly to the Gmail REST API upload
   endpoint (``https://gmail.googleapis.com/upload/gmail/v1/...``) using
   Python's standard ``urllib`` library — no third-party dependencies.

Usage
-----
Identical to gws-import-to-gmail.py::

    exec: ["scripts/gws-import-to-gmail-direct.py"]
    exec: ["scripts/gws-import-to-gmail-direct.py", "--user", "alice@example.com"]
    exec: ["scripts/gws-import-to-gmail-direct.py", "--never-mark-spam"]

Prerequisites
-------------
``gws`` must be installed and ``gws auth login`` must have been run so that
credentials are stored in the encrypted gws credential store.
"""

import argparse
import json
import os
import subprocess
import sys
import urllib.error
import urllib.parse
import urllib.request
import uuid


# ---------------------------------------------------------------------------
# Argument parser
# ---------------------------------------------------------------------------

def build_argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Import an email from stdin into Gmail via the REST API directly.",
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
        help="Print the HTTP request that would be sent without executing it.",
    )
    return parser


# ---------------------------------------------------------------------------
# Credential / token helpers
# ---------------------------------------------------------------------------

def load_gws_credentials() -> dict:
    """Return the unmasked gws credentials dict by calling ``gws auth export``.

    Raises ``RuntimeError`` if gws is not available or has no stored
    credentials.
    """
    try:
        result = subprocess.run(
            ["gws", "auth", "export", "--unmasked"],
            capture_output=True,
            check=True,
        )
    except FileNotFoundError:
        raise RuntimeError(
            "gws not found on PATH. Install gws and run 'gws auth login' first."
        )
    except subprocess.CalledProcessError as exc:
        raise RuntimeError(
            f"gws auth export failed (exit {exc.returncode}): "
            f"{exc.stderr.decode().strip()}"
        )
    try:
        return json.loads(result.stdout)
    except json.JSONDecodeError as exc:
        raise RuntimeError(f"gws auth export returned invalid JSON: {exc}") from exc


def exchange_refresh_token(
    client_id: str,
    client_secret: str,
    refresh_token: str,
    *,
    token_endpoint: str = "https://oauth2.googleapis.com/token",
) -> str:
    """Exchange a refresh_token for a fresh access token.

    Returns the access token string.  Raises ``RuntimeError`` on failure.
    """
    payload = urllib.parse.urlencode({
        "client_id": client_id,
        "client_secret": client_secret,
        "refresh_token": refresh_token,
        "grant_type": "refresh_token",
    }).encode()

    req = urllib.request.Request(
        token_endpoint,
        data=payload,
        method="POST",
    )
    try:
        with urllib.request.urlopen(req) as resp:
            body = json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        error_body = exc.read().decode(errors="replace")
        raise RuntimeError(
            f"Token exchange failed ({exc.code}): {error_body}"
        ) from exc

    if "access_token" not in body:
        raise RuntimeError(
            f"Token endpoint did not return an access_token: {body}"
        )
    return body["access_token"]


def get_access_token() -> str:
    """Obtain a fresh access token using stored gws credentials.

    Checks ``GOOGLE_WORKSPACE_CLI_TOKEN`` first so the caller can inject a
    token directly (useful for testing and CI environments).
    """
    env_token = os.environ.get("GOOGLE_WORKSPACE_CLI_TOKEN", "").strip()
    if env_token:
        return env_token

    creds = load_gws_credentials()
    missing = [k for k in ("client_id", "client_secret", "refresh_token") if not creds.get(k)]
    if missing:
        raise RuntimeError(
            f"gws credentials are missing required fields: {missing}. "
            "Run 'gws auth login' to re-authenticate."
        )
    return exchange_refresh_token(
        creds["client_id"],
        creds["client_secret"],
        creds["refresh_token"],
    )


# ---------------------------------------------------------------------------
# Gmail API upload
# ---------------------------------------------------------------------------

def build_multipart_body(metadata: dict, raw_email: bytes) -> tuple[bytes, str]:
    """Build a multipart/related body for the Gmail upload endpoint.

    Returns ``(body_bytes, content_type_header_value)``.
    """
    boundary = uuid.uuid4().hex
    metadata_json = json.dumps(metadata, separators=(",", ":")).encode()

    body = (
        f"--{boundary}\r\n"
        f"Content-Type: application/json; charset=UTF-8\r\n\r\n"
    ).encode()
    body += metadata_json + b"\r\n"
    body += (
        f"--{boundary}\r\n"
        f"Content-Type: message/rfc822\r\n\r\n"
    ).encode()
    body += raw_email + b"\r\n"
    body += f"--{boundary}--\r\n".encode()

    content_type = f"multipart/related; boundary={boundary}"
    return body, content_type


def import_message(
    access_token: str,
    user_id: str,
    metadata: dict,
    raw_email: bytes,
    query_params: dict,
    *,
    api_base: str = "https://gmail.googleapis.com",
) -> dict:
    """POST the email to the Gmail users.messages.import upload endpoint.

    Returns the parsed JSON response body.  Raises ``RuntimeError`` on
    non-2xx responses.
    """
    body, content_type = build_multipart_body(metadata, raw_email)

    params = {"uploadType": "multipart", **query_params}
    url = (
        f"{api_base}/upload/gmail/v1/users/"
        f"{urllib.parse.quote(user_id, safe='')}"
        f"/messages/import"
        f"?{urllib.parse.urlencode(params)}"
    )

    req = urllib.request.Request(
        url,
        data=body,
        method="POST",
        headers={
            "Authorization": f"Bearer {access_token}",
            "Content-Type": content_type,
        },
    )
    try:
        with urllib.request.urlopen(req) as resp:
            return json.loads(resp.read())
    except urllib.error.HTTPError as exc:
        error_body = exc.read().decode(errors="replace")
        # Print the API error to stdout so it is visible in imapproc logs,
        # matching the behaviour of gws which also writes its JSON output to
        # stdout.
        print(error_body)
        raise RuntimeError(
            f"Gmail API returned {exc.code}"
        ) from exc


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    args = build_argument_parser().parse_args()

    # Read the raw RFC 2822 email from stdin.
    raw_email = sys.stdin.buffer.read()
    if not raw_email:
        print("error: no email data received on stdin", file=sys.stderr)
        return 1

    # Build query parameters (passed as URL params to the API).
    query_params: dict = {}
    if args.never_mark_spam:
        query_params["neverMarkSpam"] = "true"
    if args.process_for_calendar:
        query_params["processForCalendar"] = "true"
    if args.deleted:
        query_params["deleted"] = "true"
    if args.internal_date_source is not None:
        query_params["internalDateSource"] = args.internal_date_source

    # Build the JSON metadata body (no "raw" field — that goes in the upload part).
    metadata: dict = {}
    label_ids = list(args.labels or [])
    if not args.do_not_mark_unread and "UNREAD" not in label_ids:
        label_ids.append("UNREAD")
    if not args.archive and "INBOX" not in label_ids:
        label_ids.append("INBOX")
    if label_ids:
        metadata["labelIds"] = label_ids

    if args.dry_run:
        _print_dry_run(args.user, metadata, query_params, raw_email)
        return 0

    try:
        access_token = get_access_token()
        response = import_message(
            access_token,
            args.user,
            metadata,
            raw_email,
            query_params,
        )
        print(json.dumps(response, indent=2))
        return 0
    except RuntimeError as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


def _print_dry_run(
    user_id: str,
    metadata: dict,
    query_params: dict,
    raw_email: bytes,
) -> None:
    """Print a human-readable summary of the request that would be sent."""
    params = {"uploadType": "multipart", **query_params}
    url = (
        f"https://gmail.googleapis.com/upload/gmail/v1/users/"
        f"{urllib.parse.quote(user_id, safe='')}"
        f"/messages/import"
        f"?{urllib.parse.urlencode(params)}"
    )
    print(f"POST {url}")
    print(f"Content-Type: multipart/related; boundary=<boundary>")
    print(f"Authorization: Bearer <token>")
    print()
    print("-- part 1: application/json")
    print(json.dumps(metadata, indent=2) if metadata else "{}")
    print()
    print("-- part 2: message/rfc822")
    print(f"<{len(raw_email)} bytes of RFC 2822 email>")


if __name__ == "__main__":
    sys.exit(main())
