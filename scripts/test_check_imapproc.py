"""Tests for check_imapproc.py — Nagios check for go-imapproc /api/health."""

import importlib.util
import io
import json
import socket
import subprocess
import sys
import threading
import urllib.error
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from unittest.mock import patch

import pytest

# ---------------------------------------------------------------------------
# Import the script as a module despite the hyphen in the filename.
# ---------------------------------------------------------------------------

SCRIPT = Path(__file__).parent / "check_imapproc.py"
spec = importlib.util.spec_from_file_location("check_imapproc", SCRIPT)
assert spec is not None and spec.loader is not None
mod = importlib.util.module_from_spec(spec)
spec.loader.exec_module(mod)  # type: ignore[union-attr]

# ---------------------------------------------------------------------------
# Shared fixture payloads
# ---------------------------------------------------------------------------

def _health(
    *,
    conn_status: str = "UP",
    poll_failed: int = 0,
    poll_received: int = 0,
    poll_success: int = 0,
    poll_ts: str | None = "2026-01-02T03:04:05Z",
    total_received: int = 0,
    total_success: int = 0,
    total_failed: int = 0,
) -> dict:
    """Build a health response dict matching go-imapproc's JSON schema."""
    last_poll: dict = {
        "healthy": poll_failed == 0,
        "messagesReceived": poll_received,
        "messagesSuccess": poll_success,
        "messagesFailed": poll_failed,
    }
    if poll_ts is not None:
        last_poll["timestamp"] = poll_ts
    return {
        "status": "UP" if conn_status == "UP" and poll_failed == 0 else "DOWN",
        "details": {
            "connection": {"status": conn_status, "healthy": conn_status == "UP"},
            "lastPoll": last_poll,
        },
        "stats": {
            "messagesReceived": total_received,
            "messagesSuccess": total_success,
            "messagesFailed": total_failed,
        },
    }


# ---------------------------------------------------------------------------
# Unit tests: evaluate_health
# ---------------------------------------------------------------------------

class TestEvaluateHealth:

    # --- OK ---

    def test_ok_when_connected_and_no_failures(self):
        state, msg = mod.evaluate_health(_health())
        assert state == mod.STATE_OK
        assert "OK" not in msg  # label is printed by main(); msg is body only
        assert "IMAP UP" in msg

    def test_ok_when_connected_no_poll_yet(self):
        state, msg = mod.evaluate_health(_health(poll_ts=None))
        assert state == mod.STATE_OK
        assert "no poll yet" in msg

    def test_ok_message_contains_timestamp(self):
        _state, msg = mod.evaluate_health(
            _health(poll_success=3, poll_received=3, poll_ts="2026-01-02T03:04:05Z")
        )
        assert "2026-01-02T03:04:05Z" in msg

    def test_ok_message_contains_success_count(self):
        _state, msg = mod.evaluate_health(
            _health(poll_success=5, poll_received=5)
        )
        assert "5" in msg

    # --- WARNING ---

    def test_warning_when_last_poll_has_failures(self):
        state, _msg = mod.evaluate_health(
            _health(conn_status="UP", poll_failed=2, poll_success=3, poll_received=5)
        )
        assert state == mod.STATE_WARNING

    def test_warning_message_reports_failed_count(self):
        _state, msg = mod.evaluate_health(
            _health(poll_failed=7, poll_success=1, poll_received=8)
        )
        assert "7" in msg
        assert "failed" in msg

    def test_warning_message_reports_success_count(self):
        _state, msg = mod.evaluate_health(
            _health(poll_failed=2, poll_success=5, poll_received=7)
        )
        assert "5" in msg

    def test_warning_message_includes_timestamp(self):
        _state, msg = mod.evaluate_health(
            _health(poll_failed=1, poll_ts="2026-03-01T00:00:00Z")
        )
        assert "2026-03-01T00:00:00Z" in msg

    def test_warning_message_omits_timestamp_when_absent(self):
        _state, msg = mod.evaluate_health(
            _health(poll_failed=1, poll_ts=None)
        )
        # Should not raise; timestamp section simply absent
        assert "failed" in msg

    # --- CRITICAL ---

    def test_critical_when_connection_down(self):
        state, _msg = mod.evaluate_health(_health(conn_status="DOWN"))
        assert state == mod.STATE_CRITICAL

    def test_critical_when_connection_unknown(self):
        state, _msg = mod.evaluate_health(_health(conn_status="UNKNOWN"))
        assert state == mod.STATE_CRITICAL

    def test_critical_message_reports_status(self):
        _state, msg = mod.evaluate_health(_health(conn_status="DOWN"))
        assert "DOWN" in msg

    def test_critical_takes_priority_over_poll_failures(self):
        """Even with poll failures, DOWN connection must be CRITICAL, not WARNING."""
        state, _msg = mod.evaluate_health(
            _health(conn_status="DOWN", poll_failed=3)
        )
        assert state == mod.STATE_CRITICAL

    # --- UNKNOWN ---

    def test_unknown_on_missing_details_key(self):
        state, msg = mod.evaluate_health({"status": "UP"})
        assert state == mod.STATE_UNKNOWN
        assert "structure" in msg.lower() or "unexpected" in msg.lower()

    def test_unknown_on_missing_connection_key(self):
        state, _msg = mod.evaluate_health({"details": {}, "stats": {}})
        assert state == mod.STATE_UNKNOWN

    def test_unknown_on_none_details(self):
        state, _msg = mod.evaluate_health({"details": None, "stats": {}})
        assert state == mod.STATE_UNKNOWN

    # --- Performance data ---

    def test_perfdata_present_on_ok(self):
        _state, msg = mod.evaluate_health(_health())
        assert "|" in msg
        assert "poll_failed=0" in msg

    def test_perfdata_present_on_warning(self):
        _state, msg = mod.evaluate_health(_health(poll_failed=2))
        assert "poll_failed=2" in msg

    def test_perfdata_present_on_critical(self):
        _state, msg = mod.evaluate_health(_health(conn_status="DOWN"))
        assert "|" in msg

    def test_perfdata_contains_totals(self):
        _state, msg = mod.evaluate_health(
            _health(total_received=100, total_success=98, total_failed=2)
        )
        assert "total_received=100" in msg
        assert "total_success=98" in msg
        assert "total_failed=2" in msg


