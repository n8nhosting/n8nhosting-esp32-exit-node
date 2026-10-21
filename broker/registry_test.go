package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Uptime keeps running from the moment it was reported, freezes when the
// link closes, and restarts from the next HELLO's value. Firmware reports
// uptime once, in HELLO, seconds after boot; the broker is what keeps it
// current for every board, old firmware included.
func TestUptimeKeepsRunning(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	now := t0
	timeNow = func() time.Time { return now }
	defer func() { timeNow = time.Now }()

	r := newRegistry("")
	n := r.touch("aa:bb:cc:dd:ee:01", "2.4.21-mux1.4", "27.1.1.1", 10, "", "")
	n.addMux(1)
	now = t0.Add(90 * time.Second)
	if got := r.list()[0].UptimeSec; got != 100 {
		t.Fatalf("linked 90 s after a HELLO at uptime 10: got %d want 100", got)
	}
	now = t0.Add(120 * time.Second)
	n.addMux(-1)
	r.linkDown(n)
	now = t0.Add(500 * time.Second)
	v := r.list()[0]
	if v.UptimeSec != 130 {
		t.Fatalf("frozen at link down: got %d want 130", v.UptimeSec)
	}
	if v.LastSeen != t0.Add(120*time.Second).Format(time.RFC3339) {
		t.Fatalf("last_seen must be the link-down time, got %s", v.LastSeen)
	}
	// a reboot: the next HELLO carries a small uptime and it runs from there
	now = t0.Add(600 * time.Second)
	r.touch("aa:bb:cc:dd:ee:01", "2.4.21-mux1.4", "27.1.1.1", 3, "", "")
	now = t0.Add(660 * time.Second)
	if got := r.list()[0].UptimeSec; got != 63 {
		t.Fatalf("after a new HELLO at uptime 3: got %d want 63", got)
	}
}

// A STATS sample restarts the running clock from the uptime it carries and
// is what /nodes shows as stats.
func TestStatsSampleUpdatesUptime(t *testing.T) {
	t0 := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	now := t0
	timeNow = func() time.Time { return now }
	defer func() { timeNow = time.Now }()

	r := newRegistry("")
	n := r.touch("aa:bb:cc:dd:ee:03", "2.4.23-c3", "27.1.1.3", 7, "", "esp32c3")
	now = t0.Add(30 * time.Second)
	r.stats(n, nodeStats{UptimeSec: 600, HeapFree: 140000, HeapTotal: 320000, CPUPct: 12})
	now = t0.Add(45 * time.Second)
	v := r.list()[0]
	if v.UptimeSec != 615 {
		t.Fatalf("uptime after STATS up=600 15 s ago: got %d want 615", v.UptimeSec)
	}
	if v.Stats == nil || v.Stats.HeapFree != 140000 || v.Stats.CPUPct != 12 || v.Stats.At != t0.Add(30*time.Second).Format(time.RFC3339) {
		t.Fatalf("stats = %+v", v.Stats)
	}
}

// A node read back from disk has no live report: it shows what was stored
// until it HELLOs again, and never a stats sample.
func TestUptimeStoredNodeDoesNotGrow(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nodes.json")
	os.WriteFile(file, []byte(`[{"mac":"aa:bb:cc:dd:ee:02","uptime_sec":42,"first_seen":"2026-10-20T00:00:00Z","last_seen":"2026-10-20T00:00:00Z"}]`), 0o644)
	timeNow = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	defer func() { timeNow = time.Now }()
	r := newRegistry(file)
	v := r.list()[0]
	if v.UptimeSec != 42 || v.Stats != nil {
		t.Fatalf("stored node: uptime %d stats %+v", v.UptimeSec, v.Stats)
	}
}
