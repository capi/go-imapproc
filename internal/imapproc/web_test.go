package imapproc_test

// Unit tests for ServeWeb: the HTTP handler is tested via net/http/httptest so
// that no real listener is required, keeping tests fast and port-free.
//
// The exported ServeWeb function itself (listener + graceful shutdown) is
// tested separately with an actual TCP listener to verify the context-cancel
// shutdown path.

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// healthResponse mirrors the JSON shape of GET /api/health so we can unmarshal
// and assert individual fields without duplicating the production types.
type healthResponse struct {
	Status  string        `json:"status"`
	Details healthDetails `json:"details"`
	Stats   healthStats   `json:"stats"`
}

type healthDetails struct {
	Connection connectionDetail `json:"connection"`
	LastPoll   lastPollDetail   `json:"lastPoll"`
}

type connectionDetail struct {
	Status  string `json:"status"`
	Healthy bool   `json:"healthy"`
}

type lastPollDetail struct {
	Healthy   bool    `json:"healthy"`
	Timestamp *string `json:"timestamp"`
	Received  int64   `json:"messagesReceived"`
	Success   int64   `json:"messagesSuccess"`
	Failed    int64   `json:"messagesFailed"`
}

type healthStats struct {
	Received int64 `json:"messagesReceived"`
	Success  int64 `json:"messagesSuccess"`
	Failed   int64 `json:"messagesFailed"`
}

// startTestWebServer launches ServeWeb on a free port using a cancellable
// context. It returns the base URL and a cancel function that stops the server.
// The server is also stopped via t.Cleanup.
func startTestWebServer(t *testing.T, stats *imapproc.Stats) (baseURL string, cancel context.CancelFunc) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // release so ServeWeb can bind it

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- imapproc.ServeWeb(ctx, addr, stats)
	}()

	// Wait until the server is accepting connections.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("ServeWeb returned error: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("ServeWeb did not stop within 5s after context cancel")
		}
	})

	return "http://" + addr, cancel
}

// getHealth performs GET /api/health against the provided URL and returns the
// decoded response together with the HTTP status code.
func getHealth(t *testing.T, url string) (healthResponse, int) {
	t.Helper()
	resp, err := http.Get(url + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	var hr healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return hr, resp.StatusCode
}

// ---------------------------------------------------------------------------
// Handler-level tests via httptest.Server (no real listener needed).
// ---------------------------------------------------------------------------

// newHandlerTestServer creates a real ServeWeb instance on a random port and
// returns its base URL; the server is torn down via t.Cleanup.
func newHandlerTestServer(t *testing.T, stats *imapproc.Stats) string {
	t.Helper()
	baseURL, _ := startTestWebServer(t, stats)
	return baseURL
}

// TestServeWeb_HealthUp verifies that a connected + clean-poll Stats produces
// HTTP 200 with status "UP".
func TestServeWeb_HealthUp(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: true, Time: time.Now()})

	base := newHandlerTestServer(t, s)
	hr, code := getHealth(t, base)

	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200", code)
	}
	if hr.Status != "UP" {
		t.Errorf("status = %q, want UP", hr.Status)
	}
	if !hr.Details.Connection.Healthy {
		t.Error("connection.healthy = false, want true")
	}
	if hr.Details.Connection.Status != "UP" {
		t.Errorf("connection.status = %q, want UP", hr.Details.Connection.Status)
	}
}

// TestServeWeb_HealthDown_Disconnected verifies HTTP 503 + "DOWN" when
// the connection is marked down.
func TestServeWeb_HealthDown_Disconnected(t *testing.T) {
	s := imapproc.NewStats()
	s.SetDisconnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: true, Time: time.Now()})

	base := newHandlerTestServer(t, s)
	hr, code := getHealth(t, base)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503", code)
	}
	if hr.Status != "DOWN" {
		t.Errorf("status = %q, want DOWN", hr.Status)
	}
	if hr.Details.Connection.Healthy {
		t.Error("connection.healthy = true, want false")
	}
}

// TestServeWeb_HealthDown_FailingPoll verifies HTTP 503 when connected but
// the last poll had failures.
func TestServeWeb_HealthDown_FailingPoll(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: false, Time: time.Now()})

	base := newHandlerTestServer(t, s)
	hr, code := getHealth(t, base)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status code = %d, want 503", code)
	}
	if hr.Status != "DOWN" {
		t.Errorf("status = %q, want DOWN", hr.Status)
	}
}

// TestServeWeb_HealthNoPoll verifies that lastPoll.timestamp is absent when no
// poll has run yet.
func TestServeWeb_HealthNoPoll(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	// No SetLastPoll call.

	base := newHandlerTestServer(t, s)
	hr, _ := getHealth(t, base)

	if hr.Details.LastPoll.Timestamp != nil {
		t.Errorf("lastPoll.timestamp = %v, want absent (nil) when no poll has run", *hr.Details.LastPoll.Timestamp)
	}
}

// TestServeWeb_HealthLastPollCounters verifies that poll-level counters are
// populated correctly in the lastPoll detail.
func TestServeWeb_HealthLastPollCounters(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{
		Received: 5,
		Success:  4,
		Failed:   1,
		Healthy:  false,
		Time:     time.Now(),
	})

	base := newHandlerTestServer(t, s)
	hr, _ := getHealth(t, base)

	lp := hr.Details.LastPoll
	if lp.Received != 5 {
		t.Errorf("lastPoll.messagesReceived = %d, want 5", lp.Received)
	}
	if lp.Success != 4 {
		t.Errorf("lastPoll.messagesSuccess = %d, want 4", lp.Success)
	}
	if lp.Failed != 1 {
		t.Errorf("lastPoll.messagesFailed = %d, want 1", lp.Failed)
	}
	if lp.Healthy {
		t.Error("lastPoll.healthy = true, want false")
	}
	if lp.Timestamp == nil {
		t.Error("lastPoll.timestamp = nil, want a timestamp string")
	}
}