# ---------------------------------------------------------------------------
# Unit tests: _perfdata
# ---------------------------------------------------------------------------

class TestPerfdata:
    def test_all_fields_present(self):
        result = mod._perfdata(10, 8, 2, 100, 90, 10)
        for key in (
            "poll_received=10",
            "poll_success=8",
            "poll_failed=2",
            "total_received=100",
            "total_success=90",
            "total_failed=10",
        ):
            assert key in result


# ---------------------------------------------------------------------------
# Unit tests: argument parser
# ---------------------------------------------------------------------------

class TestArgumentParser:
    def setup_method(self):
        self.parser = mod.build_argument_parser()

    def parse(self, *args):
        return self.parser.parse_args(list(args))

    def test_url_required(self):
        with pytest.raises(SystemExit):
            self.parse()

    def test_url_stored(self):
        args = self.parse("--url", "http://localhost:8080/api/health")
        assert args.url == "http://localhost:8080/api/health"

    def test_timeout_default(self):
        args = self.parse("--url", "http://x/api/health")
        assert args.timeout == mod._DEFAULT_TIMEOUT

    def test_timeout_custom(self):
        args = self.parse("--url", "http://x/api/health", "--timeout", "5")
        assert args.timeout == 5.0

    def test_timeout_must_be_numeric(self):
        with pytest.raises(SystemExit):
            self.parse("--url", "http://x/api/health", "--timeout", "fast")


# ---------------------------------------------------------------------------
# Unit tests: fetch_health (mocked urllib)
# ---------------------------------------------------------------------------

