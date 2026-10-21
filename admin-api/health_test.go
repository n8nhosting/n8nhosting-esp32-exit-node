package main

import (
	"fmt"
	"testing"
	"time"
)

// Check every possible combination of the optional health components. Dial
// history cannot exist without availability history: they share metrics_node.
func TestHealthOptionalComponentMatrix(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	for history := 0; history < 3; history++ {
		// Contract oracle: live availability is 1; historical availability .8;
		// dial success .9 (when present). Each enabled headroom scores .5.
		wantByOptionalCount := [][]int{{100, 90, 83, 79}, {80, 74, 70, 67}, {84, 80, 77, 74}}[history]
		for mask := 0; mask < 8; mask++ {
			t.Run(fmt.Sprintf("history%d_optional%03b", history, mask), func(t *testing.T) {
				mac := fmt.Sprintf("matrix-%d-%d", history, mask)
				if history > 0 {
					ok, failed := 0, 0
					if history == 2 {
						ok, failed = 90, 10
					}
					if _, err := s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'5m',?,0.8,0,0,?,?,0)`, now.Add(-5*time.Minute).Unix(), mac, ok, failed); err != nil {
						t.Fatal(err)
					}
				}
				v := deviceView{brokerNode: brokerNode{MAC: mac, Online: true}, Status: "online"}
				count := 0
				if mask&1 != 0 {
					v.MaxStreams, v.Active = 10, 5
					count++
				}
				if mask&6 != 0 {
					v.Stats = &nodeStats{At: now.Format(time.RFC3339), CPUPct: -1}
				}
				if mask&2 != 0 {
					v.Stats.HeapTotal, v.Stats.HeapFree = 200*1024, 30*1024
					count++
				}
				if mask&4 != 0 {
					v.Stats.CPUPct = 80
					count++
				}
				h := s.healthFor(v, now)
				if h.Score != wantByOptionalCount[count] {
					t.Fatalf("score=%d want=%d: %+v", h.Score, wantByOptionalCount[count], h)
				}
				if (h.Availability24h != nil) != (history > 0) || (h.DialSuccess24h != nil) != (history == 2) || (h.StreamLoad != nil) != (mask&1 != 0) || (h.HeapHeadroom != nil) != (mask&2 != 0) || (h.CPULoad != nil) != (mask&4 != 0) {
					t.Fatalf("component presence: %+v", h)
				}
			})
		}
	}
}

func TestHealthResourceScoreThresholds(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	for _, c := range []struct {
		name        string
		free, total uint32
		cpu, score  int
	}{
		{"ram_absolute_exhausted", 8 * 1024, 100 * 1024, -1, 80},
		{"ram_fraction_exhausted", 10 * 1024, 200 * 1024, -1, 80},
		{"ram_half_fraction", 30 * 1024, 200 * 1024, -1, 90},
		{"ram_full_both", 48 * 1024, 192 * 1024, -1, 100},
		{"ram_fraction_limits", 48 * 1024, 256 * 1024, -1, 94},
		{"ram_absolute_limits", 32 * 1024, 64 * 1024, -1, 92},
		{"cpu_zero", 0, 0, 0, 100},
		{"cpu_full_boundary", 0, 0, 60, 100},
		{"cpu_half", 0, 0, 80, 90},
		{"cpu_warning", 0, 0, 85, 88},
		{"cpu_saturated", 0, 0, 100, 80},
		{"cpu_unknown", 0, 0, -1, 100},
		{"cpu_invalid", 0, 0, 101, 100},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := deviceView{brokerNode: brokerNode{MAC: c.name, Online: true, Stats: &nodeStats{At: now.Format(time.RFC3339), HeapFree: c.free, HeapTotal: c.total, CPUPct: c.cpu}}, Status: "online"}
			if h := s.healthFor(v, now); h.Score != c.score {
				t.Fatalf("score=%d want=%d", h.Score, c.score)
			}
		})
	}
}
