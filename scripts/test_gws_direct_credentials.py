"""Tests for credential loading functions and related constants."""

import json
import subprocess
import time
from unittest.mock import patch

import pytest

from conftest import SAMPLE_CREDS, mod


class TestLoadGwsCredentials:
    """Tests for the backwards-compatible load_gws_credentials() wrapper."""

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
            with pytest.raises(RuntimeError, match="not found"):
                mod.load_gws_credentials()

    def test_raises_on_nonzero_exit(self):
        exc = subprocess.CalledProcessError(1, "gws", stderr=b"no credentials")
        with patch("subprocess.run", side_effect=exc):
            with pytest.raises(RuntimeError, match="failed"):
                mod.load_gws_credentials()

    def test_raises_on_invalid_json(self):
        result = subprocess.CompletedProcess(
            args=[], returncode=0, stdout=b"not-json", stderr=b"",
        )
        with patch("subprocess.run", return_value=result):
            with pytest.raises(RuntimeError, match="invalid JSON"):
                mod.load_gws_credentials()


class TestLoadCredentials:
    """Tests for the new load_credentials(provider_cmd) function."""

    def test_returns_parsed_json_from_custom_provider(self):
        """load_credentials should call the given command and parse its JSON output."""
        result = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout=json.dumps(SAMPLE_CREDS).encode(),
            stderr=b"",
        )
        with patch("subprocess.run", return_value=result) as mock_run:
            creds = mod.load_credentials("my-tool auth export")
        assert creds == SAMPLE_CREDS
        # The command must have been split correctly.
        mock_run.assert_called_once()
        called_cmd = mock_run.call_args[0][0]
        assert called_cmd == ["my-tool", "auth", "export"]

    def test_default_provider_calls_gws_auth_export(self):
        """When called with the default provider string the argv should match the old behaviour."""
        result = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout=json.dumps(SAMPLE_CREDS).encode(),
            stderr=b"",
        )
        with patch("subprocess.run", return_value=result) as mock_run:
            mod.load_credentials(mod._DEFAULT_CREDENTIALS_PROVIDER)
        called_cmd = mock_run.call_args[0][0]
        assert called_cmd == ["gws", "auth", "export", "--unmasked"]

    def test_raises_when_provider_not_found(self):
        """FileNotFoundError from subprocess becomes a RuntimeError."""
        with patch("subprocess.run", side_effect=FileNotFoundError):
            with pytest.raises(RuntimeError):
                mod.load_credentials("nonexistent-tool auth")

    def test_raises_on_nonzero_exit(self):
        exc = subprocess.CalledProcessError(1, "my-tool", stderr=b"no credentials")
        with patch("subprocess.run", side_effect=exc):
            with pytest.raises(RuntimeError):
                mod.load_credentials("my-tool auth export")

    def test_raises_on_invalid_json(self):
        result = subprocess.CompletedProcess(
            args=[], returncode=0, stdout=b"not-json", stderr=b"",
        )
        with patch("subprocess.run", return_value=result):
            with pytest.raises(RuntimeError, match="invalid JSON"):
                mod.load_credentials("my-tool auth export")

    def test_error_message_contains_command(self):
        """The RuntimeError should mention the actual command used."""
        exc = subprocess.CalledProcessError(1, "custom-creds", stderr=b"oops")
        with patch("subprocess.run", side_effect=exc):
            with pytest.raises(RuntimeError, match="custom-creds"):
                mod.load_credentials("custom-creds export")

    def test_provider_with_multi_word_args(self):
        """shlex.split should handle quoted arguments correctly."""
        result = subprocess.CompletedProcess(
            args=[], returncode=0,
            stdout=json.dumps(SAMPLE_CREDS).encode(),
            stderr=b"",
        )
        with patch("subprocess.run", return_value=result) as mock_run:
            mod.load_credentials("my-tool --config '/path with spaces/cfg.json' export")
        called_cmd = mock_run.call_args[0][0]
        assert called_cmd == ["my-tool", "--config", "/path with spaces/cfg.json", "export"]


class TestDefaultCredentialsProviderConstant:
    def test_default_credentials_provider_constant_exists(self):
        assert hasattr(mod, "_DEFAULT_CREDENTIALS_PROVIDER")

    def test_default_credentials_provider_is_gws(self):
        assert "gws" in mod._DEFAULT_CREDENTIALS_PROVIDER
        assert "auth" in mod._DEFAULT_CREDENTIALS_PROVIDER
        assert "export" in mod._DEFAULT_CREDENTIALS_PROVIDER
        assert "--unmasked" in mod._DEFAULT_CREDENTIALS_PROVIDER