class TestFetchHealth:
    def _make_response(self, body: dict, status: int = 200):
        data = json.dumps(body).encode()
        resp = type("FakeResp", (), {
            "read": lambda self: data,
            "status": status,
            "__enter__": lambda self: self,
            "__exit__": lambda self, *a: False,
        })()
        return resp

    def test_returns_status_and_parsed_json(self):
        payload = _health()
        resp = self._make_response(payload, 200)
        with patch("urllib.request.urlopen", return_value=resp):
            code, data = mod.fetch_health("http://x/api/health", 10)
        assert code == 200
        assert data == payload

    def test_returns_503_and_body_on_http_error_with_json(self):
        payload = _health(conn_status="DOWN")
        http_err = urllib.error.HTTPError(
            url="http://x/api/health",
            code=503,
            msg="Service Unavailable",
            hdrs=None,
            fp=io.BytesIO(json.dumps(payload).encode()),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            code, data = mod.fetch_health("http://x/api/health", 10)
        assert code == 503
        assert data["status"] == "DOWN"

    def test_raises_value_error_on_http_error_with_non_json_body(self):
        http_err = urllib.error.HTTPError(
            url="http://x/api/health",
            code=500,
            msg="Internal Server Error",
            hdrs=None,
            fp=io.BytesIO(b"<html>oops</html>"),
        )
        with patch("urllib.request.urlopen", side_effect=http_err):
            with pytest.raises(ValueError, match="not valid JSON"):
                mod.fetch_health("http://x/api/health", 10)

    def test_propagates_url_error(self):
        err = urllib.error.URLError("connection refused")
        with patch("urllib.request.urlopen", side_effect=err):
            with pytest.raises(urllib.error.URLError):
                mod.fetch_health("http://x/api/health", 10)


# ---------------------------------------------------------------------------
# Integration tests: real HTTP server
# ---------------------------------------------------------------------------

class _HealthServer:
    """Minimal HTTP server that serves a fixed health response for testing."""

    def __init__(self, payload: dict, status: int = 200) -> None:
        self.payload = payload
        self.status = status
        self._requests: list[str] = []
        self._server: HTTPServer | None = None
        self._thread: threading.Thread | None = None

    @property
    def url(self) -> str:
        assert self._server is not None
        host, port = self._server.server_address
        return f"http://{host}:{port}/api/health"

    def start(self) -> None:
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):  # noqa: N802
                outer._requests.append(self.path)
                body = json.dumps(outer.payload).encode()
                self.send_response(outer.status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):  # silence request logs in test output
                pass

        self._server = HTTPServer(("127.0.0.1", 0), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        if self._server:
            self._server.shutdown()

    def requests_made(self) -> list[str]:
        return list(self._requests)


@pytest.fixture()
def health_server():
    """Factory fixture: call with (payload, status=200) to get a running server."""
    servers = []

    def factory(payload: dict, status: int = 200) -> _HealthServer:
        srv = _HealthServer(payload, status)
        srv.start()
        servers.append(srv)
        return srv

    yield factory

    for srv in servers:
        srv.stop()


class TestIntegrationOK:
    def test_exit_code_0_on_healthy(self, health_server):
        srv = health_server(_health(poll_success=3, poll_received=3))
        _code, data = mod.fetch_health(srv.url, 5)
        state, _msg = mod.evaluate_health(data)
        assert state == mod.STATE_OK

    def test_ok_message_via_main(self, health_server, monkeypatch, capsys):
        srv = health_server(_health(poll_success=2, poll_received=2))
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        rc = mod.main()
        out = capsys.readouterr().out
        assert rc == mod.STATE_OK
        assert "OK:" in out

    def test_no_poll_yet_ok(self, health_server, monkeypatch, capsys):
        srv = health_server(_health(poll_ts=None))
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        rc = mod.main()
        out = capsys.readouterr().out
        assert rc == mod.STATE_OK
        assert "no poll yet" in out

    def test_ok_perfdata_all_zeros(self, health_server, monkeypatch, capsys):
        srv = health_server(_health())
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        out = capsys.readouterr().out
        assert "poll_failed=0" in out
        assert "total_failed=0" in out


class TestIntegrationWarning:
    def test_exit_code_1_on_failed_mails(self, health_server, monkeypatch, capsys):
        srv = health_server(
            _health(poll_failed=3, poll_success=7, poll_received=10),
            status=503,
        )
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        rc = mod.main()
        assert rc == mod.STATE_WARNING

    def test_warning_label_in_output(self, health_server, monkeypatch, capsys):
        srv = health_server(
            _health(poll_failed=1, poll_success=4, poll_received=5),
            status=503,
        )
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        out = capsys.readouterr().out
        assert "WARNING:" in out

    def test_warning_reports_failure_count(self, health_server, monkeypatch, capsys):
        srv = health_server(
            _health(poll_failed=5, poll_success=2, poll_received=7),
            status=503,
        )
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        out = capsys.readouterr().out
        assert "5" in out
        assert "failed" in out

    def test_warning_perfdata_poll_failed(self, health_server, monkeypatch, capsys):
        srv = health_server(
            _health(poll_failed=4, poll_success=6, poll_received=10),
            status=503,
        )
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        out = capsys.readouterr().out
        assert "poll_failed=4" in out


class TestIntegrationCritical:
    def test_exit_code_2_on_imap_down(self, health_server, monkeypatch, capsys):
        srv = health_server(_health(conn_status="DOWN"), status=503)
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        rc = mod.main()
        assert rc == mod.STATE_CRITICAL

    def test_exit_code_2_on_imap_unknown(self, health_server, monkeypatch, capsys):
        srv = health_server(_health(conn_status="UNKNOWN"), status=503)
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        rc = mod.main()
        assert rc == mod.STATE_CRITICAL

    def test_critical_label_in_output(self, health_server, monkeypatch, capsys):
        srv = health_server(_health(conn_status="DOWN"), status=503)
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        out = capsys.readouterr().out
        assert "CRITICAL:" in out

    def test_critical_reports_connection_status(self, health_server, monkeypatch, capsys):
        srv = health_server(_health(conn_status="DOWN"), status=503)
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        out = capsys.readouterr().out
        assert "DOWN" in out

    def test_critical_with_poll_failures_still_critical(self, health_server, monkeypatch, capsys):
        """Connection DOWN takes priority over poll failures."""
        srv = health_server(
            _health(conn_status="DOWN", poll_failed=2),
            status=503,
        )
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        rc = mod.main()
        assert rc == mod.STATE_CRITICAL


class TestIntegrationUnknown:
    def test_exit_code_3_on_connection_refused(self, monkeypatch, capsys):
        # Pick a port that is not listening.
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        # Socket is now closed; port should be free (not listening).
        url = f"http://127.0.0.1:{port}/api/health"
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", url])
        rc = mod.main()
        assert rc == mod.STATE_UNKNOWN

    def test_unknown_label_on_unreachable(self, monkeypatch, capsys):
        with socket.socket() as s:
            s.bind(("127.0.0.1", 0))
            port = s.getsockname()[1]
        url = f"http://127.0.0.1:{port}/api/health"
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", url])
        mod.main()
        out = capsys.readouterr().out
        assert "UNKNOWN:" in out

    def test_unknown_on_non_json_response(self, monkeypatch, capsys):
        """Server returns 200 but with HTML body."""
        class _HtmlHandler(BaseHTTPRequestHandler):
            def do_GET(self):  # noqa: N802
                body = b"<html><body>Not JSON</body></html>"
                self.send_response(200)
                self.send_header("Content-Type", "text/html")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = HTTPServer(("127.0.0.1", 0), _HtmlHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        host, port = server.server_address
        url = f"http://{host}:{port}/api/health"
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", url])
        try:
            rc = mod.main()
        finally:
            server.shutdown()
        assert rc == mod.STATE_UNKNOWN

    def test_unknown_on_truncated_json(self, monkeypatch, capsys):
        """Server returns a truncated/invalid JSON body with a 200 status."""
        class _BadJsonHandler(BaseHTTPRequestHandler):
            def do_GET(self):  # noqa: N802
                body = b'{"status": "UP", "details":'  # truncated
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        server = HTTPServer(("127.0.0.1", 0), _BadJsonHandler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        host, port = server.server_address
        url = f"http://{host}:{port}/api/health"
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", url])
        try:
            rc = mod.main()
        finally:
            server.shutdown()
        assert rc == mod.STATE_UNKNOWN

    def test_server_requests_reach_correct_path(self, health_server, monkeypatch):
        srv = health_server(_health())
        monkeypatch.setattr("sys.argv", ["check_imapproc.py", "--url", srv.url])
        mod.main()
        assert "/api/health" in srv.requests_made()


# ---------------------------------------------------------------------------
# Integration tests: subprocess (full end-to-end)
# ---------------------------------------------------------------------------

class TestSubprocess:
    def _run(self, *args: str, input_: bytes | None = None) -> subprocess.CompletedProcess:
        return subprocess.run(
            [sys.executable, str(SCRIPT), *args],
            capture_output=True,
            input=input_,
        )

    def test_exits_nonzero_without_args(self):
        result = self._run()
        assert result.returncode != 0

    def test_exits_2_on_imap_down_via_subprocess(self, health_server):
        srv = health_server(_health(conn_status="DOWN"), status=503)
        result = self._run("--url", srv.url)
        assert result.returncode == mod.STATE_CRITICAL
        assert b"CRITICAL" in result.stdout

    def test_exits_1_on_poll_failures_via_subprocess(self, health_server):
        srv = health_server(
            _health(poll_failed=2, poll_success=5, poll_received=7),
            status=503,
        )
        result = self._run("--url", srv.url)
        assert result.returncode == mod.STATE_WARNING
        assert b"WARNING" in result.stdout

    def test_exits_0_on_healthy_via_subprocess(self, health_server):
        srv = health_server(_health(poll_success=4, poll_received=4))
        result = self._run("--url", srv.url)
        assert result.returncode == mod.STATE_OK
        assert b"OK:" in result.stdout

    def test_exits_3_on_unreachable_via_subprocess(self):
        result = self._run("--url", "http://127.0.0.1:1/api/health", "--timeout", "1")
        assert result.returncode == mod.STATE_UNKNOWN
        assert b"UNKNOWN" in result.stdout
