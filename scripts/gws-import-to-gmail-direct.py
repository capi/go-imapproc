#!/usr/bin/env python3
"""Import an RFC 2822 email from stdin into Gmail via the REST API directly.

This script is a drop-in replacement for ``gws-import-to-gmail.py`` that
avoids the OS ``ARG_MAX`` limit by never placing the email content on a
command-line argument.  Instead it:

1. Reads credentials (client_id, client_secret, refresh_token) from ``gws``::

       gws auth export --unmasked

2. Obtains an access token, caching it in a local file to avoid unnecessary
   token exchanges.  The cached token is re-used until it is older than
   ``--token-rotation-interval`` minutes (default: 50), or until the Gmail API
   returns a 401, whichever comes first.  On a 401 the token is refreshed once
   and the request is retried; if the retry also fails the script exits with an
   error.

   The cache file is stored at ``~/.config/gws/imapproc-token-cache.json``
   (configurable via ``--token-cache-file``) with mode 0600.  Concurrent
   invocations co-ordinate via ``fcntl.flock()`` on the cache file so that
   only one process refreshes the token at a time.

3. Posts a ``multipart/related`` request directly to the Gmail REST API upload
   endpoint (``https://gmail.googleapis.com/upload/gmail/v1/...``) using
   Python's standard ``urllib`` library — no third-party dependencies.

Usage
-----
Identical to gws-import-to-gmail.py::

    exec: ["scripts/gws-import-to-gmail-direct.py"]
    exec: ["scripts/gws-import-to-gmail-direct.py", "--user", "alice@example.com"]
    exec: ["scripts/gws-import-to-gmail-direct.py", "--never-mark-spam"]
    exec: ["scripts/gws-import-to-gmail-direct.py", "--token-rotation-interval", "30"]
    exec: ["scripts/gws-import-to-gmail-direct.py", "--token-cache-file", "/run/secrets/gmail-token.json"]

Prerequisites
-------------
``gws`` must be installed and ``gws auth login`` must have been run so that
credentials are stored in the encrypted gws credential store.
"""

import argparse
import fcntl
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

# Default token cache location (inside the gws config directory so the same
# volume/bind-mount covers both gws credentials and this cache).
_DEFAULT_TOKEN_CACHE = os.path.expanduser("~/.config/gws/imapproc-token-cache.json")

# Default number of minutes before proactively rotating the access token.
_DEFAULT_ROTATION_MINUTES = 50


# ---------------------------------------------------------------------------
# Tracing
# ---------------------------------------------------------------------------

