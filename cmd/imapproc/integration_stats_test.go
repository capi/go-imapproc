package main

// Integration tests for Stats counters and health state that result from
// running ProcessUnread (via runOnceCfg) against a real in-process IMAP
// server. These tests verify that the Stats object is correctly wired through
// the run loop and processor.

import (
	"testing"

	"github.com/capi/go-imapproc/internal/imapproc"
)

// TestStats_AllSuccessPoll verifies that after a poll where every message is
// processed successfully the Stats object reflects received=N, success=N,
// failed=0 and the last poll is healthy.
func TestStats_AllSuccessPoll(t *testing.T) {
	_, user, addr := newTestServer(t)

	// Append two unread messages.
	appendMessage(t, user, testMailbox, testRawEmail)
	appendMessage(t, user, testMailbox, testRawEmail)

	script := writeTempScript(t, "exit 0")
	stats := imapproc.NewStats()
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    script,
		Stats:   stats,
	}

	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	r, s, f := stats.Counters()
	if r != 2 {
		t.Errorf("received = %d, want 2", r)
	}
	if s != 2 {
		t.Errorf("success = %d, want 2", s)
	}
	if f != 0 {
		t.Errorf("failed = %d, want 0", f)
	}

	snap, hasPoll := stats.LastPoll()
	if !hasPoll {
		t.Fatal("no last poll recorded")
	}
	if !snap.Healthy {
		t.Error("lastPoll.Healthy = false, want true")
	}
	if snap.Received != 2 {
		t.Errorf("lastPoll.Received = %d, want 2", snap.Received)
	}
	if snap.Success != 2 {
		t.Errorf("lastPoll.Success = %d, want 2", snap.Success)
	}
	if snap.Failed != 0 {
		t.Errorf("lastPoll.Failed = %d, want 0", snap.Failed)
	}

	if !stats.Healthy() {
		t.Error("Healthy() = false, want true after clean poll")
	}
}

// TestStats_MixedPoll verifies that when some messages fail (non-zero exit)
// the Stats reflects the correct failed count and the last poll is unhealthy.
func TestStats_MixedPoll(t *testing.T) {
	_, user, addr := newTestServer(t)

	// Two messages: first succeeds, second fails.
	appendMessage(t, user, testMailbox, testRawEmail)
	appendMessage(t, user, testMailbox, testRawEmail)

	// The script exits 0 first time, 1 second time. We track calls with a
	// temp file used as a counter sentinel.
	script := writeTempScript(t, `
COUNT_FILE="`+t.TempDir()+`/count"
COUNT=0
if [ -f "$COUNT_FILE" ]; then
  COUNT=$(cat "$COUNT_FILE")
fi
COUNT=$((COUNT + 1))
echo $COUNT > "$COUNT_FILE"
if [ "$COUNT" -ge 2 ]; then
  exit 1
fi
exit 0
`)
	stats := imapproc.NewStats()
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    script,
		Stats:   stats,
	}

	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	r, s, f := stats.Counters()
	if r != 2 {
		t.Errorf("received = %d, want 2", r)
	}
	if s != 1 {
		t.Errorf("success = %d, want 1", s)
	}
	if f != 1 {
		t.Errorf("failed = %d, want 1", f)
	}

	snap, hasPoll := stats.LastPoll()
	if !hasPoll {
		t.Fatal("no last poll recorded")
	}
	if snap.Healthy {
		t.Error("lastPoll.Healthy = true, want false (some messages failed)")
	}
	if snap.Failed != 1 {
		t.Errorf("lastPoll.Failed = %d, want 1", snap.Failed)
	}

	if stats.Healthy() {
		t.Error("Healthy() = true, want false after poll with failures")
	}
}

// TestStats_EmptyMailboxPoll verifies that when there are no unread messages
// the poll is still recorded as healthy with zero counters.
func TestStats_EmptyMailboxPoll(t *testing.T) {
	_, _, addr := newTestServer(t)
	// No messages appended.

	script := writeTempScript(t, "exit 0")
	stats := imapproc.NewStats()
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    script,
		Stats:   stats,
	}

	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	r, s, f := stats.Counters()
	if r != 0 || s != 0 || f != 0 {
		t.Errorf("counters = (%d, %d, %d), want (0, 0, 0)", r, s, f)
	}

	snap, hasPoll := stats.LastPoll()
	if !hasPoll {
		t.Fatal("expected a poll snapshot even for an empty mailbox")
	}
	if !snap.Healthy {
		t.Error("lastPoll.Healthy = false, want true for empty mailbox")
	}
	if snap.Received != 0 {
		t.Errorf("lastPoll.Received = %d, want 0", snap.Received)
	}
	if snap.Time.IsZero() {
		t.Error("lastPoll.Time is zero, want a non-zero timestamp")
	}
}

// TestStats_CumulativeAcrossPolls verifies that cumulative counters accumulate
// across two separate runOnceCfg calls while lastPoll reflects only the most
// recent pass.
func TestStats_CumulativeAcrossPolls(t *testing.T) {
	_, user, addr := newTestServer(t)

	script := writeTempScript(t, "exit 0")
	stats := imapproc.NewStats()
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    script,
		Stats:   stats,
	}

	// First poll: 1 message.
	appendMessage(t, user, testMailbox, testRawEmail)
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("first runOnceCfg: %v", err)
	}

	r1, s1, f1 := stats.Counters()
	if r1 != 1 || s1 != 1 || f1 != 0 {
		t.Errorf("after first poll counters = (%d, %d, %d), want (1, 1, 0)", r1, s1, f1)
	}

	// Second poll: 2 more messages.
	appendMessage(t, user, testMailbox, testRawEmail)
	appendMessage(t, user, testMailbox, testRawEmail)
	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("second runOnceCfg: %v", err)
	}

	r2, s2, f2 := stats.Counters()
	if r2 != 3 {
		t.Errorf("cumulative received = %d, want 3", r2)
	}
	if s2 != 3 {
		t.Errorf("cumulative success = %d, want 3", s2)
	}
	if f2 != 0 {
		t.Errorf("cumulative failed = %d, want 0", f2)
	}

	// lastPoll should only reflect the second pass (2 messages).
	snap, hasPoll := stats.LastPoll()
	if !hasPoll {
		t.Fatal("no last poll recorded")
	}
	if snap.Received != 2 {
		t.Errorf("lastPoll.Received = %d, want 2 (second pass only)", snap.Received)
	}
	if snap.Success != 2 {
		t.Errorf("lastPoll.Success = %d, want 2 (second pass only)", snap.Success)
	}
}

// TestStats_ConnectionStatusAfterRun verifies that the Stats connection status
// is set to UP after a successful runOnceCfg (i.e. the Run loop called
// SetConnected after login+select).
func TestStats_ConnectionStatusAfterRun(t *testing.T) {
	_, _, addr := newTestServer(t)

	script := writeTempScript(t, "exit 0")
	stats := imapproc.NewStats()
	cfg := imapproc.Config{
		User:    testUser,
		Pass:    testPass,
		Mailbox: testMailbox,
		Exec:    script,
		Stats:   stats,
	}

	if err := runOnceCfg(t, addr, cfg); err != nil {
		t.Fatalf("runOnceCfg: %v", err)
	}

	// After a successful run the connection status must be UP.
	if got := stats.ConnStatus(); got != imapproc.StatusUp {
		t.Errorf("ConnStatus = %q, want %q", got, imapproc.StatusUp)
	}
}
