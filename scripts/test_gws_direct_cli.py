"""Tests for CLI argument parsing, dry-run, empty stdin, and main()."""

import subprocess
import sys
from unittest.mock import patch

import pytest

from conftest import SAMPLE_EMAIL, _FakeStdin, mod, run_dry


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


class TestArgumentParserCredentialsProvider:
    """Tests for the --credentials-provider argument."""

    def setup_method(self):
        self.parser = mod.build_argument_parser()

    def parse(self, *args):
        return self.parser.parse_args(list(args))

    def test_default_is_gws_auth_export(self):
        args = self.parse()
        assert args.credentials_provider == mod._DEFAULT_CREDENTIALS_PROVIDER

    def test_custom_provider_is_accepted(self):
        args = self.parse("--credentials-provider", "vault-tool get-oauth-creds")
        assert args.credentials_provider == "vault-tool get-oauth-creds"

    def test_credentials_provider_in_parser(self):
        """The option must be registered in the parser."""
        action_dests = [a.dest for a in self.parser._actions]
        assert "credentials_provider" in action_dests


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


class TestEmptyStdin:
    def test_empty_stdin_exits_nonzero(self):
        from conftest import SCRIPT
        result = subprocess.run(
            [sys.executable, str(SCRIPT)],
            input=b"",
            capture_output=True,
        )
        assert result.returncode != 0
        assert b"no email data received on stdin" in result.stderr


class TestMain:
    def _run_main(self, monkeypatch, extra_argv=None, env_token="test-token", tmp_path=None):
        monkeypatch.setattr("sys.stdin", _FakeStdin(SAMPLE_EMAIL))
        argv = ["gws-import-to-gmail-direct.py"]
        if tmp_path is not None:
            argv += ["--token-cache-file", str(tmp_path / "cache.json")]
        argv += extra_argv or []
        monkeypatch.setattr("sys.argv", argv)
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        if env_token:
            monkeypatch.setenv("GOOGLE_OAUTH_ACCESS_TOKEN", env_token)
        else:
            monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)
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

        def fake_import(cache_path, rotation, user, metadata, raw, query, **kwargs):
            captured["metadata"] = metadata
            captured["query"] = query
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(monkeypatch, extra_argv=["--add-label-id", "Label_X"], tmp_path=tmp_path)

        assert "Label_X" in captured["metadata"].get("labelIds", [])

    def test_never_mark_spam_in_query(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query, **kwargs):
            captured["query"] = query
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(monkeypatch, extra_argv=["--never-mark-spam"], tmp_path=tmp_path)

        assert captured["query"].get("neverMarkSpam") == "true"

    def test_raw_email_passed_to_import(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query, **kwargs):
            captured["raw"] = raw
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(monkeypatch, tmp_path=tmp_path)

        assert captured["raw"] == SAMPLE_EMAIL

    def test_token_rotation_interval_forwarded(self, monkeypatch, tmp_path):
        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query, **kwargs):
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

        def fake_import(cache_path, rotation, user, metadata, raw, query, **kwargs):
            captured["cache_path"] = cache_path
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            self._run_main(
                monkeypatch,
                extra_argv=["--token-cache-file", custom_cache],
                # Don't pass tmp_path so the argv doesn't add another --token-cache-file
            )

        assert captured["cache_path"] == custom_cache


class TestMainCredentialsProviderThreading:
    """main() must forward --credentials-provider to import_message_with_retry."""

    def test_main_passes_credentials_provider_to_import(self, monkeypatch, tmp_path):
        monkeypatch.setattr("sys.stdin", _FakeStdin(SAMPLE_EMAIL))
        custom_cache = str(tmp_path / "cache.json")
        monkeypatch.setattr("sys.argv", [
            "gws-import-to-gmail-direct.py",
            "--token-cache-file", custom_cache,
            "--credentials-provider", "vault-tool get-creds",
        ])
        monkeypatch.delenv("GOOGLE_WORKSPACE_CLI_TOKEN", raising=False)
        monkeypatch.delenv("GOOGLE_OAUTH_ACCESS_TOKEN", raising=False)

        captured = {}

        def fake_import(cache_path, rotation, user, metadata, raw, query,
                        credentials_provider=None):
            captured["credentials_provider"] = credentials_provider
            return {"id": "x"}

        with patch.object(mod, "import_message_with_retry", side_effect=fake_import):
            mod.main()

        assert captured["credentials_provider"] == "vault-tool get-creds"
