"""Tests for OAuth token exchange, refresh-and-save, and SHA-256 helpers."""

import hashlib
import io
import json
import time
import urllib.error
from unittest.mock import MagicMock, patch

import pytest

from conftest import SAMPLE_CREDS, mod


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

    def test_raises_invalid_refresh_token_error_on_invalid_grant(self):
        http_err = urllib.error.HTTPError(
            url="https://oauth2.googleapis.com/token",
            code=400,
            msg="Bad Request",
            hdrs=MagicMock(),
            fp=io.BytesIO(b'{"error":"invalid_grant","error_description":"Token has been expired or revoked."}'),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            with pytest.raises(mod.InvalidRefreshTokenError, match="Token exchange failed"):
                mod.exchange_refresh_token("cid", "csec", "rtoken")

    def test_invalid_grant_is_subclass_of_runtime_error(self):
        """InvalidRefreshTokenError must be catchable as RuntimeError."""
        http_err = urllib.error.HTTPError(
            url="https://oauth2.googleapis.com/token",
            code=400,
            msg="Bad Request",
            hdrs=MagicMock(),
            fp=io.BytesIO(b'{"error":"invalid_grant"}'),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            with pytest.raises(RuntimeError):
                mod.exchange_refresh_token("cid", "csec", "rtoken")

    def test_non_invalid_grant_400_raises_plain_runtime_error(self):
        """A 400 with a different error code must NOT raise InvalidRefreshTokenError."""
        http_err = urllib.error.HTTPError(
            url="https://oauth2.googleapis.com/token",
            code=400,
            msg="Bad Request",
            hdrs=MagicMock(),
            fp=io.BytesIO(b'{"error":"invalid_client"}'),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            with pytest.raises(RuntimeError) as exc_info:
                mod.exchange_refresh_token("cid", "csec", "rtoken")
        assert not isinstance(exc_info.value, mod.InvalidRefreshTokenError)


class TestSha256:
    def test_returns_hex_string(self):
        result = mod._sha256("hello")
        assert isinstance(result, str)
        assert len(result) == 64  # SHA-256 hex digest is always 64 chars

    def test_deterministic(self):
        assert mod._sha256("token") == mod._sha256("token")

    def test_different_inputs_differ(self):
        assert mod._sha256("token-a") != mod._sha256("token-b")

    def test_known_value(self):
        expected = hashlib.sha256(b"test").hexdigest()
        assert mod._sha256("test") == expected


class TestRefreshAndSave:
    def test_saves_token_and_hash_on_success(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        with patch.object(mod, "exchange_refresh_token", return_value="access-tok"):
            token = mod._refresh_and_save(str(p), SAMPLE_CREDS)
        assert token == "access-tok"
        cache = json.loads(p.read_text())
        assert cache["access_token"] == "access-tok"
        assert cache["refresh_token_sha256"] == mod._sha256(SAMPLE_CREDS["refresh_token"])
        assert "invalid_refresh_token" not in cache

    def test_caches_invalid_flag_on_invalid_grant(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        with patch.object(mod, "exchange_refresh_token",
                          side_effect=mod.InvalidRefreshTokenError("expired")):
            with pytest.raises(mod.InvalidRefreshTokenError):
                mod._refresh_and_save(str(p), SAMPLE_CREDS)
        cache = json.loads(p.read_text())
        assert cache.get("invalid_refresh_token") is True
        assert cache["refresh_token_sha256"] == mod._sha256(SAMPLE_CREDS["refresh_token"])
        assert "access_token" not in cache

    def test_invalid_flag_records_timestamp(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        before = time.time()
        with patch.object(mod, "exchange_refresh_token",
                          side_effect=mod.InvalidRefreshTokenError("expired")):
            with pytest.raises(mod.InvalidRefreshTokenError):
                mod._refresh_and_save(str(p), SAMPLE_CREDS)
        after = time.time()
        cache = json.loads(p.read_text())
        assert before <= cache["invalid_refresh_token_at"] <= after

    def test_env_token_bypasses_exchange(self, tmp_path, monkeypatch):
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "env-tok")
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        with patch.object(mod, "exchange_refresh_token") as mock_ex:
            token = mod._refresh_and_save(str(p), SAMPLE_CREDS)
        assert token == "env-tok"
        mock_ex.assert_not_called()
