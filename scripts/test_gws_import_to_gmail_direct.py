"""Tests for gws-import-to-gmail-direct.py."""

import importlib.util
import io
import json
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path
from unittest.mock import MagicMock, patch

import pytest

# ---------------------------------------------------------------------------
# Import the script as a module despite the hyphenated filename.
# ---------------------------------------------------------------------------

SCRIPT = Path(__file__).parent / "gws-import-to-gmail-direct.py"
spec = importlib.util.spec_from_file_location("gws_import_to_gmail_direct", SCRIPT)
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)

# ---------------------------------------------------------------------------
# Fixtures / helpers
# ---------------------------------------------------------------------------

SAMPLE_EMAIL = b"From: sender@example.com\r\nTo: me@example.com\r\nSubject: Hi\r\n\r\nHello.\r\n"

SAMPLE_CREDS = {
    "client_id": "test-client-id",
    "client_secret": "test-client-secret",
    "refresh_token": "test-refresh-token",
}


class _FakeStdin:
    """Minimal sys.stdin stand-in that exposes a .buffer attribute."""

    def __init__(self, data: bytes) -> None:
        self.buffer = io.BytesIO(data)


def run_dry(*args: str) -> str:
    """Run the script with --dry-run and return stdout."""
    result = subprocess.run(
        [sys.executable, str(SCRIPT), "--dry-run", *args],
        input=SAMPLE_EMAIL,
        capture_output=True,
    )
    assert result.returncode == 0, result.stderr.decode()
    return result.stdout.decode()


# ---------------------------------------------------------------------------
# Unit tests: build_multipart_body
# ---------------------------------------------------------------------------

class TestBuildMultipartBody:
    def test_content_type_header_contains_boundary(self):
        _, ct = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert ct.startswith("multipart/related; boundary=")

    def test_body_contains_json_part(self):
        metadata = {"labelIds": ["INBOX"]}
        body, _ = mod.build_multipart_body(metadata, SAMPLE_EMAIL)
        body_str = body.decode()
        assert "Content-Type: application/json" in body_str
        assert '"labelIds"' in body_str
        assert '"INBOX"' in body_str

    def test_body_contains_rfc822_part(self):
        body, _ = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert b"Content-Type: message/rfc822" in body
        assert SAMPLE_EMAIL in body

    def test_boundary_used_as_delimiter(self):
        body, ct = mod.build_multipart_body({}, SAMPLE_EMAIL)
        boundary = ct.split("boundary=")[1]
        assert boundary.encode() in body

    def test_closing_boundary_present(self):
        body, ct = mod.build_multipart_body({}, SAMPLE_EMAIL)
        boundary = ct.split("boundary=")[1]
        assert f"--{boundary}--".encode() in body

    def test_empty_metadata_serialises_to_empty_object(self):
        body, _ = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert b"{}" in body

    def test_boundary_is_unique_across_calls(self):
        _, ct1 = mod.build_multipart_body({}, SAMPLE_EMAIL)
        _, ct2 = mod.build_multipart_body({}, SAMPLE_EMAIL)
        assert ct1 != ct2


# ---------------------------------------------------------------------------
# Unit tests: exchange_refresh_token
# ---------------------------------------------------------------------------

class TestExchangeRefreshToken:
    def _make_response(self, body: dict, status: int = 200):
        data = json.dumps(body).encode()
        resp = MagicMock()
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        resp.read = MagicMock(return_value=data)
        resp.status = status
        return resp

    def test_returns_access_token_on_success(self):
        resp = self._make_response({"access_token": "ya29.fresh"})
        with patch("urllib.request.urlopen", return_value=resp):
            token = mod.exchange_refresh_token("cid", "csec", "rtoken")
        assert token == "ya29.fresh"

    def test_raises_on_missing_access_token(self):
        resp = self._make_response({"error": "invalid_grant"})
        with patch("urllib.request.urlopen", return_value=resp):
            with pytest.raises(RuntimeError, match="access_token"):
                mod.exchange_refresh_token("cid", "csec", "rtoken")

    def test_raises_on_http_error(self):
        http_err = urllib.error.HTTPError(
            url="https://oauth2.googleapis.com/token",
            code=400,
            msg="Bad Request",
            hdrs=None,
            fp=io.BytesIO(b'{"error":"invalid_client"}'),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            with pytest.raises(RuntimeError, match="Token exchange failed"):
                mod.exchange_refresh_token("cid", "csec", "rtoken")


# ---------------------------------------------------------------------------
# Unit tests: load_gws_credentials
# ---------------------------------------------------------------------------

class TestLoadGwsCredentials:
    def test_returns_parsed_json_on_success(self):
        result = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout=json.dumps(SAMPLE_CREDS).encode(),
            stderr=b"",
        )
        with patch("subprocess.run", return_value=result):
            creds = mod.load_gws_credentials()
        assert creds == SAMPLE_CREDS

    def test_raises_when_gws_not_found(self):
        with patch("subprocess.run", side_effect=FileNotFoundError):
            with pytest.raises(RuntimeError, match="gws not found"):
                mod.load_gws_credentials()

    def test_raises_on_nonzero_exit(self):
        exc = subprocess.CalledProcessError(1, "gws", stderr=b"no credentials")
        with patch("subprocess.run", side_effect=exc):
            with pytest.raises(RuntimeError, match="gws auth export failed"):
                mod.load_gws_credentials()

    def test_raises_on_invalid_json(self):
        result = subprocess.CompletedProcess(
            args=[], returncode=0, stdout=b"not-json", stderr=b"",
        )
        with patch("subprocess.run", return_value=result):
            with pytest.raises(RuntimeError, match="invalid JSON"):
                mod.load_gws_credentials()


