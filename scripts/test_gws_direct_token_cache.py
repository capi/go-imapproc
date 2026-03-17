"""Tests for token cache helpers and get_access_token."""

import json
import time
from unittest.mock import call, patch

import pytest

from conftest import SAMPLE_CREDS, mod


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


class TestGetAccessToken:
    def _clear_env_tokens(self, monkeypatch):
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)

    def test_uses_env_var_when_set(self, monkeypatch, tmp_path):
        self._clear_env_tokens(monkeypatch)
        monkeypatch.setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "env-token")
        token = mod.get_access_token(str(tmp_path / "cache.json"), 50)
        assert token == "env-token"

    def test_uses_cached_token_when_fresh(self, monkeypatch, tmp_path):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        rt_hash = mod._sha256(SAMPLE_CREDS["refresh_token"])
        mod._save_cache(str(p), {"access_token": "cached-tok", "obtained_at": time.time() - 60,
                                  "refresh_token_sha256": rt_hash})
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            token = mod.get_access_token(str(p), 50)
        assert token == "cached-tok"

    def test_refreshes_when_cache_is_stale(self, monkeypatch, tmp_path):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old-tok", "obtained_at": time.time() - 9999})
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="new-tok"):
                token = mod.get_access_token(str(p), 50)
        assert token == "new-tok"

    def test_refreshes_when_no_cache_file(self, monkeypatch, tmp_path):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="brand-new"):
                token = mod.get_access_token(str(p), 50)
        assert token == "brand-new"

    def test_persists_new_token_to_cache(self, monkeypatch, tmp_path):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="saved-tok"):
                mod.get_access_token(str(p), 50)
        cache = json.loads(p.read_text())
        assert cache["access_token"] == "saved-tok"
        assert "obtained_at" in cache

    def test_raises_when_credentials_missing_fields(self, monkeypatch, tmp_path):
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        with patch.object(mod, "load_credentials", return_value={"client_id": "x"}):
            with pytest.raises(RuntimeError, match="missing required fields"):
                mod.get_access_token(str(p), 50)

    def test_fails_immediately_when_refresh_token_cached_as_invalid(self, monkeypatch, tmp_path):
        """If the cache records this refresh token as invalid, raise without hitting the endpoint."""
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        rt_hash = mod._sha256(SAMPLE_CREDS["refresh_token"])
        mod._save_cache(str(p), {
            "refresh_token_sha256": rt_hash,
            "invalid_refresh_token": True,
            "invalid_refresh_token_at": time.time() - 60,
        })
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token") as mock_exchange:
                with pytest.raises(mod.InvalidRefreshTokenError, match="expired or revoked"):
                    mod.get_access_token(str(p), 50)
        mock_exchange.assert_not_called()

    def test_clears_invalid_flag_when_refresh_token_rotates(self, monkeypatch, tmp_path):
        """A new refresh token (different hash) must clear the invalid flag and proceed."""
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        # Cache records the *old* refresh token as invalid.
        old_rt_hash = mod._sha256("old-refresh-token")
        mod._save_cache(str(p), {
            "refresh_token_sha256": old_rt_hash,
            "invalid_refresh_token": True,
            "invalid_refresh_token_at": time.time() - 60,
        })
        # Credentials now contain a *new* refresh token.
        new_creds = {**SAMPLE_CREDS, "refresh_token": "new-refresh-token"}
        with patch.object(mod, "load_credentials", return_value=new_creds):
            with patch.object(mod, "exchange_refresh_token", return_value="fresh-tok"):
                token = mod.get_access_token(str(p), 50)
        assert token == "fresh-tok"

    def test_invalid_flag_ignored_for_different_token_hash(self, monkeypatch, tmp_path):
        """The invalid flag is only honoured when the hash matches the current token."""
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {
            "refresh_token_sha256": mod._sha256("some-other-token"),
            "invalid_refresh_token": True,
            "invalid_refresh_token_at": time.time() - 60,
        })
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token", return_value="ok-tok"):
                token = mod.get_access_token(str(p), 50)
        assert token == "ok-tok"

    def test_fresh_cached_token_without_hash_is_reused(self, monkeypatch, tmp_path):
        """Old-format cache (no refresh_token_sha256) with a fresh token must be reused.

        This covers the upgrade path: an existing installation whose cache was
        written by a previous version of the script (before the hash field was
        introduced) must keep working without forcing an unnecessary token
        exchange.
        """
        self._clear_env_tokens(monkeypatch)
        p = tmp_path / "cache.json"
        # Deliberately omit refresh_token_sha256 — simulates a pre-upgrade cache.
        mod._save_cache(str(p), {"access_token": "legacy-tok", "obtained_at": time.time() - 60})
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with patch.object(mod, "exchange_refresh_token") as mock_exchange:
                token = mod.get_access_token(str(p), 50)
        assert token == "legacy-tok"
        mock_exchange.assert_not_called()


class TestCredentialsProviderThreadingTokenCache:
    """credentials_provider threading tests for get_access_token."""

    def test_get_access_token_accepts_credentials_provider(self, monkeypatch, tmp_path):
        """get_access_token must accept a credentials_provider parameter."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        rt_hash = mod._sha256(SAMPLE_CREDS["refresh_token"])
        mod._save_cache(str(p), {"access_token": "cached", "obtained_at": time.time() - 60,
                                  "refresh_token_sha256": rt_hash})
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS) as mock_lc:
            token = mod.get_access_token(str(p), 50, "custom-provider creds")
        assert token == "cached"
        # load_credentials must have been called with the provided command.
        mock_lc.assert_called_once_with("custom-provider creds")

    def test_get_access_token_uses_provider_when_refreshing(self, monkeypatch, tmp_path):
        """On stale token, get_access_token must call load_credentials with the given provider."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        mod._save_cache(str(p), {"access_token": "old", "obtained_at": time.time() - 9999})
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS) as mock_lc:
            with patch.object(mod, "exchange_refresh_token", return_value="refreshed"):
                token = mod.get_access_token(str(p), 50, "my-provider get-creds")
        assert token == "refreshed"
        mock_lc.assert_called_with("my-provider get-creds")
