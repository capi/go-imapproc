"""Shared fixtures and helpers for gws-import-to-gmail-direct tests."""

import importlib.util
import io
import subprocess
import sys
from pathlib import Path

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
# Shared constants
# ---------------------------------------------------------------------------

SAMPLE_EMAIL = b"From: sender@example.com\r\nTo: me@example.com\r\nSubject: Hi\r\n\r\nHello.\r\n"

SAMPLE_CREDS = {
    "client_id": "test-client-id",
    "client_secret": "test-client-secret",
    "refresh_token": "test-refresh-token",
}


# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------

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
