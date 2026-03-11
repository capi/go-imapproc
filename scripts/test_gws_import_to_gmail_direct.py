"""Tests for gws-import-to-gmail-direct.py."""

import importlib.util
import io
import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path
from unittest.mock import MagicMock, call, patch

import pytest

# ---------------------------------------------------------------------------
# Import the script as a module despite the hyphenated filename.
# ---------------------------------------------------------------------------

SCRIPT = Path(__file__).parent / "gws-import-to-gmail-direct.py"
spec = importlib.util.spec_from_file_location("gws_import_to_gmail_direct", SCRIPT)
assert spec is not None and spec.loader is not None
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)  # type: ignore[union-attr]

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
            hdrs=MagicMock(),
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
# Unit tests: token cache helpers
# ---------------------------------------------------------------------------

class TestIsTokenFresh:
    def test_fresh_token_returns_true(self):
        cache = {"access_token": "tok", "obtained_at": time.time() - 60}
        assert mod._is_token_fresh(cache, rotation_minutes=50) is True

    def test_stale_token_returns_false(self):
        cache = {"access_token": "tok", "obtained_at": time.time() - 3200}
        assert mod._is_token_fresh(cache, rotation_minutes=50) is False

    def test_missing_obtained_at_returns_false(self):
        assert mod._is_token_fresh({"access_token": "tok"}, rotation_minutes=50) is False

    def test_missing_access_token_returns_false(self):
        assert mod._is_token_fresh({"obtained_at": time.time()}, rotation_minutes=50) is False

    def test_empty_cache_returns_false(self):
        assert mod._is_token_fresh({}, rotation_minutes=50) is False

    def test_custom_rotation_interval_respected(self):
        # 5 minutes old, rotation=3 → stale
        cache = {"access_token": "tok", "obtained_at": time.time() - 300}
        assert mod._is_token_fresh(cache, rotation_minutes=3) is False
        # 5 minutes old, rotation=10 → fresh
        assert mod._is_token_fresh(cache, rotation_minutes=10) is True


class TestLoadCache:
    def test_returns_empty_dict_on_empty_file(self, tmp_path):
        f = (tmp_path / "cache.json").open("w+")
        assert mod._load_cache(f) == {}
        f.close()

    def test_returns_parsed_data(self, tmp_path):
        p = tmp_path / "cache.json"
        p.write_text('{"access_token":"tok","obtained_at":1234567890}')
        with p.open("r+") as f:
            result = mod._load_cache(f)
        assert result == {"access_token": "tok", "obtained_at": 1234567890}

    def test_returns_empty_dict_on_invalid_json(self, tmp_path):
        p = tmp_path / "cache.json"
        p.write_text("not-json")
        with p.open("r+") as f:
            result = mod._load_cache(f)
        assert result == {}


class TestSaveCache:
    def test_writes_json_to_file(self, tmp_path):
        p = tmp_path / "cache.json"
        data = {"access_token": "tok", "obtained_at": 1234567890.0}
        mod._save_cache(str(p), data)
        assert json.loads(p.read_text()) == data

    def test_file_permissions_are_0600(self, tmp_path):
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": 0.0})
        mode = oct(p.stat().st_mode & 0o777)
        assert mode == oct(0o600)

    def test_no_temp_file_left_behind(self, tmp_path):
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": 0.0})
        assert not (tmp_path / "cache.json.new").exists()

    def test_atomic_replace_overwrites_existing(self, tmp_path):
        p = tmp_path / "cache.json"
        p.write_text('{"access_token":"old","obtained_at":0}')
        mod._save_cache(str(p), {"access_token": "new", "obtained_at": 1.0})
        assert json.loads(p.read_text())["access_token"] == "new"


class TestEnsureCacheFile:
    def test_creates_file_if_absent(self, tmp_path):
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        assert p.exists()

    def test_created_file_has_0600_permissions(self, tmp_path):
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        assert oct(p.stat().st_mode & 0o777) == oct(0o600)

    def test_does_not_overwrite_existing_file(self, tmp_path):
        p = tmp_path / "cache.json"
        p.write_text('{"access_token":"existing","obtained_at":1}')
        mod._ensure_cache_file(str(p))
        assert "existing" in p.read_text()