def _trace(msg: str) -> None:
    """Print a trace line to stderr, prefixed with a marker for easy grepping."""
    print(f"[gws-import] {msg}", file=sys.stderr)


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
    parser.add_argument(
        "--token-rotation-interval",
        type=int,
        default=_DEFAULT_ROTATION_MINUTES,
        metavar="MINUTES",
        help=(
            f"Proactively rotate the cached access token after this many minutes "
            f"(default: {_DEFAULT_ROTATION_MINUTES})."
        ),
    )
    parser.add_argument(
        "--token-cache-file",
        default=_DEFAULT_TOKEN_CACHE,
        metavar="PATH",
        help=(
            f"Path to the JSON file used to cache the access token "
            f"(default: {_DEFAULT_TOKEN_CACHE})."
        ),
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

    _trace(f"token: exchanging refresh_token at {token_endpoint}")
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
    _trace("token: new access token obtained")
    return body["access_token"]


# ---------------------------------------------------------------------------
# Token cache helpers
# ---------------------------------------------------------------------------

def _ensure_cache_file(path: str) -> None:
    """Create an empty cache file with mode 0600 if it does not exist yet.

    We intentionally do not use O_EXCL / check-then-act atomically here
    because we accept the race on first creation — the worst outcome is two
    processes both writing an empty JSON object, which is harmless.
    """
    if not os.path.exists(path):
        _trace(f"token cache: creating new cache file {path}")
        fd = os.open(path, os.O_WRONLY | os.O_CREAT, 0o600)
        try:
            os.write(fd, b"{}")
        finally:
            os.close(fd)


def _load_cache(f) -> dict:
    """Read JSON from an open (and locked) cache file handle.

    Returns an empty dict if the file is empty or contains invalid JSON.
    """
    f.seek(0)
    raw = f.read()
    if not raw.strip():
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return {}


def _save_cache(path: str, data: dict) -> None:
    """Write *data* to *path* atomically using a .new temp file, mode 0600."""
    tmp_path = path + ".new"
    fd = os.open(tmp_path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    try:
        os.write(fd, json.dumps(data, indent=2).encode())
    finally:
        os.close(fd)
    os.replace(tmp_path, path)


def _is_token_fresh(cache: dict, rotation_minutes: int) -> bool:
    """Return True if the cached token was obtained recently enough."""
    obtained_at = cache.get("obtained_at")
    access_token = cache.get("access_token")
    if not obtained_at or not access_token:
        return False
    age_seconds = time.time() - obtained_at
    return age_seconds < rotation_minutes * 60


def _refresh_and_save(cache_path: str) -> str:
    """Obtain a fresh access token, persist it, and return it.

    Must be called while the caller holds the flock on the cache file.
    """
    env_token = os.environ.get("GOOGLE_WORKSPACE_CLI_TOKEN", "").strip()
    if env_token:
        # Env-injected token: skip the cache entirely (useful for CI/testing).
        _trace("token: using GOOGLE_WORKSPACE_CLI_TOKEN from environment")
        return env_token

    creds = load_gws_credentials()
    missing = [k for k in ("client_id", "client_secret", "refresh_token") if not creds.get(k)]
    if missing:
        raise RuntimeError(
            f"gws credentials are missing required fields: {missing}. "
            "Run 'gws auth login' to re-authenticate."
        )
    token = exchange_refresh_token(
        creds["client_id"],
        creds["client_secret"],
        creds["refresh_token"],
    )
    _save_cache(cache_path, {
        "access_token": token,
        "obtained_at": time.time(),
    })
    _trace(f"token: saved to cache {cache_path}")
    return token


def get_access_token(cache_path: str, rotation_minutes: int) -> str:
    """Return a valid access token, using the on-disk cache when possible.

    Checks ``GOOGLE_WORKSPACE_CLI_TOKEN`` first so the caller can inject a
    token directly (useful for testing and CI environments).

    Otherwise:
    1. Ensures the cache file exists (creating an empty one if needed).
    2. Acquires an exclusive flock on it.
    3. Reads the cache; if the token is fresh enough, returns it immediately.
    4. Otherwise refreshes the token via ``exchange_refresh_token``, persists
       the new token, and returns it.

    The flock is released when this function returns so that concurrent
    invocations serialize their refresh attempts.
    """
    env_token = os.environ.get("GOOGLE_WORKSPACE_CLI_TOKEN", "").strip()
    if env_token:
        _trace("token: using GOOGLE_WORKSPACE_CLI_TOKEN from environment (cache skipped)")
        return env_token

    _ensure_cache_file(cache_path)

    with open(cache_path, "r+") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        try:
            cache = _load_cache(f)
            if _is_token_fresh(cache, rotation_minutes):
                age_min = int((time.time() - cache["obtained_at"]) / 60)
                _trace(f"token: reusing cached token (age {age_min}m, rotation {rotation_minutes}m)")
                return cache["access_token"]
            _trace(f"token: cached token absent or stale, refreshing (rotation {rotation_minutes}m)")
            # Token is stale or absent — refresh under the lock so only one
            # concurrent process does the exchange.
            return _refresh_and_save(cache_path)
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)


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
) -> tuple[dict, bool]:
    """POST the email to the Gmail users.messages.import upload endpoint.

    Returns ``(parsed_response, token_was_rejected)`` where
    ``token_was_rejected`` is True when the server responded with 401.
    Raises ``RuntimeError`` on non-2xx responses other than 401.
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
    _trace(f"api: POST {url}")
    try:
        with urllib.request.urlopen(req) as resp:
            result = json.loads(resp.read())
            _trace(f"api: success, message id={result.get('id', '?')}")
            return result, False
    except urllib.error.HTTPError as exc:
        if exc.code == 401:
            _trace("api: 401 Unauthorized — token rejected")
            return {}, True
        error_body = exc.read().decode(errors="replace")
        _trace(f"api: error {exc.code}")
        # Print the API error to stdout so it is visible in imapproc logs,
        # matching the behaviour of gws which also writes its JSON output to
        # stdout.
        print(error_body)
        raise RuntimeError(
            f"Gmail API returned {exc.code}"
        ) from exc


def import_message_with_retry(
    cache_path: str,
    rotation_minutes: int,
    user_id: str,
    metadata: dict,
    raw_email: bytes,
    query_params: dict,
) -> dict:
    """Import a message, refreshing the token once on 401.

    1. Obtain (possibly cached) access token.
    2. Attempt import.
    3. On 401: force-refresh the token (under lock, with safe replace), retry.
    4. On second 401 or any other error: raise ``RuntimeError``.
    """
    access_token = get_access_token(cache_path, rotation_minutes)
    response, token_rejected = import_message(
        access_token, user_id, metadata, raw_email, query_params
    )
    if not token_rejected:
        return response

    # Token was rejected — force a refresh under lock.
    _trace("token: forcing refresh after 401, retrying request")
    _ensure_cache_file(cache_path)
    with open(cache_path, "r+") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        try:
            access_token = _refresh_and_save(cache_path)
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)

    response, token_rejected = import_message(
        access_token, user_id, metadata, raw_email, query_params
    )
    if token_rejected:
        raise RuntimeError(
            "Gmail API returned 401 even after token refresh; "
            "check gws credentials."
        )
    return response


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
        response = import_message_with_retry(
            args.token_cache_file,
            args.token_rotation_interval,
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