# ---------------------------------------------------------------------------
# Unit tests: get_access_token
# ---------------------------------------------------------------------------

class TestGetAccessToken:
    def test_uses_env_var_when_set(self, monkeypatch):
        monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", "env-token")
        assert mod.get_access_token() == "env-token"

    def test_ignores_empty_env_var(self, monkeypatch):
        monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", "")
        with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="fresh"):
                token = mod.get_access_token()
        assert token == "fresh"

    def test_calls_exchange_with_correct_args(self, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="tok") as mock_ex:
                mod.get_access_token()
        mock_ex.assert_called_once_with(
            SAMPLE_CREDS["client_id"],
            SAMPLE_CREDS["client_secret"],
            SAMPLE_CREDS["refresh_token"],
        )

    def test_raises_when_credentials_missing_fields(self, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        with patch.object(mod, "load_gws_credentials", return_value={"client_id": "x"}):
            with pytest.raises(RuntimeError, match="missing required fields"):
                mod.get_access_token()


# ---------------------------------------------------------------------------
# Unit tests: import_message
# ---------------------------------------------------------------------------

class TestImportMessage:
    def _make_urlopen(self, response_body: dict):
        data = json.dumps(response_body).encode()
        resp = MagicMock()
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        resp.read = MagicMock(return_value=data)
        return MagicMock(return_value=resp)

    def test_returns_parsed_response(self):
        api_resp = {"id": "msg123", "threadId": "thread456"}
        with patch("urllib.request.urlopen", self._make_urlopen(api_resp)):
            result = mod.import_message("token", "me", {}, SAMPLE_EMAIL, {})
        assert result == api_resp

    def test_url_contains_user_id(self):
        captured = {}

        def fake_urlopen(req):
            captured["url"] = req.full_url
            resp = MagicMock()
            resp.__enter__ = lambda s: s
            resp.__exit__ = MagicMock(return_value=False)
            resp.read = MagicMock(return_value=b'{"id":"x"}')
            return resp

        with patch("urllib.request.urlopen", fake_urlopen):
            mod.import_message("token", "alice@example.com", {}, SAMPLE_EMAIL, {})

        assert "alice%40example.com" in captured["url"]

    def test_url_contains_upload_type(self):
        captured = {}

        def fake_urlopen(req):
            captured["url"] = req.full_url
            resp = MagicMock()
            resp.__enter__ = lambda s: s
            resp.__exit__ = MagicMock(return_value=False)
            resp.read = MagicMock(return_value=b'{"id":"x"}')
            return resp

        with patch("urllib.request.urlopen", fake_urlopen):
            mod.import_message("token", "me", {}, SAMPLE_EMAIL, {})

        assert "uploadType=multipart" in captured["url"]

    def test_query_params_forwarded(self):
        captured = {}

        def fake_urlopen(req):
            captured["url"] = req.full_url
            resp = MagicMock()
            resp.__enter__ = lambda s: s
            resp.__exit__ = MagicMock(return_value=False)
            resp.read = MagicMock(return_value=b'{"id":"x"}')
            return resp

        with patch("urllib.request.urlopen", fake_urlopen):
            mod.import_message("token", "me", {}, SAMPLE_EMAIL,
                               {"neverMarkSpam": "true"})

        assert "neverMarkSpam=true" in captured["url"]

    def test_raises_on_http_error(self, capsys):
        http_err = urllib.error.HTTPError(
            url="https://gmail.googleapis.com/upload/...",
            code=400,
            msg="Bad Request",
            hdrs=None,
            fp=io.BytesIO(b'{"error":{"code":400,"message":"bad"}}'),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            with pytest.raises(RuntimeError, match="Gmail API returned 400"):
                mod.import_message("token", "me", {}, SAMPLE_EMAIL, {})
        # API error body should have been printed to stdout
        captured = capsys.readouterr()
        assert "bad" in captured.out

    def test_authorization_header_set(self):
        captured = {}

        def fake_urlopen(req):
            captured["headers"] = dict(req.headers)
            resp = MagicMock()
            resp.__enter__ = lambda s: s
            resp.__exit__ = MagicMock(return_value=False)
            resp.read = MagicMock(return_value=b'{"id":"x"}')
            return resp

        with patch("urllib.request.urlopen", fake_urlopen):
            mod.import_message("my-token", "me", {}, SAMPLE_EMAIL, {})

        assert captured["headers"].get("Authorization") == "Bearer my-token"


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

class TestDryRun:
    def test_default_output_contains_post(self):
        out = run_dry()
        assert "POST" in out

    def test_default_output_contains_user_me(self):
        out = run_dry()
        assert "/users/me/" in out

    def test_default_output_contains_unread_and_inbox(self):
        out = run_dry()
        assert "UNREAD" in out
        assert "INBOX" in out

    def test_default_output_contains_rfc822(self):
        out = run_dry()
        assert "message/rfc822" in out

    def test_default_output_no_raw_field(self):
        """Email bytes must never appear in the dry-run output as base64."""
        out = run_dry()
        assert '"raw"' not in out

    def test_custom_user(self):
        out = run_dry("--user", "alice@example.com")
        assert "alice%40example.com" in out

    def test_never_mark_spam(self):
        out = run_dry("--never-mark-spam")
        assert "neverMarkSpam=true" in out

    def test_process_for_calendar(self):
        out = run_dry("--process-for-calendar")
        assert "processForCalendar=true" in out

    def test_deleted(self):
        out = run_dry("--deleted")
        assert "deleted=true" in out

    def test_internal_date_source(self):
        out = run_dry("--internal-date-source", "receivedTime")
        assert "receivedTime" in out

    def test_do_not_mark_unread_suppresses_unread(self):
        out = run_dry("--do-not-mark-unread")
        assert "UNREAD" not in out

    def test_archive_suppresses_inbox(self):
        out = run_dry("--archive")
        assert "INBOX" not in out

    def test_archive_and_do_not_mark_unread(self):
        out = run_dry("--archive", "--do-not-mark-unread")
        assert "UNREAD" not in out
        assert "INBOX" not in out

    def test_extra_label_included(self):
        out = run_dry("--add-label-id", "Label_abc")
        assert "Label_abc" in out

    def test_email_size_reported(self):
        out = run_dry()
        assert str(len(SAMPLE_EMAIL)) in out


# ---------------------------------------------------------------------------
# Integration tests: empty stdin
# ---------------------------------------------------------------------------

class TestEmptyStdin:
    def test_empty_stdin_exits_nonzero(self):
        result = subprocess.run(
            [sys.executable, str(SCRIPT)],
            input=b"",
            capture_output=True,
        )
        assert result.returncode != 0
        assert b"no email data received on stdin" in result.stderr


# ---------------------------------------------------------------------------
# Integration tests: main() with mocked API
# ---------------------------------------------------------------------------

class TestMain:
    def _run_main(self, monkeypatch, extra_argv=None, env_token="test-token"):
        monkeypatch.setattr("sys.stdin", _FakeStdin(SAMPLE_EMAIL))
        monkeypatch.setattr("sys.argv", ["gws-import-to-gmail-direct.py", *(extra_argv or [])])
        if env_token:
            monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", env_token)
        return mod.main()

    def test_success_returns_zero(self, monkeypatch):
        api_resp = {"id": "msg1", "threadId": "t1"}
        with patch.object(mod, "import_message", return_value=api_resp):
            rc = self._run_main(monkeypatch)
        assert rc == 0

    def test_runtime_error_returns_one(self, monkeypatch):
        with patch.object(mod, "import_message", side_effect=RuntimeError("API error")):
            rc = self._run_main(monkeypatch)
        assert rc == 1

    def test_labels_passed_to_import(self, monkeypatch):
        captured = {}

        def fake_import(token, user, metadata, raw, query):
            captured["metadata"] = metadata
            captured["query"] = query
            return {"id": "x"}

        with patch.object(mod, "import_message", side_effect=fake_import):
            self._run_main(monkeypatch, extra_argv=["--add-label-id", "Label_X"])

        assert "Label_X" in captured["metadata"].get("labelIds", [])

    def test_never_mark_spam_in_query(self, monkeypatch):
        captured = {}

        def fake_import(token, user, metadata, raw, query):
            captured["query"] = query
            return {"id": "x"}

        with patch.object(mod, "import_message", side_effect=fake_import):
            self._run_main(monkeypatch, extra_argv=["--never-mark-spam"])

        assert captured["query"].get("neverMarkSpam") == "true"

    def test_raw_email_passed_to_import(self, monkeypatch):
        captured = {}

        def fake_import(token, user, metadata, raw, query):
            captured["raw"] = raw
            return {"id": "x"}

        with patch.object(mod, "import_message", side_effect=fake_import):
            self._run_main(monkeypatch)

        assert captured["raw"] == SAMPLE_EMAIL