# ---------------------------------------------------------------------------
# Unit tests: get_access_token
# ---------------------------------------------------------------------------

class TestGetAccessToken:
    def test_uses_env_var_when_set(self, monkeypatch, tmp_path):
        monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", "env-token")
        token = mod.get_access_token(str(tmp_path / "cache.json"), 50)
        assert token == "env-token"

    def test_uses_cached_token_when_fresh(self, monkeypatch, tmp_path):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "cached-tok", "obtained_at": time.time() - 60})
        token = mod.get_access_token(str(p), 50)
        assert token == "cached-tok"

    def test_refreshes_when_cache_is_stale(self, monkeypatch, tmp_path):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old-tok", "obtained_at": time.time() - 9999})
        with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                token = mod.get_access_token(str(p), 50)
        assert token == "new-tok"

    def test_refreshes_when_no_cache_file(self, monkeypatch, tmp_path):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="brand-new"):
                token = mod.get_access_token(str(p), 50)
        assert token == "brand-new"

    def test_persists_new_token_to_cache(self, monkeypatch, tmp_path):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="saved-tok"):
                mod.get_access_token(str(p), 50)
        cache = json.loads(p.read_text())
        assert cache["access_token"] == "saved-tok"
        assert "obtained_at" in cache

    def test_raises_when_credentials_missing_fields(self, monkeypatch, tmp_path):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        with patch.object(mod, "load_gws_credentials", return_value={"client_id": "x"}):
            with pytest.raises(RuntimeError, match="missing required fields"):
                mod.get_access_token(str(p), 50)


# ---------------------------------------------------------------------------
# Unit tests: import_message (now returns (dict, bool))
# ---------------------------------------------------------------------------

