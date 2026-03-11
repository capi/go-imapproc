package imapproc

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// healthResponse mirrors the Spring Actuator /actuator/health envelope.
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

// lastPollDetail describes the most recent processing pass.
type lastPollDetail struct {
	Healthy   bool    `json:"healthy"`
	Timestamp *string `json:"timestamp,omitempty"` // RFC3339; absent if no poll yet
	Received  int64   `json:"messagesReceived"`
	Success   int64   `json:"messagesSuccess"`
	Failed    int64   `json:"messagesFailed"`
}

type healthStats struct {
	Received int64 `json:"messagesReceived"`
	Success  int64 `json:"messagesSuccess"`
	Failed   int64 `json:"messagesFailed"`
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>go-imapproc</title>
  <style>
    body { font-family: monospace; max-width: 800px; margin: 40px auto; padding: 0 20px; color: #333; }
    h1 { font-size: 1.4em; }
    a { color: #0066cc; }
    #health { margin-top: 1.5em; }
    #updated { font-size: 0.85em; color: #888; margin-top: 1em; }
    #reach-banner { display: none; padding: 6px 12px; margin-bottom: 1em;
                    background: #fdd; border: 1px solid #c33; color: #c33; }
    .status-UP    { color: #2a7; font-weight: bold; }
    .status-DOWN  { color: #c33; font-weight: bold; }
    .status-UNKNOWN { color: #888; font-weight: bold; }
    table { border-collapse: collapse; margin-top: 0.5em; }
    td, th { padding: 4px 12px; border: 1px solid #ccc; text-align: left; }
    th { background: #f5f5f5; }
  </style>
</head>
<body>
  <h1>go-imapproc</h1>
  <p>IMAP daemon that processes unread emails via an external program.</p>
  <p>Source: <a href="https://github.com/capi/go-imapproc">https://github.com/capi/go-imapproc</a></p>
  <div id="reach-banner">UNREACHABLE — last successful data shown below</div>
  <h2 id="health-status">Health: <span class="status-UNKNOWN">UNKNOWN</span></h2>
  <div id="health"></div>
  <div id="updated"></div>
  <script>
    function render(h) {
      var cls = 'status-' + h.status;
      var conn = h.details.connection;
      var poll = h.details.lastPoll;
      var pollTs = poll.timestamp ? poll.timestamp : '—';
      var pollStatus = poll.timestamp
        ? (poll.healthy ? 'OK' : 'FAILED')
        : 'no poll yet';
      var pollStatusCls = poll.timestamp
        ? (poll.healthy ? 'status-UP' : 'status-DOWN')
        : '';

      var html = '<h3>Status</h3>';
      html += '<table><tr><th>Component</th><th>Status</th><th>Details</th></tr>';
      html += '<tr><td>IMAP connection</td>'
            + '<td class="' + (conn.healthy ? 'status-UP' : 'status-DOWN') + '">' + conn.status + '</td>'
            + '<td></td></tr>';
      html += '<tr><td>Last poll</td>'
            + '<td class="' + pollStatusCls + '">' + pollStatus + '</td>'
            + '<td>' + pollTs + '</td></tr>';
      html += '</table>';

      var lp = poll.timestamp ? poll.messagesReceived : '—';
      var ls = poll.timestamp ? poll.messagesSuccess  : '—';
      var lf = poll.timestamp ? poll.messagesFailed   : '—';
      html += '<h3>Statistics</h3>';
      html += '<table><tr><th>Counter</th><th>Last Poll</th><th>Total</th></tr>';
      html += '<tr><td>Messages received</td><td>' + lp + '</td><td>' + h.stats.messagesReceived + '</td></tr>';
      html += '<tr><td>Messages succeeded</td><td>' + ls + '</td><td>' + h.stats.messagesSuccess  + '</td></tr>';
      html += '<tr><td>Messages failed</td><td>'    + lf + '</td><td>' + h.stats.messagesFailed   + '</td></tr>';
      html += '</table>';

      document.getElementById('health-status').innerHTML = 'Health: <span class="' + cls + '">' + h.status + '</span>';
      document.getElementById('health').innerHTML = html;
      document.getElementById('reach-banner').style.display = 'none';
      document.getElementById('updated').textContent = 'Data last updated: ' + new Date().toLocaleTimeString();
    }
    function load() {
      var ctrl = new AbortController();
      var timer = setTimeout(function() { ctrl.abort(); }, 5000);
      fetch('/api/health', { signal: ctrl.signal })
        .then(function(r) { clearTimeout(timer); return r.json(); })
        .then(render)
        .catch(function() {
          clearTimeout(timer);
          document.getElementById('reach-banner').style.display = 'block';
          document.getElementById('health-status').innerHTML = 'Health: <span class="status-DOWN">UNREACHABLE</span>';
          document.getElementById('updated').textContent = 'Last update attempt: ' + new Date().toLocaleTimeString();
        });
    }
    load();
    setInterval(load, 5000);
  </script>
</body>
</html>
`

// ServeWeb starts an HTTP server on addr that exposes:
//
//	GET /         — HTML dashboard (auto-refreshes every 5 s via JS)
//	GET /api/health — JSON health endpoint
//
// The server shuts down gracefully when ctx is cancelled. It returns an error
// only if the listener cannot be bound; context cancellation is not an error.
func ServeWeb(ctx context.Context, addr string, stats *Stats) error {
	mux := http.NewServeMux()

	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		connStatus := stats.ConnStatus()
		connHealthy := connStatus == StatusUp

		snap, hasPoll := stats.LastPoll()
		received, success, failed := stats.Counters()

		overall := "UP"
		if !stats.Healthy() {
			overall = "DOWN"
		}

		var pollDetail lastPollDetail
		if hasPoll {
			ts := snap.Time.UTC().Format(time.RFC3339)
			pollDetail = lastPollDetail{
				Healthy:   snap.Healthy,
				Timestamp: &ts,
				Received:  snap.Received,
				Success:   snap.Success,
				Failed:    snap.Failed,
			}
		}

		resp := healthResponse{
			Status: overall,
			Details: healthDetails{
				Connection: connectionDetail{
					Status:  string(connStatus),
					Healthy: connHealthy,
				},
				LastPoll: pollDetail,
			},
			Stats: healthStats{
				Received: received,
				Success:  success,
				Failed:   failed,
			},
		}

		statusCode := http.StatusOK
		if overall == "DOWN" {
			statusCode = http.StatusServiceUnavailable
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Printf("web: encode health response: %v", err)
		}
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, indexHTML)
	})

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// Shut down when context is cancelled.
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("web: shutdown: %v", err)
		}
	}()

	log.Printf("web: listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("web: %w", err)
	}
	return nil
}