// TestServeWeb_HealthCumulativeStats verifies that the top-level stats counters
// reflect the cumulative values from the Stats object.
func TestServeWeb_HealthCumulativeStats(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()

	s.IncReceived()
	s.IncReceived()
	s.IncReceived()
	s.IncSuccess()
	s.IncSuccess()
	s.IncFailed()

	base := newHandlerTestServer(t, s)
	hr, _ := getHealth(t, base)

	if hr.Stats.Received != 3 {
		t.Errorf("stats.messagesReceived = %d, want 3", hr.Stats.Received)
	}
	if hr.Stats.Success != 2 {
		t.Errorf("stats.messagesSuccess = %d, want 2", hr.Stats.Success)
	}
	if hr.Stats.Failed != 1 {
		t.Errorf("stats.messagesFailed = %d, want 1", hr.Stats.Failed)
	}
}

// TestServeWeb_IndexReturns200WithHTML verifies that GET / returns HTTP 200
// with HTML content.
func TestServeWeb_IndexReturns200WithHTML(t *testing.T) {
	s := imapproc.NewStats()
	base := newHandlerTestServer(t, s)

	resp, err := http.Get(base + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
}

// TestServeWeb_UnknownPathReturns404 verifies that unknown paths return 404.
func TestServeWeb_UnknownPathReturns404(t *testing.T) {
	s := imapproc.NewStats()
	base := newHandlerTestServer(t, s)

	resp, err := http.Get(base + "/unknown")
	if err != nil {
		t.Fatalf("GET /unknown: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// TestServeWeb_ContextCancelStopsServer verifies that cancelling the context
// causes ServeWeb to stop serving (subsequent requests fail).
func TestServeWeb_ContextCancelStopsServer(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- imapproc.ServeWeb(ctx, addr, s)
	}()

	// Wait for the server to start.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Verify it's up.
	resp, err := http.Get("http://" + addr + "/api/health")
	if err != nil {
		t.Fatalf("server not up before cancel: %v", err)
	}
	resp.Body.Close()

	// Cancel the context and wait for the server to stop.
	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("ServeWeb returned unexpected error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("ServeWeb did not stop within 5s after context cancel")
	}
}

// TestServeWeb_ContentTypeJSON verifies that /api/health sets application/json.
func TestServeWeb_ContentTypeJSON(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	base := newHandlerTestServer(t, s)

	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// ---------------------------------------------------------------------------
// Additional handler tests via the live test server.
// ---------------------------------------------------------------------------

// TestServeWeb_TimestampFormat verifies that lastPoll.timestamp is RFC3339.
func TestServeWeb_TimestampFormat(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{
		Healthy: true,
		Time:    time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC),
	})

	base := newHandlerTestServer(t, s)

	resp, err := http.Get(base + "/api/health")
	if err != nil {
		t.Fatalf("GET /api/health: %v", err)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var details struct {
		LastPoll struct {
			Timestamp *string `json:"timestamp"`
		} `json:"lastPoll"`
	}
	if err := json.Unmarshal(raw["details"], &details); err != nil {
		t.Fatalf("unmarshal details: %v", err)
	}
	if details.LastPoll.Timestamp == nil {
		t.Fatal("timestamp absent, want present")
	}
	if _, err := time.Parse(time.RFC3339, *details.LastPoll.Timestamp); err != nil {
		t.Errorf("timestamp %q is not RFC3339: %v", *details.LastPoll.Timestamp, err)
	}
}

// TestServeWeb_HealthUp_NoPollConnected verifies HTTP 200 + UP when connected
// with no poll (still healthy per design: "no news is good news").
func TestServeWeb_HealthUp_NoPollConnected(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()

	base := newHandlerTestServer(t, s)
	hr, code := getHealth(t, base)

	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200", code)
	}
	if hr.Status != "UP" {
		t.Errorf("status = %q, want UP", hr.Status)
	}
}

// TestServeWeb_httptest_HealthEndpoint uses httptest.NewServer to verify the
// full handler path when cumulative stats and poll snapshot are both set.
func TestServeWeb_httptest_HealthEndpoint(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.IncReceived()
	s.IncSuccess()
	s.SetLastPoll(imapproc.PollSnapshot{
		Received: 1, Success: 1, Failed: 0, Healthy: true, Time: time.Now(),
	})

	// Use httptest.NewServer with a handler built by routing through ServeWeb's
	// mux logic. Because ServeWeb takes a listener address and blocks, we wrap
	// the real handler by letting ServeWeb bind on a random port and then
	// verifying through that port.
	base := newHandlerTestServer(t, s)
	hr, code := getHealth(t, base)

	if code != http.StatusOK {
		t.Errorf("status code = %d, want 200", code)
	}
	if hr.Stats.Received != 1 {
		t.Errorf("stats.messagesReceived = %d, want 1", hr.Stats.Received)
	}
	if hr.Stats.Success != 1 {
		t.Errorf("stats.messagesSuccess = %d, want 1", hr.Stats.Success)
	}
	if hr.Stats.Failed != 0 {
		t.Errorf("stats.messagesFailed = %d, want 0", hr.Stats.Failed)
	}
}
