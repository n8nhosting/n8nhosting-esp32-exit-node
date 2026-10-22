package main

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestActivityRestartDedupeAndBrokerFailuresPreserveNodeAlerts(t *testing.T) {
	s := activityTestServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	nodes := []brokerNode{{MAC: "aa:bb", Online: false, RemoteIP: "1.2.3.4"}}
	s.observeActivity(nodes, true, now)
	s.observeActivity(nodes, true, now.Add(11*time.Minute))
	if s.overviewAlertCounts()["warn"] != 1 {
		t.Fatal("offline alert missing")
	}
	raised := eventCount(t, s, "alert.raised")
	offline := eventCount(t, s, "node.offline")
	restarted := &server{store: s.store, devices: s.devices}
	restarted.observeActivity(nodes, true, now.Add(12*time.Minute))
	if eventCount(t, s, "alert.raised") != raised || eventCount(t, s, "node.offline") != offline {
		t.Fatal("restart duplicated node or alert transition")
	}
	restarted.observeActivity(nil, false, now.Add(13*time.Minute))
	if restarted.overviewAlertCounts()["warn"] != 1 || restarted.overviewAlertCounts()["critical"] != 0 {
		t.Fatal("first broker failure changed node alerts or raised system alert")
	}
	restarted.observeActivity(nil, false, now.Add(14*time.Minute))
	if restarted.overviewAlertCounts()["critical"] != 1 || restarted.overviewAlertCounts()["warn"] != 1 {
		t.Fatal("broker failure threshold or stale node preservation failed")
	}
	restarted.observeActivity(nil, false, now.Add(15*time.Minute))
	if eventCount(t, s, "system.broker_unreachable") != 1 {
		t.Fatal("repeated system event")
	}
	// Restarting during the outage must not clear the durable system alert on
	// the new process's first failed attempt.
	restarted = &server{store: s.store, devices: s.devices}
	restarted.observeActivity(nil, false, now.Add(15*time.Minute+30*time.Second))
	if restarted.overviewAlertCounts()["critical"] != 1 || eventCount(t, s, "system.broker_recovered") != 0 {
		t.Fatal("restart falsely recovered unreachable broker")
	}
	nodes[0].Online = true
	restarted.observeActivity(nodes, true, now.Add(16*time.Minute))
	if restarted.overviewAlertCounts()["critical"] != 0 || eventCount(t, s, "system.broker_recovered") != 1 {
		t.Fatal("broker recovery missing")
	}
}

func TestAlertsMonthProjectionAndExpiry(t *testing.T) {
	s := activityTestServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, u := range []*User{
		{Username: "expired", Enabled: true, ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)},
		{Username: "soon", Enabled: true, ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)},
		{Username: "quota", Enabled: true, QuotaGB: 1, UsageBytes: 2 << 30, UsageMonth: monthKey(now)},
		{Username: "oldmonth", Enabled: true, QuotaGB: 1, UsageBytes: 2 << 30, UsageMonth: "2026-09"},
		{Username: "disabled", Enabled: false, ExpiresAt: now.Add(-time.Hour).Format(time.RFC3339)},
	} {
		if err := s.store.Create(u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.Update("oldmonth", func(u *User) error { u.UsageMonth = "2026-09"; return nil }); err != nil {
		t.Fatal(err)
	}
	s.observeActivity(nil, true, now)
	counts := s.overviewAlertCounts()
	if counts["warn"] != 1 || counts["info"] != 2 {
		t.Fatalf("counts %v", counts)
	}
	s.observeActivity(nil, true, now.Add(time.Minute))
	if eventCount(t, s, "user.expired") != 1 || eventCount(t, s, "user.over_quota") != 1 {
		t.Fatal("scheduler events repeated")
	}
	w := httptest.NewRecorder()
	s.handleAlerts(w, httptest.NewRequest("GET", "/api/alerts?severity=warn", nil))
	if w.Code != 200 {
		t.Fatalf("alerts: %s", w.Body)
	}
	w = httptest.NewRecorder()
	s.handleAlerts(w, httptest.NewRequest("GET", "/api/alerts?severity=error", nil))
	if w.Code != 400 {
		t.Fatal("invalid severity accepted")
	}
}

// When a same-IP sibling carried some of a slot board's dials, the board's
// activity says so once per observation in which the broker's counter grew;
// a broker restart that resets the counter still reports what it carried.
func TestActivityRecordsSlotOverflow(t *testing.T) {
	s := activityTestServer(t)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	n := brokerNode{MAC: "aa", Online: true, RemoteIP: "1.2.3.4", Firmware: "fw", DialsOverflow: 2}
	s.observeActivity([]brokerNode{n}, true, now)
	if eventCount(t, s, "node.slot_overflow") != 0 {
		t.Fatal("first sight is a baseline, not an event")
	}
	n.DialsOverflow = 5
	s.observeActivity([]brokerNode{n}, true, now.Add(time.Minute))
	s.observeActivity([]brokerNode{n}, true, now.Add(2*time.Minute))
	if got := eventCount(t, s, "node.slot_overflow"); got != 1 {
		t.Fatalf("overflow events = %d, want 1 (growth once, then unchanged)", got)
	}
	n.DialsOverflow = 1 // broker restarted and carried one dial since
	s.observeActivity([]brokerNode{n}, true, now.Add(3*time.Minute))
	if got := eventCount(t, s, "node.slot_overflow"); got != 2 {
		t.Fatalf("overflow events after a counter reset = %d, want 2", got)
	}
}

func TestActivityIPAndOTATransitionsPersist(t *testing.T) {
	s := activityTestServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	n := brokerNode{MAC: "aa", Online: true, RemoteIP: "1.2.3.4", Firmware: "old", OTAStatus: "sent", OTAAt: now.Format(time.RFC3339)}
	s.observeActivity([]brokerNode{n}, true, now)
	n.RemoteIP = "2.3.4.5"
	n.Firmware = "new"
	n.OTAStatus = "failed"
	s.observeActivity([]brokerNode{n}, true, now.Add(time.Minute))
	for _, kind := range []string{"node.ip_changed", "node.firmware_changed", "node.ota_failed"} {
		if eventCount(t, s, kind) != 1 {
			t.Fatalf("missing %s", kind)
		}
	}
	restarted := &server{store: s.store, devices: s.devices}
	restarted.observeActivity([]brokerNode{n}, true, now.Add(2*time.Minute))
	if eventCount(t, s, "node.ip_changed") != 1 || eventCount(t, s, "node.ota_failed") != 1 {
		t.Fatal("restart re-emitted transitions")
	}
	if _, ok := restarted.activity.alerts["node.ip_changed:aa"]; !ok {
		t.Fatal("IP changed alert lost on restart")
	}
}

func TestFutureOTATimestampDoesNotSuppressOfflineAlert(t *testing.T) {
	s := activityTestServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	n := brokerNode{MAC: "future-ota", OTAStatus: "sent", OTAAt: now.Add(time.Hour).Format(time.RFC3339)}
	s.observeActivity([]brokerNode{n}, true, now)
	s.observeActivity([]brokerNode{n}, true, now.Add(11*time.Minute))
	if _, ok := s.activity.alerts["node.offline:future-ota"]; !ok {
		t.Fatal("future OTA timestamp suppressed offline alert")
	}
	if _, ok := s.activity.alerts["node.ota_stalled:future-ota"]; ok {
		t.Fatal("future OTA timestamp incorrectly marked stalled")
	}
}
