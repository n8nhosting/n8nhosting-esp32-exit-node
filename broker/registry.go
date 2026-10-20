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

	// live counters (atomic)
	Idle      int64 `json:"idle_conns"`   // parked connections right now
	Active    int64 `json:"active_dials"` // relays in progress
	DialsOK   int64 `json:"dials_ok"`
	DialsErr  int64 `json:"dials_err"`
	BytesUp   int64 `json:"bytes_up"`
	BytesDown int64 `json:"bytes_down"`
	lastSeenT time.Time
}

func (n *node) addIdle(d int64)   { atomic.AddInt64(&n.Idle, d) }
func (n *node) addActive(d int64) { atomic.AddInt64(&n.Active, d) }
func (n *node) addStats(ok, errs, up, down int64) {
	atomic.AddInt64(&n.DialsOK, ok)
	atomic.AddInt64(&n.DialsErr, errs)
	atomic.AddInt64(&n.BytesUp, up)
	atomic.AddInt64(&n.BytesDown, down)
}

// Online means it has parked/active connections or was seen very recently.
func (n *node) online() bool {
	return atomic.LoadInt64(&n.Idle) > 0 || atomic.LoadInt64(&n.Active) > 0 ||
		time.Since(n.lastSeenT) < 45*time.Second
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
	n.UptimeSec = uptime
	if ssid != "" {
		n.SSID = ssid
	}
	return n
}

func (r *registry) list() []nodeView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]nodeView, 0, len(r.nodes))
	for _, n := range r.nodes {
		out = append(out, nodeView{node: n, Online: n.online()})
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
	r.saveLocked()
	return true
}

// setOTA queues a firmware URL for the node; cleared once delivered.
func (r *registry) setOTA(mac, url string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[mac]
	if !ok {
		return false
	}
	n.PendingOTA = url
	return true
}

// takeOTA returns and clears a pending OTA URL for the node.
func (r *registry) takeOTA(n *node) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	u := n.PendingOTA
	n.PendingOTA = ""
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
}

func (r *registry) saveLocked() {
	if r.file == "" {
		return
	}
	list := make([]persistedNode, 0, len(r.nodes))
	for _, n := range r.nodes {
		list = append(list, persistedNode{n.MAC, n.Firmware, n.FirstSeen, n.LastSeen, n.UptimeSec, n.SSID, n.Blocked, n.Note})
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
			LastSeen: p.LastSeen, UptimeSec: p.UptimeSec, SSID: p.SSID, Blocked: p.Blocked, Note: p.Note}
	}
}
