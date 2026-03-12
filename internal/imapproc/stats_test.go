package imapproc_test

// Unit tests for Stats: connection status transitions, counter increments,
// poll snapshot recording, and the Healthy() derived predicate.

import (
	"testing"
	"time"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// TestNewStats_InitialState verifies that a freshly created Stats has
// StatusUnknown, zero counters, and no poll recorded yet.
func TestNewStats_InitialState(t *testing.T) {
	s := imapproc.NewStats()

	if got := s.ConnStatus(); got != imapproc.StatusUnknown {
		t.Errorf("initial ConnStatus = %q, want %q", got, imapproc.StatusUnknown)
	}
	r, ok, f := s.Counters()
	if r != 0 || ok != 0 || f != 0 {
		t.Errorf("initial counters = (%d, %d, %d), want (0, 0, 0)", r, ok, f)
	}
	if _, hasPoll := s.LastPoll(); hasPoll {
		t.Error("expected no poll recorded on fresh Stats, but LastPoll reported one")
	}
}

// TestStats_ConnectionStatusTransitions exercises SetConnected and
// SetDisconnected and checks ConnStatus reflects each change.
func TestStats_ConnectionStatusTransitions(t *testing.T) {
	s := imapproc.NewStats()

	s.SetConnected()
	if got := s.ConnStatus(); got != imapproc.StatusUp {
		t.Errorf("after SetConnected: ConnStatus = %q, want %q", got, imapproc.StatusUp)
	}

	s.SetDisconnected()
	if got := s.ConnStatus(); got != imapproc.StatusDown {
		t.Errorf("after SetDisconnected: ConnStatus = %q, want %q", got, imapproc.StatusDown)
	}
}

// TestStats_Counters verifies that IncReceived, IncSuccess, and IncFailed each
// advance their respective counter independently.
func TestStats_Counters(t *testing.T) {
	s := imapproc.NewStats()

	s.IncReceived()
	s.IncReceived()
	s.IncSuccess()
	s.IncFailed()

	r, ok, f := s.Counters()
	if r != 2 {
		t.Errorf("received = %d, want 2", r)
	}
	if ok != 1 {
		t.Errorf("success = %d, want 1", ok)
	}
	if f != 1 {
		t.Errorf("failed = %d, want 1", f)
	}
}

// TestStats_LastPoll verifies that SetLastPoll stores the snapshot and
// LastPoll returns it accurately.
func TestStats_LastPoll(t *testing.T) {
	s := imapproc.NewStats()

	before := time.Now().Truncate(time.Second)
	snap := imapproc.PollSnapshot{
		Received: 3,
		Success:  2,
		Failed:   1,
		Healthy:  false,
		Time:     time.Now(),
	}
	s.SetLastPoll(snap)

	got, hasPoll := s.LastPoll()
	if !hasPoll {
		t.Fatal("expected LastPoll to report a poll was recorded")
	}
	if got.Received != 3 {
		t.Errorf("Received = %d, want 3", got.Received)
	}
	if got.Success != 2 {
		t.Errorf("Success = %d, want 2", got.Success)
	}
	if got.Failed != 1 {
		t.Errorf("Failed = %d, want 1", got.Failed)
	}
	if got.Healthy {
		t.Error("Healthy = true, want false")
	}
	if got.Time.Before(before) {
		t.Errorf("poll timestamp %v is before test start %v", got.Time, before)
	}
}

// TestStats_LastPoll_OverwritesPrevious confirms that a second SetLastPoll
// replaces the first snapshot.
func TestStats_LastPoll_OverwritesPrevious(t *testing.T) {
	s := imapproc.NewStats()

	s.SetLastPoll(imapproc.PollSnapshot{Received: 1, Healthy: false})
	s.SetLastPoll(imapproc.PollSnapshot{Received: 5, Healthy: true})

	got, _ := s.LastPoll()
	if got.Received != 5 {
		t.Errorf("Received = %d, want 5 (second snapshot)", got.Received)
	}
	if !got.Healthy {
		t.Error("Healthy = false, want true (second snapshot)")
	}
}

// TestStats_Healthy_NoPoll verifies that a connected Stats with no poll yet
// is considered healthy (no bad news yet).
func TestStats_Healthy_NoPoll(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()

	if !s.Healthy() {
		t.Error("expected Healthy() = true when connected and no poll has run yet")
	}
}

// TestStats_Healthy_ConnectedAndCleanPoll verifies healthy when connected and
// the last poll succeeded.
func TestStats_Healthy_ConnectedAndCleanPoll(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: true})

	if !s.Healthy() {
		t.Error("expected Healthy() = true when connected and last poll was healthy")
	}
}

// TestStats_Healthy_ConnectedButFailingPoll verifies unhealthy when connected
// but the last poll had failures.
func TestStats_Healthy_ConnectedButFailingPoll(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: false})

	if s.Healthy() {
		t.Error("expected Healthy() = false when connected but last poll had failures")
	}
}

// TestStats_Healthy_Disconnected verifies unhealthy when disconnected,
// regardless of poll outcome.
func TestStats_Healthy_Disconnected(t *testing.T) {
	s := imapproc.NewStats()
	s.SetDisconnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: true})

	if s.Healthy() {
		t.Error("expected Healthy() = false when disconnected, even with a clean poll")
	}
}

// TestStats_Healthy_Unknown verifies unhealthy when status is UNKNOWN (before
// any connection attempt).
func TestStats_Healthy_Unknown(t *testing.T) {
	s := imapproc.NewStats()
	// No SetConnected call — status stays UNKNOWN.
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: true})

	if s.Healthy() {
		t.Error("expected Healthy() = false with StatusUnknown")
	}
}

// TestStats_Healthy_RecoveryAfterFailure verifies that health recovers once a
// clean poll follows a failing one.
func TestStats_Healthy_RecoveryAfterFailure(t *testing.T) {
	s := imapproc.NewStats()
	s.SetConnected()
	s.SetLastPoll(imapproc.PollSnapshot{Healthy: false})

	if s.Healthy() {
		t.Error("expected unhealthy after failing poll")
	}

	s.SetLastPoll(imapproc.PollSnapshot{Healthy: true})
	if !s.Healthy() {
		t.Error("expected healthy after subsequent clean poll")
	}
}
