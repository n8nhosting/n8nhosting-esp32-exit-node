package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"
)

type activeAlert struct {
	Key         string         `json:"key"`
	Kind        string         `json:"kind"`
	Severity    string         `json:"severity"`
	SubjectType string         `json:"subject_type"`
	SubjectID   string         `json:"subject_id"`
	Since       string         `json:"since"`
	Message     string         `json:"message"`
	Data        map[string]any `json:"data"`
}

type nodeActivity struct {
	Online        bool   `json:"online"`
	IP            string `json:"remote_ip"`
	Firmware      string `json:"firmware"`
	OTA           string `json:"ota_status"`
	OfflineSince  string `json:"offline_since,omitempty"`
	IPChangedAt   string `json:"ip_changed_at,omitempty"`
	PinnedOffline int64  `json:"pinned_offline"`
}

type activityState struct {
	// observeMu serializes evaluation; mu only guards the published cache and
	// is never held while querying SQLite or reading the store.
	observeMu   sync.Mutex
	mu          sync.RWMutex
	initialized bool
	nodes       map[string]nodeActivity
	alerts      map[string]activeAlert
	failures    int
	brokerSeen  bool
	asOf        string
	lastPrune   time.Time
}

func (s *server) restoreActivity() (map[string]nodeActivity, map[string]activeAlert, error) {
	nodes, alerts := map[string]nodeActivity{}, map[string]activeAlert{}
	rows, err := s.store.DB().Query(`SELECT kind,subject_id,data FROM events WHERE id IN (
		SELECT MAX(id) FROM events WHERE kind IN ('node.first_seen','node.online','node.offline','node.ip_changed','node.firmware_changed','node.ota_sent','node.ota_ok','node.ota_failed','alert.raised','alert.cleared') GROUP BY subject_type,subject_id)`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, id, data string
		if err := rows.Scan(&kind, &id, &data); err != nil {
			return nil, nil, err
		}
		if kind == "alert.raised" {
			var a activeAlert
			if json.Unmarshal([]byte(data), &a) == nil && a.Key != "" {
				alerts[a.Key] = a
			}
		} else if kind != "alert.cleared" {
			var n nodeActivity
			if json.Unmarshal([]byte(data), &n) == nil {
				nodes[id] = n
			}
		}
	}
	return nodes, alerts, rows.Err()
}

