"""Tests for Gmail API import functions."""

import io
import json
import time
import urllib.error
from unittest.mock import MagicMock, call, patch

import pytest

from conftest import SAMPLE_CREDS, SAMPLE_EMAIL, mod


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


class TestImportMessageWithRetry:
    def _clear_env_tokens(self, monkeypatch):
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)

    def test_success_on_first_attempt(self, tmp_path, monkeypatch):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        rt_hash = mod._sha256(SAMPLE_CREDS["refresh_token"])
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": time.time() - 60,
                                  "refresh_token_sha256": rt_hash})

        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "import_message", return_value=({"id": "x"}, False)) as m:
                result = mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})

        assert result == {"id": "x"}
        assert m.call_count == 1

    def test_retries_on_401_with_fresh_token(self, tmp_path, monkeypatch):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old-tok", "obtained_at": time.time() - 60})

        call_results = [({}, True), ({"id": "retried"}, False)]

        with patch.object(mod, "import_message", side_effect=call_results):
            with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
                with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                    result = mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})

        assert result == {"id": "retried"}

    def test_persists_refreshed_token_after_401(self, tmp_path, monkeypatch):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old-tok", "obtained_at": time.time() - 60})

        with patch.object(mod, "import_message", side_effect=[({}, True), ({"id": "x"}, False)]):
            with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
                with patch.object(mod, "exchange_refresh_token", return_value="persisted-tok"):
                    mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})

        cache = json.loads(p.read_text())
        assert cache["access_token"] == "persisted-tok"

    def test_raises_on_second_401(self, tmp_path, monkeypatch):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": time.time() - 60})

        with patch.object(mod, "import_message", return_value=({}, True)):
            with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
                with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                    with pytest.raises(RuntimeError, match="401 even after token refresh"):
                        mod.import_message_with_retry(str(p), 50, "me", {}, SAMPLE_EMAIL, {})


class TestCredentialsProviderThreadingGmailApi:
    """credentials_provider threading tests for import_message_with_retry."""

    def test_import_message_with_retry_accepts_credentials_provider(self, tmp_path, monkeypatch):
        """import_message_with_retry must accept and use credentials_provider."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        rt_hash = mod._sha256(SAMPLE_CREDS["refresh_token"])
        mod._save_cache(str(p), {"access_token": "tok", "obtained_at": time.time() - 60,
                                  "refresh_token_sha256": rt_hash})

        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS) as mock_lc:
            with patch.object(mod, "import_message", return_value=({"id": "x"}, False)):
                result = mod.import_message_with_retry(
                    str(p), 50, "me", {}, SAMPLE_EMAIL, {},
                    credentials_provider="my-provider get-creds",
                )
        assert result == {"id": "x"}
        mock_lc.assert_called_with("my-provider get-creds")

    def test_import_message_with_retry_uses_provider_on_401(self, tmp_path, monkeypatch):
        """On 401, import_message_with_retry must pass credentials_provider to load_credentials."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old", "obtained_at": time.time() - 60})

        call_results = [({}, True), ({"id": "retried"}, False)]

        with patch.object(mod, "import_message", side_effect=call_results):
            with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS) as mock_lc:
                with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                    mod.import_message_with_retry(
                        str(p), 50, "me", {}, SAMPLE_EMAIL, {},
                        credentials_provider="my-provider get-creds",
                    )
        # load_credentials must have been called with the custom provider each time.
        for call_args in mock_lc.call_args_list:
            assert call_args == call("my-provider get-creds")
