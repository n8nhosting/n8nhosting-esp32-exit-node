package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// node is one ESP32 exit device, identified by its WiFi STA MAC address.
type node struct {
	MAC        string `json:"mac"`
	Firmware   string `json:"firmware,omitempty"`
	RemoteIP   string `json:"remote_ip,omitempty"` // public IP the node dials from (= exit IP)
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	UptimeSec  uint64 `json:"uptime_sec"`
	SSID       string `json:"ssid,omitempty"`
	Blocked    bool   `json:"blocked"`
	Note       string `json:"note,omitempty"`
	PendingOTA string `json:"pending_ota,omitempty"` // firmware URL to push on next connection
	// OTA tracking: queued -> sent -> ok | failed. "failed" means the node came
	// back after a reboot still on its old firmware (download failed, or the new
	// image did not confirm itself and the bootloader rolled it back).
	OTATarget string `json:"ota_target,omitempty"` // firmware version expected after the update
	OTAStatus string `json:"ota_status,omitempty"`
	OTAAt     string `json:"ota_at,omitempty"`
	otaFromFW string // firmware when the OTA was delivered
	otaUptime uint64 // uptime when the OTA was delivered

	// live counters (atomic)
	Idle      int64 `json:"idle_conns"`   // parked connections right now (legacy one-shot links)
	Mux       int64 `json:"mux_links"`    // live multiplexed links
	Active    int64 `json:"active_dials"` // relays in progress
	DialsOK   int64 `json:"dials_ok"`
	DialsErr  int64 `json:"dials_err"`
	BytesUp   int64 `json:"bytes_up"`
	BytesDown int64 `json:"bytes_down"`
	lastSeenT time.Time
	blk       atomic.Bool // mirror of Blocked for lock-free readers
}

func (n *node) isBlocked() bool { return n.blk.Load() }

func (n *node) addIdle(d int64)   { atomic.AddInt64(&n.Idle, d) }
func (n *node) addActive(d int64) { atomic.AddInt64(&n.Active, d) }
func (n *node) addMux(d int64)    { atomic.AddInt64(&n.Mux, d) }
func (n *node) addStats(ok, errs, up, down int64) {
	atomic.AddInt64(&n.DialsOK, ok)
	atomic.AddInt64(&n.DialsErr, errs)
	atomic.AddInt64(&n.BytesUp, up)
	atomic.AddInt64(&n.BytesDown, down)
}

// Online means it has parked/active connections or was seen very recently.
func (n *node) online() bool {
	return atomic.LoadInt64(&n.Idle) > 0 || atomic.LoadInt64(&n.Active) > 0 || atomic.LoadInt64(&n.Mux) > 0 ||
		time.Since(n.lastSeenT) < 45*time.Second
}

// snapshot copies the node with its live counters read atomically, so the
// JSON encoder never reads fields that relays are updating.
func (n *node) snapshot() *node {
	return &node{MAC: n.MAC, Firmware: n.Firmware, RemoteIP: n.RemoteIP, FirstSeen: n.FirstSeen,
		LastSeen: n.LastSeen, UptimeSec: n.UptimeSec, SSID: n.SSID, Blocked: n.Blocked, Note: n.Note,
		PendingOTA: n.PendingOTA, OTATarget: n.OTATarget, OTAStatus: n.OTAStatus, OTAAt: n.OTAAt,
		lastSeenT: n.lastSeenT,
		Idle:      atomic.LoadInt64(&n.Idle), Mux: atomic.LoadInt64(&n.Mux), Active: atomic.LoadInt64(&n.Active),
		DialsOK: atomic.LoadInt64(&n.DialsOK), DialsErr: atomic.LoadInt64(&n.DialsErr),
		BytesUp: atomic.LoadInt64(&n.BytesUp), BytesDown: atomic.LoadInt64(&n.BytesDown)}
}

type nodeView struct {
	*node
	Online bool `json:"online"`
}

type registry struct {
	mu    sync.Mutex
	nodes map[string]*node
	file  string
}

func newRegistry(file string) *registry {
	r := &registry{nodes: map[string]*node{}, file: file}
	r.load()
	return r
}

