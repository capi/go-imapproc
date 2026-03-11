package imapproc

import (
	"sync/atomic"
	"time"
)

// ConnectionStatus represents the current IMAP connection state.
type ConnectionStatus string

const (
	// StatusUnknown is the initial state before any connection attempt.
	StatusUnknown ConnectionStatus = "UNKNOWN"
	// StatusUp means the daemon is successfully connected to the IMAP server.
	StatusUp ConnectionStatus = "UP"
	// StatusDown means the daemon is disconnected (never connected, or lost the
	// connection).
	StatusDown ConnectionStatus = "DOWN"
)

// PollSnapshot holds the counters for a single processing pass.
type PollSnapshot struct {
	Received int64
	Success  int64
	Failed   int64
	Healthy  bool
	Time     time.Time
}

// Stats tracks runtime health and message processing counters for the daemon.
// All fields are safe for concurrent use.
type Stats struct {
	// connection state
	connStatus atomic.Value // stores ConnectionStatus

	// cumulative message counters (since process start)
	received atomic.Int64
	success  atomic.Int64
	failed   atomic.Int64

	// snapshot of the most recent poll; stored as *PollSnapshot via atomic pointer
	lastPoll atomic.Pointer[PollSnapshot]
}

// NewStats creates a Stats instance with initial state.
func NewStats() *Stats {
	s := &Stats{}
	s.connStatus.Store(StatusUnknown)
	return s
}

// SetConnected marks the IMAP connection as established.
func (s *Stats) SetConnected() {
	s.connStatus.Store(StatusUp)
}

// SetDisconnected marks the IMAP connection as lost.
func (s *Stats) SetDisconnected() {
	s.connStatus.Store(StatusDown)
}

// ConnStatus returns the current connection status.
func (s *Stats) ConnStatus() ConnectionStatus {
	return s.connStatus.Load().(ConnectionStatus)
}

// IncReceived increments the cumulative received counter.
func (s *Stats) IncReceived() {
	s.received.Add(1)
}

// IncSuccess increments the cumulative success counter.
func (s *Stats) IncSuccess() {
	s.success.Add(1)
}

// IncFailed increments the cumulative failed counter.
func (s *Stats) IncFailed() {
	s.failed.Add(1)
}

// Counters returns the cumulative (received, success, failed) counters.
func (s *Stats) Counters() (received, success, failed int64) {
	return s.received.Load(), s.success.Load(), s.failed.Load()
}

// SetLastPoll records the outcome of the most recent processing pass.
func (s *Stats) SetLastPoll(snap PollSnapshot) {
	s.lastPoll.Store(&snap)
}

// LastPoll returns the snapshot of the most recent poll, and a boolean
// indicating whether any poll has run yet.
func (s *Stats) LastPoll() (PollSnapshot, bool) {
	p := s.lastPoll.Load()
	if p == nil {
		return PollSnapshot{}, false
	}
	return *p, true
}

// Healthy returns true when all health criteria are met:
//   - connection is UP
//   - the last poll (if any) had no failures
func (s *Stats) Healthy() bool {
	if s.ConnStatus() != StatusUp {
		return false
	}
	snap, hasPoll := s.LastPoll()
	if !hasPoll {
		// No poll has run yet; connected but no data — treat as healthy.
		return true
	}
	return snap.Healthy
}