class TestImportMessage:
    def _make_urlopen(self, response_body: dict):
        data = json.dumps(response_body).encode()
        resp = MagicMock()
        resp.__enter__ = lambda s: s
        resp.__exit__ = MagicMock(return_value=False)
        resp.read = MagicMock(return_value=data)
        return MagicMock(return_value=resp)

    def test_returns_parsed_response_and_false(self):
        api_resp = {"id": "msg123", "threadId": "thread456"}
        with patch("urllib.request.urlopen", self._make_urlopen(api_resp)):
            result, rejected = mod.import_message("token", "me", {}, SAMPLE_EMAIL, {})
        assert result == api_resp
        assert rejected is False

    def test_returns_empty_dict_and_true_on_401(self):
        http_err = urllib.error.HTTPError(
            url="https://gmail.googleapis.com/...",
            code=401,
            msg="Unauthorized",
            hdrs=MagicMock(),
            fp=io.BytesIO(b'{"error":"invalid_credentials"}'),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            result, rejected = mod.import_message("token", "me", {}, SAMPLE_EMAIL, {})
        assert result == {}
        assert rejected is True

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

    def test_raises_on_non_401_http_error(self, capsys):
        http_err = urllib.error.HTTPError(
            url="https://gmail.googleapis.com/upload/...",
            code=400,
            msg="Bad Request",
            hdrs=MagicMock(),
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
# Unit tests: import_message_with_retry
# ---------------------------------------------------------------------------

class TestImportMessageWithRetry:
    def test_success_on_first_attempt(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": time.time() - 60})

        with patch.object(mod, "import_message", return_value=({"id": "x"}, False)) as m:
            result = mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})

        assert result == {"id": "x"}
        assert m.call_count == 1

    def test_retries_on_401_with_fresh_token(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old-tok", "obtained_at": time.time() - 60})

        call_results = [({}, True), ({"id": "retried"}, False)]

        with patch.object(mod, "import_message", side_effect=call_results):
            with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
                with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                    result = mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})

        assert result == {"id": "retried"}

    def test_persists_refreshed_token_after_401(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old-tok", "obtained_at": time.time() - 60})

        with patch.object(mod, "import_message", side_effect=[({}, True), ({"id": "x"}, False)]):
            with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
                with patch.object(mod, "exchange_refresh_token", return_value="persisted-tok"):
                    mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})

        cache = json.loads(p.read_text())
        assert cache["access_token"] == "persisted-tok"

    def test_raises_on_second_401(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": time.time() - 60})

        with patch.object(mod, "import_message", return_value=({}, True)):
            with patch.object(mod, "load_gws_credentials", return_value=SAMPLE_CREDS):
                with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                    with pytest.raises(RuntimeError, match="401 even after token refresh"):
                        mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})


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

    def test_token_rotation_interval_default(self):
        assert self.parse().token_rotation_interval == mod._DEFAULT_ROTATION_MINUTES

    def test_token_rotation_interval_custom(self):
        assert self.parse("--token-rotation-interval", "30").token_rotation_interval == 30

    def test_token_cache_file_default(self):
        assert self.parse().token_cache_file == mod._DEFAULT_TOKEN_CACHE

    def test_token_cache_file_custom(self):
        assert self.parse("--token-cache-file", "/tmp/my-cache.json").token_cache_file == "/tmp/my-cache.json"

    def test_token_rotation_interval_must_be_int(self):
        with pytest.raises(SystemExit):
            self.parse("--token-rotation-interval", "50m")


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
    def _run_main(self, monkeypatch, extra_argv=None, env_token="test-token", tmp_path=None):
        monkeypatch.setattr("sys.stdin", _FakeStdin(SAMPLE_EMAIL))
        argv = ["gws-import-to-gmail-direct.py"]
        if tmp_path is not None:
            argv += ["--token-cache-file", str(tmp_path / "cache.json")]
        argv += extra_argv or []
        monkeypatch.setattr("sys.argv", argv)
        if env_token:
            monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", env_token)
        return mod.main()

    def test_success_returns_zero(self, monkeypatch, tmp_path):
        api_resp = {"id": "msg1", "threadId": "t1"}
        with patch.object(mod, "import_message_with_retry", return_value=api_resp):
            rc = self._run_main(monkeypatch, tmp_path=tmp_path)
        assert rc == 0

    def test_runtime_error_returns_one(self, monkeypatch, tmp_path):
        with patch.object(mod, "import_message_with_retry", side_effect=RuntimeError("API error")):
            rc = self._run_main(monkeypatch, tmp_path=tmp_path)
        assert rc == 1

    def test_labels_passed_to_import(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query):
            captured["metadata"] = metadata
            captured["query"] = query
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(monkeypatch, extra_argv=["--add-label-id", "Label_X"], tmp_path=tmp_path)

        assert "Label_X" in captured["metadata"].get("labelIds", [])

    def test_never_mark_spam_in_query(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query):
            captured["query"] = query
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(monkeypatch, extra_argv=["--never-mark-spam"], tmp_path=tmp_path)

        assert captured["query"].get("neverMarkSpam") == "true"

    def test_raw_email_passed_to_import(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query):
            captured["raw"] = raw
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(monkeypatch, tmp_path=tmp_path)

        assert captured["raw"] == SAMPLE_EMAIL

    def test_token_rotation_interval_forwarded(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query):
            captured["rotation"] = rotation
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(
                monkeypatch,
                extra_argv=["--token-rotation-interval", "30"],
                tmp_path=tmp_path,
            )

        assert captured["rotation"] == 30

    def test_token_cache_file_forwarded(self, monkeypatch, tmp_path):
        captured = {}
        custom_cache = str(tmp_path / "custom.json")

        def fake_import(cache_path, rotation, user, metadata, raw, query):
            captured["cache_path"] = cache_path
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(
                monkeypatch,
                extra_argv=["--token-cache-file", custom_cache],
                # Don't pass tmp_path so the argv doesn't add another --token-cache-file
            )

        assert captured["cache_path"] == custom_cache
