package main

import (
	"fmt"
	"math"
	"time"
)

type deviceHealth struct {
	Score           int      `json:"score"`
	Availability24h *float64 `json:"availability_24h"`
	DialSuccess24h  *float64 `json:"dial_success_24h"`
	StreamLoad      *float64 `json:"stream_load"`
	HeapHeadroom    *float64 `json:"heap_headroom"`
	CPULoad         *float64 `json:"cpu_load"`
	Reasons         []string `json:"reasons"`
}

func deviceStatus(v deviceView, now time.Time) (string, []string) {
	at, err := time.Parse(time.RFC3339, v.OTAAt)
	tags := []string{}
	sentActive := v.OTAStatus == "sent" && err == nil && now.Sub(at) >= 0 && now.Sub(at) <= 15*time.Minute
	otaFailed := v.OTAStatus == "failed" || v.OTAStatus == "timeout" || (v.OTAStatus == "sent" && !sentActive)
	if v.OTAStatus == "failed" {
		tags = append(tags, "ota_failed")
	}
	if v.OTAStatus == "timeout" || (v.OTAStatus == "sent" && !sentActive) {
		tags = append(tags, "ota_stalled")
	}
	switch {
	case v.Blocked:
		return "blocked", tags
	case sentActive:
		return "updating", nil
	case !v.Online:
		return "offline", tags
	case otaFailed:
		return "ota_failed", tags
	case v.Kind != "dedicated" && v.CapacityMbps <= 0:
		return "unmeasured", tags
	}
	if v.OTAStatus == "queued" {
		tags = append(tags, "ota_queued")
	}
	if v.SlotMismatch {
		tags = append(tags, "slot_mismatch")
	}
	return "online", tags
}

// healthFor exposes raw components as fractions and scores normalized resources.
// RAM is fully healthy above BOTH 25% and 48 KiB, exhausted below EITHER
// 5% or 8 KiB. CPU <=60% scores fully and degrades linearly to zero at 100%.
func (s *server) healthFor(v deviceView, now time.Time) deviceHealth {
	h := deviceHealth{Reasons: []string{}}
	availability := 0.0
	if v.Online {
		availability = 1
	}
	var avg *float64
	var ok, errs int64
	// The last 24h is within the 48h fine-grained retention window; using 5m
	// alone avoids counting the hourly rollup twice.
	err := s.store.DB().QueryRow(`SELECT AVG(online_frac),COALESCE(SUM(dials_ok),0),COALESCE(SUM(dials_err),0) FROM metrics_node WHERE mac=? AND step='5m' AND ts>=?`, v.MAC, now.Add(-24*time.Hour).Unix()).Scan(&avg, &ok, &errs)
	if err == nil && avg != nil {
		h.Availability24h = avg
		availability = *avg
	} else {
		h.Reasons = append(h.Reasons, "No availability history yet; score uses live connection state")
	}
	if ok+errs >= 20 {
		ratio := clamp(float64(ok) / float64(ok+errs))
		h.DialSuccess24h = &ratio
		if ratio < .99 {
			h.Reasons = append(h.Reasons, fmt.Sprintf("Dial failures %.1f%%", (1-ratio)*100))
		}
	}
	if v.MaxStreams > 0 {
		load := clamp(float64(v.Active) / float64(v.MaxStreams))
		h.StreamLoad = &load
		if load >= .8 {
			h.Reasons = append(h.Reasons, "Stream capacity is nearly full")
		}
	}
	sum, weight := 0.0, 0.0
	add := func(value, w float64) { sum += clamp(value) * w; weight += w }
	add(availability, 40)
	if h.Availability24h != nil && availability < .99 {
		h.Reasons = append(h.Reasons, fmt.Sprintf("Availability %.1f%% (24h)", availability*100))
	}
	if h.DialSuccess24h != nil {
		add(*h.DialSuccess24h, 30)
	}
	if h.StreamLoad != nil {
		add(1-*h.StreamLoad, 10)
	}
	st := v.Stats
	fresh := false
	if st != nil {
		if at, err := time.Parse(time.RFC3339, st.At); err == nil {
			age := now.Sub(at)
			fresh = age >= 0 && age <= 120*time.Second
		}
	}
	if fresh {
		if st.HeapTotal > 0 && st.HeapFree <= st.HeapTotal {
			ratio := clamp(float64(st.HeapFree) / float64(st.HeapTotal))
			h.HeapHeadroom = &ratio
			score := math.Min(clamp((ratio-.05)/.20), clamp((float64(st.HeapFree)-8192)/40960))
			add(score, 10)
			if ratio < .15 || st.HeapFree < 24576 {
				h.Reasons = append(h.Reasons, "Internal RAM is running low")
			}
		}
		if st.CPUPct >= 0 && st.CPUPct <= 100 {
			load := clamp(float64(st.CPUPct) / 100)
			h.CPULoad = &load
			add(clamp((100-float64(st.CPUPct))/40), 10)
			if st.CPUPct >= 85 {
				h.Reasons = append(h.Reasons, "CPU load is high")
			}
		}
	} else if st != nil {
		h.Reasons = append(h.Reasons, "Health sample is stale")
	}
	if weight > 0 {
		h.Score = int(math.Round(100 * sum / weight))
	}
	switch v.Status {
	case "blocked":
		h.Score = 0
		h.Reasons = append(h.Reasons, "Device is blocked")
	case "offline", "updating":
		if h.Score > 30 {
			h.Score = 30
		}
		h.Reasons = append(h.Reasons, "Device is "+v.Status)
	}
	if len(h.Reasons) == 0 {
		h.Reasons = append(h.Reasons, "Available telemetry is healthy")
	}
	if v.Status == "ota_failed" {
		h.Reasons = append(h.Reasons, "Firmware update was not confirmed")
	}
	return h
}
