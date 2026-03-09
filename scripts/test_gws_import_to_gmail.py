"""Tests for gws-import-to-gmail.py.

The script is imported as a module (sys.path manipulation below) so we can
unit-test its internal helpers and argument parser directly, and run a small
set of integration-style tests via subprocess --dry-run for full end-to-end
argument parsing.
"""

import base64
import importlib.util
import json
import subprocess
import sys
from pathlib import Path

import pytest

# ---------------------------------------------------------------------------
# Import the script as a module despite the hyphenated filename.
# ---------------------------------------------------------------------------

SCRIPT = Path(__file__).parent / "gws-import-to-gmail.py"
spec = importlib.util.spec_from_file_location("gws_import_to_gmail", SCRIPT)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

SAMPLE_EMAIL = b"From: sender@example.com\r\nTo: me@example.com\r\nSubject: Hi\r\n\r\nHello.\r\n"


def run_dry(*args: str) -> str:
    """Run the script with --dry-run and return the printed command line."""
    result = subprocess.run(
        [sys.executable, str(SCRIPT), "--dry-run", *args],
        input=SAMPLE_EMAIL,
        capture_output=True,
    )
    assert result.returncode == 0, result.stderr.decode()
    return result.stdout.decode().strip()


def parse_dry(*args: str) -> tuple[dict, dict]:
    """Run --dry-run and parse the --params and --json values from the output."""
    line = run_dry(*args)
    # The command is:  gws ... --params '<json>' --json '<json>'
    tokens = line.split("--params ", 1)[1]
    params_str, rest = tokens.split(" --json ", 1)
    body_str = rest.strip()
    return json.loads(params_str.strip("'")), json.loads(body_str.strip("'"))


# ---------------------------------------------------------------------------
# Unit tests: base64url_encode
# ---------------------------------------------------------------------------

class TestBase64urlEncode:
    def test_no_padding(self):
        """Encoded output must never contain '=' padding characters."""
        for length in range(1, 20):
            encoded = mod.base64url_encode(b"x" * length)
            assert "=" not in encoded

    def test_url_safe_alphabet(self):
        """Must use '-' and '_' instead of '+' and '/'."""
        # 0xFB = 11111011 → produces '+' in standard base64
        data = bytes([0xFB, 0xFF, 0xFE])
        encoded = mod.base64url_encode(data)
        assert "+" not in encoded
        assert "/" not in encoded

    def test_round_trip(self):
        """Decoding the output (with padding restored) must yield the original."""
        data = SAMPLE_EMAIL
        encoded = mod.base64url_encode(data)
        # Restore padding for standard decoder
        padded = encoded + "=" * (-len(encoded) % 4)
        assert base64.urlsafe_b64decode(padded) == data


# ---------------------------------------------------------------------------
# Unit tests: argument parser defaults
# ---------------------------------------------------------------------------

class TestArgumentParserDefaults:
    def setup_method(self):
        self.parser = mod.build_argument_parser()

    def parse(self, *args):
        return self.parser.parse_args(list(args))

    def test_user_default(self):
        assert self.parse().user == "me"

    def test_boolean_flags_default_false(self):
        args = self.parse()
        assert args.never_mark_spam is False
        assert args.process_for_calendar is False
        assert args.deleted is False
        assert args.do_not_mark_unread is False
        assert args.archive is False
        assert args.dry_run is False

    def test_labels_default_none(self):
        assert self.parse().labels is None

    def test_internal_date_source_default_none(self):
        assert self.parse().internal_date_source is None

    def test_internal_date_source_choices(self):
        assert self.parse("--internal-date-source", "dateHeader").internal_date_source == "dateHeader"
        assert self.parse("--internal-date-source", "receivedTime").internal_date_source == "receivedTime"

    def test_internal_date_source_invalid(self):
        with pytest.raises(SystemExit):
            self.parse("--internal-date-source", "bogus")

    def test_add_label_id_repeatable(self):
        args = self.parse("--add-label-id", "INBOX", "--add-label-id", "Label_123")
        assert args.labels == ["INBOX", "Label_123"]


# ---------------------------------------------------------------------------
# Integration tests via --dry-run subprocess
# ---------------------------------------------------------------------------

class TestDryRunDefaults:
    def test_default_user_is_me(self):
        params, _ = parse_dry()
        assert params["userId"] == "me"

    def test_default_labels_contain_unread_and_inbox(self):
        _, body = parse_dry()
        assert "UNREAD" in body["labelIds"]
        assert "INBOX" in body["labelIds"]

    def test_raw_field_present_and_decodable(self):
        _, body = parse_dry()
        raw = body["raw"]
        padded = raw + "=" * (-len(raw) % 4)
        assert base64.urlsafe_b64decode(padded) == SAMPLE_EMAIL

    def test_no_extra_params_by_default(self):
        params, _ = parse_dry()
        assert set(params.keys()) == {"userId"}


class TestDryRunLabels:
    def test_do_not_mark_unread_suppresses_unread(self):
        _, body = parse_dry("--do-not-mark-unread")
        assert "UNREAD" not in body.get("labelIds", [])

    def test_archive_suppresses_inbox(self):
        _, body = parse_dry("--archive")
        assert "INBOX" not in body.get("labelIds", [])

    def test_archive_and_do_not_mark_unread_produces_no_label_ids(self):
        _, body = parse_dry("--archive", "--do-not-mark-unread")
        assert "labelIds" not in body

    def test_inbox_not_duplicated_when_supplied_via_add_label_id(self):
        _, body = parse_dry("--add-label-id", "INBOX")
        assert body["labelIds"].count("INBOX") == 1

    def test_unread_not_duplicated_when_supplied_via_add_label_id(self):
        _, body = parse_dry("--add-label-id", "UNREAD")
        assert body["labelIds"].count("UNREAD") == 1

    def test_extra_label_is_included(self):
        _, body = parse_dry("--add-label-id", "Label_abc123")
        assert "Label_abc123" in body["labelIds"]

    def test_multiple_extra_labels(self):
        _, body = parse_dry("--add-label-id", "Label_1", "--add-label-id", "Label_2")
        assert "Label_1" in body["labelIds"]
        assert "Label_2" in body["labelIds"]


class TestDryRunParams:
    def test_custom_user(self):
        params, _ = parse_dry("--user", "alice@example.com")
        assert params["userId"] == "alice@example.com"

    def test_never_mark_spam(self):
        params, _ = parse_dry("--never-mark-spam")
        assert params["neverMarkSpam"] is True

    def test_process_for_calendar(self):
        params, _ = parse_dry("--process-for-calendar")
        assert params["processForCalendar"] is True

    def test_deleted(self):
        params, _ = parse_dry("--deleted")
        assert params["deleted"] is True

    def test_internal_date_source_date_header(self):
        params, _ = parse_dry("--internal-date-source", "dateHeader")
        assert params["internalDateSource"] == "dateHeader"

    def test_internal_date_source_received_time(self):
        params, _ = parse_dry("--internal-date-source", "receivedTime")
        assert params["internalDateSource"] == "receivedTime"

    def test_combined_params(self):
        params, _ = parse_dry(
            "--user", "bob@example.com",
            "--never-mark-spam",
            "--process-for-calendar",
        )
        assert params["userId"] == "bob@example.com"
        assert params["neverMarkSpam"] is True
        assert params["processForCalendar"] is True


class TestEmptyStdin:
    def test_empty_stdin_exits_nonzero(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT)],
            input=b"",
            capture_output=True,
        )
        assert result.returncode != 0
        assert b"no email data received on stdin" in result.stderr
