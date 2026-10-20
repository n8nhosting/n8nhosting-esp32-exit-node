package main

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"time"
)

// Device/overview endpoints proxy the broker's internal status/nodes API so the
// dashboard has a single origin (admin-api) and the broker stays off the public
// network. BROKER_STATUS_URL defaults to the in-compose broker.

func (s *server) brokerURL() string { return envOr("BROKER_STATUS_URL", "http://broker:7001") }

func (s *server) brokerGET(path string, v any) error {
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Get(s.brokerURL() + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// brokerNode mirrors the broker's /nodes JSON.
type brokerNode struct {
	MAC        string `json:"mac"`
	Online     bool   `json:"online"`
	Blocked    bool   `json:"blocked"`
	RemoteIP   string `json:"remote_ip"`
	Firmware   string `json:"firmware"`
	SSID       string `json:"ssid"`
	UptimeSec  uint64 `json:"uptime_sec"`
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	IdleConns  int64  `json:"idle_conns"`
	Active     int64  `json:"active_dials"`
	DialsOK    int64  `json:"dials_ok"`
	DialsErr   int64  `json:"dials_err"`
	BytesUp    int64  `json:"bytes_up"`
	BytesDown  int64  `json:"bytes_down"`
	PendingOTA string `json:"pending_ota,omitempty"`
	OTATarget  string `json:"ota_target,omitempty"`
	OTAStatus  string `json:"ota_status,omitempty"`
	OTAAt      string `json:"ota_at,omitempty"`
}

type deviceView struct {
	brokerNode
	Geo   geoInfo  `json:"geo"`
	Users []string `json:"users"` // usernames bound to this device
}

// GET /api/devices — list ESP32 exit nodes enriched with geo/ISP and the users
// bound to each device.
func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	var nodes []brokerNode
	if err := s.brokerGET("/nodes", &nodes); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "broker unreachable"})
		return
	}
	// users bound per MAC
	byMAC := map[string][]string{}
	for _, u := range s.store.List() {
		if u.DeviceMAC != "" {
			byMAC[u.DeviceMAC] = append(byMAC[u.DeviceMAC], u.Username)
		}
	}
	out := make([]deviceView, 0, len(nodes))
	for _, n := range nodes {
		users := byMAC[n.MAC]
		sort.Strings(users)
		out = append(out, deviceView{brokerNode: n, Geo: s.geo.lookup(n.RemoteIP), Users: users})
	}
	writeJSON(w, http.StatusOK, out)
}

// POST /api/devices/{mac}/block|allow|forget|ota|ota-cancel — proxy to the broker.
func (s *server) handleDeviceAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	rest := r.URL.Path[len("/api/devices/"):]
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Post(s.brokerURL()+"/nodes/"+rest, "application/json", r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "broker unreachable"})
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GET /api/overview — one call for the dashboard header cards.
func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	users := s.store.List()
	active, over, expired := 0, 0, 0
	var monthBytes int64
	now := time.Now()
	for _, u := range users {
		if u.Enabled {
			active++
		}
		if u.ExpiresAt != "" {
			if exp, e := time.Parse(time.RFC3339, u.ExpiresAt); e == nil && now.After(exp) {
				expired++
			}
		}
		if u.QuotaGB > 0 && float64(u.UsageBytes) >= u.QuotaGB*1024*1024*1024 {
			over++
		}
		monthBytes += u.UsageBytes
	}
	broker := map[string]any{}
	_ = s.brokerGET("/status", &broker)
	writeJSON(w, http.StatusOK, map[string]any{
		"users": map[string]any{
			"total": len(users), "active": active, "over_quota": over, "expired": expired,
		},
		"month_gb": float64(monthBytes) / (1024 * 1024 * 1024),
		"broker":   broker,
	})
}