// observeActivity is invoked once per sampler attempt. Failed broker polls
// preserve node alerts and their history: stale is not evidence of recovery.
func (s *server) observeActivity(nodes []brokerNode, brokerOK bool, now time.Time) {
	a := &s.activity
	a.observeMu.Lock()
	defer a.observeMu.Unlock()
	if !a.initialized {
		prior, alerts, err := s.restoreActivity()
		if err != nil {
			log.Printf("activity: restore: %v", err)
			return
		}
		a.nodes = prior
		a.mu.Lock()
		a.alerts = alerts
		a.mu.Unlock()
		a.initialized = true
	}
	a.mu.RLock()
	prevAlerts := make(map[string]activeAlert, len(a.alerts))
	for k, v := range a.alerts {
		prevAlerts[k] = v
	}
	a.mu.RUnlock()
	next := map[string]activeAlert{}
	add := func(kind, severity, typ, id, message string, data map[string]any) {
		key := kind
		if id != "" {
			key += ":" + id
		}
		since := now.UTC().Format(time.RFC3339)
		if old, ok := prevAlerts[key]; ok {
			since = old.Since
		}
		next[key] = activeAlert{key, kind, severity, typ, id, since, message, data}
	}
	if !brokerOK {
		a.failures++
		for key, v := range prevAlerts {
			if v.SubjectType == "node" || v.SubjectType == "device" || key == "system.slot_dials_refused" {
				next[key] = v
			}
		}
		_, alreadyUnreachable := prevAlerts["system.broker_unreachable"]
		if a.failures >= 2 || alreadyUnreachable {
			add("system.broker_unreachable", "critical", "system", "", "Broker has failed two consecutive polls", map[string]any{"failed_polls": a.failures})
		}
	} else {
		a.failures = 0
		recs := map[string]*Device{}
		if s.devices != nil {
			for _, d := range s.devices.List() {
				recs[d.MAC] = d
			}
		}
		usersByMAC := map[string][]string{}
		for _, u := range s.store.List() {
			if !isReservedUsername(u.Username) && takesSeat(u, now) {
				usersByMAC[u.DeviceMAC] = append(usersByMAC[u.DeviceMAC], u.Username)
			}
		}
		seen := map[string]nodeActivity{}
		pinnedGrew := false
		for _, n := range nodes {
			old, known := a.nodes[n.MAC]
			cur := nodeActivity{Online: n.Online, IP: n.RemoteIP, Firmware: n.Firmware, OTA: n.OTAStatus, OfflineSince: old.OfflineSince, IPChangedAt: old.IPChangedAt, PinnedOffline: n.DialsPinnedOffline}
			if n.Online {
				cur.OfflineSince = ""
			} else if cur.OfflineSince == "" {
				cur.OfflineSince = now.UTC().Format(time.RFC3339)
			}
			if known && old.IP != "" && n.RemoteIP != "" && old.IP != n.RemoteIP {
				cur.IPChangedAt = now.UTC().Format(time.RFC3339)
			}
			emitNode := func(kind, severity, msg string) { s.emitAt(now, kind, severity, "node", n.MAC, "poller", msg, cur) }
			if !known {
				emitNode("node.first_seen", "info", "Device first seen")
			}
			if !known || old.Online != n.Online {
				if n.Online {
					emitNode("node.online", "info", "Device came online")
				} else {
					emitNode("node.offline", "warn", "Device went offline")
				}
			}
			if known && old.IP != "" && n.RemoteIP != "" && old.IP != n.RemoteIP {
				emitNode("node.ip_changed", "warn", "Device public IP changed")
			}
			if known && old.Firmware != n.Firmware {
				emitNode("node.firmware_changed", "info", "Device firmware changed")
			}
			if (!known || old.OTA != n.OTAStatus) && (n.OTAStatus == "sent" || n.OTAStatus == "ok" || n.OTAStatus == "failed") {
				sev := "info"
				if n.OTAStatus == "failed" {
					sev = "error"
				}
				emitNode("node.ota_"+n.OTAStatus, sev, "Device OTA "+n.OTAStatus)
			}
			seen[n.MAC] = cur
			if a.brokerSeen && known && n.DialsPinnedOffline > old.PinnedOffline {
				pinnedGrew = true
			}
			d := recs[n.MAC]
			kind, name, capacity, slot := "shared", boardDisplayName(n.MAC, ""), 0.0, 0
			if d != nil {
				kind, capacity, slot = d.Kind, d.CapacityMbps, d.Slot
				name = boardDisplayName(n.MAC, d.Name)
			}
			otaAt, _ := time.Parse(time.RFC3339, n.OTAAt)
			otaAge := now.Sub(otaAt)
			updating := n.OTAStatus == "sent" && !otaAt.IsZero() && otaAge >= 0 && otaAge <= 15*time.Minute
			if !n.Online && !n.Blocked && !updating {
				since, _ := time.Parse(time.RFC3339, cur.OfflineSince)
				threshold := atoiDefault(envOr("ALERT_OFFLINE_MIN", "10"), 10)
				if threshold < 0 {
					threshold = 10
				}
				if now.Sub(since) > time.Duration(threshold)*time.Minute {
					severity := "warn"
					if len(usersByMAC[n.MAC]) > 0 {
						severity = "critical"
					}
					minutes := int(now.Sub(since).Minutes())
					sort.Strings(usersByMAC[n.MAC])
					add("node.offline", severity, "node", n.MAC, fmt.Sprintf("%s offline %d min (%d bound users)", name, minutes, len(usersByMAC[n.MAC])), map[string]any{"minutes": minutes, "users": usersByMAC[n.MAC]})
				}
			}
			if n.OTAStatus == "failed" {
				add("node.ota_failed", "warn", "node", n.MAC, name+" OTA failed", nil)
			}
			if n.OTAStatus == "sent" && !otaAt.IsZero() && now.Sub(otaAt) > 15*time.Minute {
				add("node.ota_stalled", "warn", "node", n.MAC, name+" OTA has stalled", nil)
			}
			changed, _ := time.Parse(time.RFC3339, cur.IPChangedAt)
			if !changed.IsZero() && now.Sub(changed) < 24*time.Hour {
				add("node.ip_changed", "warn", "node", n.MAC, name+" public IP changed in the last 24 hours", nil)
			}
			if n.Slot != slot || (n.Pool != nil && *n.Pool != poolFlag(kind)) {
				add("device.slot_mismatch", "warn", "device", n.MAC, name+" broker routing differs from saved settings", nil)
			}
			if n.Online && kind != "dedicated" && capacity <= 0 {
				add("device.unmeasured", "info", "device", n.MAC, name+" needs a capacity measurement", nil)
			}
		}
		a.nodes = seen
		a.brokerSeen = true
		if pinnedGrew {
			add("system.slot_dials_refused", "warn", "system", "", "Slot customers were refused because their device was unavailable", nil)
		}
	}
	for _, u := range s.store.List() {
		if isReservedUsername(u.Username) || !u.Enabled {
			continue
		}
		usage := u.UsageBytes
		if u.UsageMonth != monthKey(now) {
			usage = 0
		}
		if u.QuotaGB > 0 && float64(usage) >= u.QuotaGB*1024*1024*1024 {
			add("user.over_quota", "warn", "user", u.Username, u.Username+" reached their quota", nil)
		}
		exp, err := time.Parse(time.RFC3339, u.ExpiresAt)
		if err != nil {
			continue
		}
		days := atoiDefault(envOr("ALERT_EXPIRING_DAYS", "3"), 3)
		if days < 0 {
			days = 3
		}
		if !exp.After(now) {
			add("user.expired_enabled", "info", "user", u.Username, u.Username+" is enabled but expired", nil)
		} else if exp.Sub(now) <= time.Duration(days)*24*time.Hour {
			add("user.expiring", "info", "user", u.Username, u.Username+" expires soon", map[string]any{"expires_at": u.ExpiresAt})
		}
	}
	// Persist each transition before publishing it. Failed writes keep the prior
	// state so the next sampler attempt retries instead of losing the audit trail.
	for key, v := range next {
		if _, ok := prevAlerts[key]; ok {
			continue
		}
		if !s.emitAt(now, "alert.raised", v.Severity, "alert", key, "poller", v.Message, v) {
			delete(next, key)
			continue
		}
		if key == "system.broker_unreachable" {
			s.emitAt(now, "system.broker_unreachable", "error", "system", "", "poller", v.Message, nil)
		}
		if v.Kind == "user.expired_enabled" {
			s.emitAt(now, "user.expired", "warn", "user", v.SubjectID, "scheduler", v.Message, nil)
		}
		if v.Kind == "user.over_quota" {
			s.emitAt(now, "user.over_quota", "warn", "user", v.SubjectID, "scheduler", v.Message, nil)
		}
	}
	for key, v := range prevAlerts {
		if _, ok := next[key]; ok {
			continue
		}
		if !s.emitAt(now, "alert.cleared", v.Severity, "alert", key, "poller", v.Message+" — cleared", v) {
			next[key] = v
			continue
		}
		if key == "system.broker_unreachable" {
			s.emitAt(now, "system.broker_recovered", "info", "system", "", "poller", "Broker connection recovered", nil)
		}
	}
	a.mu.Lock()
	a.alerts = next
	a.asOf = now.UTC().Format(time.RFC3339)
	a.mu.Unlock()
	if a.lastPrune.IsZero() || now.Sub(a.lastPrune) >= time.Hour {
		s.pruneEvents(now)
		a.lastPrune = now
	}
}

