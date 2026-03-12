#!/usr/bin/env python3
"""Nagios/Icinga check for go-imapproc's /api/health endpoint.

Exit codes follow the Nagios plugin standard:
  0 — OK
  1 — WARNING
  2 — CRITICAL
  3 — UNKNOWN

Status mapping
--------------
CRITICAL  — IMAP connection is not UP (``details.connection.status != "UP"``).
WARNING   — Connection is UP but the last poll reported at least one failed
            message (``details.lastPoll.messagesFailed > 0``).
OK        — Connection is UP and no failures in the last poll.
UNKNOWN   — The endpoint could not be reached, returned unexpected data,
            or the HTTP response could not be parsed.

Usage
-----
    check_imapproc.py --url http://localhost:8080/api/health
    check_imapproc.py --url http://host:8080/api/health --timeout 10
"""

import argparse
import json
import sys
import urllib.error
import urllib.request

# ---------------------------------------------------------------------------
# Nagios exit codes
# ---------------------------------------------------------------------------

STATE_OK = 0
STATE_WARNING = 1
STATE_CRITICAL = 2
STATE_UNKNOWN = 3

_STATE_LABELS = {
    STATE_OK: "OK",
    STATE_WARNING: "WARNING",
    STATE_CRITICAL: "CRITICAL",
    STATE_UNKNOWN: "UNKNOWN",
}

# Default request timeout in seconds.
_DEFAULT_TIMEOUT = 10


# ---------------------------------------------------------------------------
# Argument parser
# ---------------------------------------------------------------------------

def build_argument_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        description="Nagios check for go-imapproc /api/health.",
    )
    parser.add_argument(
        "--url",
        required=True,
        metavar="URL",
        help="Full URL of the /api/health endpoint (e.g. http://localhost:8080/api/health).",
    )
    parser.add_argument(
        "--timeout",
        type=float,
        default=_DEFAULT_TIMEOUT,
        metavar="SECONDS",
        help=f"HTTP request timeout in seconds (default: {_DEFAULT_TIMEOUT}).",
    )
    return parser


# ---------------------------------------------------------------------------
# HTTP fetch
# ---------------------------------------------------------------------------

def fetch_health(url: str, timeout: float) -> tuple[int, dict]:
    """Fetch the health endpoint and return ``(http_status_code, parsed_json)``.

    Raises ``urllib.error.URLError`` on network / connection errors.
    Raises ``ValueError`` if the response body is not valid JSON.
    """
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            body = resp.read()
            return resp.status, json.loads(body)
    except urllib.error.HTTPError as exc:
        # HTTPError also has a body; try to parse it for richer diagnostics.
        body = exc.read()
        try:
            return exc.code, json.loads(body)
        except (json.JSONDecodeError, ValueError):
            raise ValueError(
                f"HTTP {exc.code}: response body is not valid JSON"
            ) from exc


# ---------------------------------------------------------------------------
# Check logic
# ---------------------------------------------------------------------------

def evaluate_health(data: dict) -> tuple[int, str]:
    """Derive Nagios state and a human-readable message from parsed JSON.

    Returns ``(nagios_state, message)``.

    Rules (evaluated in priority order):
    1. CRITICAL  — ``details.connection.status`` is not ``"UP"``.
    2. WARNING   — ``details.lastPoll.messagesFailed > 0``.
    3. OK        — everything else.

    All numeric performance data is appended as a Nagios perfdata string.
    """
    try:
        conn_status = data["details"]["connection"]["status"]
        last_poll = data["details"]["lastPoll"]
        poll_failed = last_poll.get("messagesFailed", 0)
        poll_received = last_poll.get("messagesReceived", 0)
        poll_success = last_poll.get("messagesSuccess", 0)
        poll_ts = last_poll.get("timestamp")
        stats = data.get("stats", {})
        total_received = stats.get("messagesReceived", 0)
        total_success = stats.get("messagesSuccess", 0)
        total_failed = stats.get("messagesFailed", 0)
    except (KeyError, TypeError) as exc:
        return STATE_UNKNOWN, f"Unexpected response structure: {exc}"

    # --- CRITICAL: not connected to IMAP ---
    if conn_status != "UP":
        msg = f"IMAP connection is {conn_status}"
        perfdata = _perfdata(
            poll_received, poll_success, poll_failed,
            total_received, total_success, total_failed,
        )
        return STATE_CRITICAL, f"{msg} | {perfdata}"

    # --- WARNING: failed mails in the last run ---
    if poll_failed > 0:
        poll_info = (
            f"last poll: {poll_failed} failed, {poll_success} ok"
            + (f" (at {poll_ts})" if poll_ts else "")
        )
        perfdata = _perfdata(
            poll_received, poll_success, poll_failed,
            total_received, total_success, total_failed,
        )
        return STATE_WARNING, f"{poll_info} | {perfdata}"

    # --- OK ---
    if poll_ts:
        ok_info = f"IMAP UP, last poll ok ({poll_success} ok at {poll_ts})"
    else:
        ok_info = "IMAP UP, no poll yet"
    perfdata = _perfdata(
        poll_received, poll_success, poll_failed,
        total_received, total_success, total_failed,
    )
    return STATE_OK, f"{ok_info} | {perfdata}"


def _perfdata(
    poll_received: int,
    poll_success: int,
    poll_failed: int,
    total_received: int,
    total_success: int,
    total_failed: int,
) -> str:
    """Format Nagios performance data string."""
    return (
        f"poll_received={poll_received} poll_success={poll_success} "
        f"poll_failed={poll_failed} "
        f"total_received={total_received} total_success={total_success} "
        f"total_failed={total_failed}"
    )


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    args = build_argument_parser().parse_args()

    try:
        _status_code, data = fetch_health(args.url, args.timeout)
    except urllib.error.URLError as exc:
        print(f"UNKNOWN: cannot reach {args.url}: {exc.reason}")
        return STATE_UNKNOWN
    except (ValueError, json.JSONDecodeError) as exc:
        print(f"UNKNOWN: invalid response from {args.url}: {exc}")
        return STATE_UNKNOWN
    except OSError as exc:
        print(f"UNKNOWN: network error reaching {args.url}: {exc}")
        return STATE_UNKNOWN

    state, message = evaluate_health(data)
    label = _STATE_LABELS.get(state, "UNKNOWN")
    print(f"{label}: {message}")
    return state


if __name__ == "__main__":
    sys.exit(main())