class TestEnvVarRename:
    """GOOGLE_OAUTH_ACCESS_TOKEN replaces GOOGLE_WORKSPACE_CLI_TOKEN."""

    def test_new_env_var_used_in_get_access_token(self, monkeypatch, tmp_path):
        """GOOGLE_OAUTH_ACCESS_TOKEN must bypass cache and return the token."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "new-env-token")
        token = mod.get_access_token(str(tmp_path / "cache.json"), 50,
                                     mod._DEFAULT_CREDENTIALS_PROVIDER)
        assert token == "new-env-token"

    def test_old_env_var_still_works_with_deprecation_warning(self, monkeypatch, tmp_path, capsys):
        """GOOGLE_WORKSPACE_CLI_TOKEN must still work (backwards compat) but emit a warning."""
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", "old-env-token")
        token = mod.get_access_token(str(tmp_path / "cache.json"), 50,
                                     mod._DEFAULT_CREDENTIALS_PROVIDER)
        assert token == "old-env-token"
        # Deprecation warning must appear on stderr.
        stderr = capsys.readouterr().err
        assert "deprecated" in stderr.lower() or "GOOGLE_OAUTH_ACCESS_TOKEN" in stderr

    def test_new_env_var_takes_precedence_over_old(self, monkeypatch, tmp_path):
        """When both vars are set the new one wins."""
        monkeypatch.setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "new-wins")
        monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", "old-loses")
        token = mod.get_access_token(str(tmp_path / "cache.json"), 50,
                                     mod._DEFAULT_CREDENTIALS_PROVIDER)
        assert token == "new-wins"

    def test_new_env_var_used_in_refresh_and_save(self, monkeypatch, tmp_path):
        """_refresh_and_save must honour GOOGLE_OAUTH_ACCESS_TOKEN."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.setenv("GOOGLE_OAUTH_ACCESS_TOKEN", "env-tok-new")
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        with patch.object(mod, "exchange_refresh_token") as mock_ex:
            token = mod._refresh_and_save(str(p), SAMPLE_CREDS)
        assert token == "env-tok-new"
        mock_ex.assert_not_called()

    def test_old_env_var_still_works_in_refresh_and_save(self, monkeypatch, tmp_path):
        """_refresh_and_save must still honour GOOGLE_WORKSPACE_CLI_TOKEN for compatibility."""
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        monkeypatch.setenv("GOOGLE_WORKSPACE_CLI_TOKEN", "compat-tok")
        p = tmp_path / "cache.json"
        mod._ensure_cache_file(str(p))
        with patch.object(mod, "exchange_refresh_token") as mock_ex:
            token = mod._refresh_and_save(str(p), SAMPLE_CREDS)
        assert token == "compat-tok"
        mock_ex.assert_not_called()


class TestGenericErrorMessages:
    """Error messages related to credential re-auth must be generic."""

    def test_invalid_refresh_token_error_message_is_generic(self, monkeypatch, tmp_path):
        """The InvalidRefreshTokenError from the cache check must not mention gws specifically."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        rt_hash = mod._sha256(SAMPLE_CREDS["refresh_token"])
        mod._save_cache(str(p), {
            "refresh_token_sha256": rt_hash,
            "invalid_refresh_token": True,
            "invalid_refresh_token_at": time.time() - 60,
        })
        with patch.object(mod, "load_credentials", return_value=SAMPLE_CREDS):
            with pytest.raises(mod.InvalidRefreshTokenError) as exc_info:
                mod.get_access_token(str(p), 50, mod._DEFAULT_CREDENTIALS_PROVIDER)
        # Must NOT contain gws-specific instructions.
        msg = str(exc_info.value)
        assert "gws auth logout" not in msg
        assert "gws auth login" not in msg

    def test_missing_fields_error_message_does_not_hardcode_gws(self, monkeypatch, tmp_path):
        """The 'missing required fields' error must not mention gws specifically."""
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
        p = tmp_path / "cache.json"
        incomplete_creds = {"client_id": "x"}  # missing client_secret and refresh_token
        with patch.object(mod, "load_credentials", return_value=incomplete_creds):
            with pytest.raises(RuntimeError) as exc_info:
                mod.get_access_token(str(p), 50, "my-provider get-creds")
        msg = str(exc_info.value)
        assert "gws auth login" not in msg