func (s *server) overviewAlertCounts() map[string]int {
	s.activity.mu.RLock()
	defer s.activity.mu.RUnlock()
	counts := map[string]int{"critical": 0, "warn": 0, "info": 0}
	for _, a := range s.activity.alerts {
		counts[a.Severity]++
	}
	return counts
}

func (s *server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method"})
		return
	}
	sev := r.URL.Query().Get("severity")
	if sev != "" && sev != "critical" && sev != "warn" && sev != "info" {
		writeJSON(w, 400, map[string]string{"error": "invalid severity"})
		return
	}
	s.activity.mu.RLock()
	out := make([]activeAlert, 0)
	asOf := s.activity.asOf
	counts := map[string]int{"critical": 0, "warn": 0, "info": 0}
	for _, a := range s.activity.alerts {
		counts[a.Severity]++
		if sev == "" || sev == a.Severity {
			out = append(out, a)
		}
	}
	s.activity.mu.RUnlock()
	rank := map[string]int{"critical": 0, "warn": 1, "info": 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Severity] != rank[out[j].Severity] {
			return rank[out[i].Severity] < rank[out[j].Severity]
		}
		if out[i].Since != out[j].Since {
			return out[i].Since < out[j].Since
		}
		return out[i].Key < out[j].Key
	})
	writeJSON(w, 200, map[string]any{"as_of": asOf, "counts": counts, "alerts": out})
}