// touch records a HELLO from mac and returns its node record.
func (r *registry) touch(mac, fw, ip string, uptime uint64, ssid string) *node {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now().UTC()
	n, ok := r.nodes[mac]
	if !ok {
		n = &node{MAC: mac, FirstSeen: now.Format(time.RFC3339)}
		r.nodes[mac] = n
		r.saveLocked()
	}
	n.lastSeenT = now
	n.LastSeen = now.Format(time.RFC3339)
	if fw != "" {
		n.Firmware = fw
	}
	if ip != "" {
		n.RemoteIP = ip
	}
	rebooted := uptime < n.UptimeSec
	n.UptimeSec = uptime
	if ssid != "" {
		n.SSID = ssid
	}
	if n.OTAStatus == "sent" {
		done := n.Firmware != n.otaFromFW
		if n.OTATarget != "" {
			done = n.Firmware == n.OTATarget
		}
		switch {
		case done:
			n.OTAStatus, n.OTAAt = "ok", n.LastSeen
			r.saveLocked()
		case rebooted || uptime < n.otaUptime:
			n.OTAStatus, n.OTAAt = "failed", n.LastSeen
			r.saveLocked()
		}
	}
	return n
}

func (r *registry) list() []nodeView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]nodeView, 0, len(r.nodes))
	for _, n := range r.nodes {
		out = append(out, nodeView{node: n.snapshot(), Online: n.online()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MAC < out[j].MAC })
	return out
}

func (r *registry) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.nodes)
}

func (r *registry) onlineCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := 0
	for _, n := range r.nodes {
		if n.online() {
			c++
		}
	}
	return c
}

func (r *registry) setBlocked(mac string, blocked bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[mac]
	if !ok {
		return false
	}
	n.Blocked = blocked
	n.blk.Store(blocked)
	r.saveLocked()
	return true
}

// setOTA queues a firmware URL for the node; cleared once delivered. target is
// the version the node should report afterwards ("" = any change counts). An
// empty url cancels a queued update.
func (r *registry) setOTA(mac, url, target string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[mac]
	if !ok {
		return false
	}
	n.PendingOTA = url
	if url == "" {
		if n.OTAStatus == "queued" {
			n.OTATarget, n.OTAStatus, n.OTAAt = "", "", ""
		}
	} else {
		n.OTATarget, n.OTAStatus, n.OTAAt = target, "queued", time.Now().UTC().Format(time.RFC3339)
	}
	r.saveLocked()
	return true
}

// takeOTA returns and clears a pending OTA URL for the node.
func (r *registry) takeOTA(n *node) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := n.PendingOTA
	n.PendingOTA = ""
	if u != "" {
		n.OTAStatus, n.OTAAt = "sent", time.Now().UTC().Format(time.RFC3339)
		n.otaFromFW, n.otaUptime = n.Firmware, n.UptimeSec
		r.saveLocked()
	}
	return u
}

func (r *registry) forget(mac string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nodes[mac]; !ok {
		return false
	}
	delete(r.nodes, mac)
	r.saveLocked()
	return true
}

// Persist only identity + policy (not live counters).
type persistedNode struct {
	MAC       string `json:"mac"`
	Firmware  string `json:"firmware,omitempty"`
	FirstSeen string `json:"first_seen"`
	LastSeen  string `json:"last_seen"`
	UptimeSec uint64 `json:"uptime_sec"`
	SSID      string `json:"ssid,omitempty"`
	Blocked   bool   `json:"blocked"`
	Note      string `json:"note,omitempty"`
	OTATarget string `json:"ota_target,omitempty"`
	OTAStatus string `json:"ota_status,omitempty"`
	OTAAt     string `json:"ota_at,omitempty"`
	OTAFromFW string `json:"ota_from_fw,omitempty"`
}

func (r *registry) saveLocked() {
	if r.file == "" {
		return
	}
	list := make([]persistedNode, 0, len(r.nodes))
	for _, n := range r.nodes {
		list = append(list, persistedNode{n.MAC, n.Firmware, n.FirstSeen, n.LastSeen, n.UptimeSec, n.SSID, n.Blocked, n.Note,
			n.OTATarget, n.OTAStatus, n.OTAAt, n.otaFromFW})
	}
	data, _ := json.MarshalIndent(list, "", "  ")
	os.MkdirAll(filepath.Dir(r.file), 0o755)
	tmp := r.file + ".tmp"
	if os.WriteFile(tmp, data, 0o600) == nil {
		os.Rename(tmp, r.file)
	}
}

func (r *registry) load() {
	if r.file == "" {
		return
	}
	data, err := os.ReadFile(r.file)
	if err != nil {
		return
	}
	var list []persistedNode
	if json.Unmarshal(data, &list) != nil {
		return
	}
	for _, p := range list {
		r.nodes[p.MAC] = &node{MAC: p.MAC, Firmware: p.Firmware, FirstSeen: p.FirstSeen,
			LastSeen: p.LastSeen, UptimeSec: p.UptimeSec, SSID: p.SSID, Blocked: p.Blocked, Note: p.Note,
			OTATarget: p.OTATarget, OTAStatus: p.OTAStatus, OTAAt: p.OTAAt, otaFromFW: p.OTAFromFW}
		r.nodes[p.MAC].blk.Store(p.Blocked)
	}
}
