package main

import (
	"testing"
	"time"
)

func TestOTAStatusNeverClaimsRollbackWithoutProof(t *testing.T) {
	now := time.Now().UTC()
	for _, tc := range []struct{ name, status, at, primary, tag string }{
		{"recent sent", "sent", now.Add(-time.Minute).Format(time.RFC3339), "updating", ""},
		{"failed", "failed", now.Format(time.RFC3339), "ota_failed", "ota_failed"},
		{"timeout", "timeout", now.Add(-16 * time.Minute).Format(time.RFC3339), "ota_failed", "ota_stalled"},
		{"old broker stale sent", "sent", now.Add(-16 * time.Minute).Format(time.RFC3339), "ota_failed", "ota_stalled"},
		{"sent without date", "sent", "", "ota_failed", "ota_stalled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := deviceView{brokerNode: brokerNode{Online: true, OTAStatus: tc.status, OTAAt: tc.at}, Kind: "shared", CapacityMbps: 10}
			status, tags := deviceStatus(v, now)
			if status != tc.primary || (tc.tag != "" && (len(tags) != 1 || tags[0] != tc.tag)) {
				t.Fatalf("status/tags: %s %v", status, tags)
			}
			v.Online = false
			if tc.status != "sent" || tc.primary != "updating" {
				status, tags = deviceStatus(v, now)
				if status != "offline" || len(tags) != 1 || tags[0] != tc.tag {
					t.Fatalf("offline precedence: %s %v", status, tags)
				}
			}
		})
	}
}

func TestOTATimeoutActivityAndAlertsDeduplicateAcrossRestart(t *testing.T) {
	s := activityTestServer(t)
	now := time.Now().UTC()
	nodes := []brokerNode{{MAC: "board", Online: true, OTAStatus: "timeout", OTAAt: now.Add(-16 * time.Minute).Format(time.RFC3339), OTAError: "confirmation_timeout"}}
	s.observeActivity(nodes, true, now)
	s.observeActivity(nodes, true, now.Add(time.Minute))
	restarted := &server{store: s.store, devices: s.devices}
	restarted.observeActivity(nodes, true, now.Add(2*time.Minute))
	if eventCount(t, s, "node.ota_timeout") != 1 {
		t.Fatal("timeout event was missing or duplicated")
	}
	if _, ok := restarted.activity.alerts["node.ota_stalled:board"]; !ok {
		t.Fatal("durable timeout alert missing")
	}
	nodes[0].OTAStatus, nodes[0].OTAError = "ok", ""
	restarted.observeActivity(nodes, true, now.Add(3*time.Minute))
	if _, ok := restarted.activity.alerts["node.ota_stalled:board"]; ok {
		t.Fatal("late successful update did not clear timeout alert")
	}
}
