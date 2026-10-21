package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// timeNow is the registry's clock; tests set it to move time.
var timeNow = time.Now

// nodeStats is the latest health sample a node sent in a STATS frame
// (firmware 2.4.23+, every 30 s). Live only: never persisted.
type nodeStats struct {
	At         string `json:"at"`
	UptimeSec  uint64 `json:"uptime_sec"`
	HeapFree   uint32 `json:"heap_free"` // internal heap (PSRAM would hide internal-RAM starvation)
	HeapMin    uint32 `json:"heap_min"`  // low-water mark since boot
	HeapTotal  uint32 `json:"heap_total"`
	PSRAMFree  uint32 `json:"psram_free,omitempty"`
	PSRAMTotal uint32 `json:"psram_total,omitempty"`
	CPUPct     int    `json:"cpu_pct"`        // load since the previous sample, all cores; -1 = unknown
	RSSI       *int   `json:"rssi,omitempty"` // STA signal in dBm; nil when missing or invalid
	AppBytes   uint32 `json:"app_bytes"`
	SlotBytes  uint32 `json:"slot_bytes"`
	FlashBytes uint32 `json:"flash_bytes"`
	Streams    int    `json:"streams"`
}

// node is one ESP32 exit device, identified by its WiFi STA MAC address.
type node struct {
	MAC        string `json:"mac"`
	Firmware   string `json:"firmware,omitempty"`
	RemoteIP   string `json:"remote_ip,omitempty"` // public IP the node dials from (= exit IP)
	FirstSeen  string `json:"first_seen"`
	LastSeen   string `json:"last_seen"`
	UptimeSec  uint64 `json:"uptime_sec"` // last reported; runningUptime() is what /nodes shows
	SSID       string `json:"ssid,omitempty"`
	Chip       string `json:"chip,omitempty"` // esp32s3 / esp32c3 from HELLO chip=; "" for firmware that predates it
	MaxStreams int    `json:"max_streams"`    // accepted limit of the most recent successful mux link; live only
	LinkSince  string `json:"link_since"`     // latest successful READY mux1, empty without a live mux link
	Blocked    bool   `json:"blocked"`
	// Stats is the latest STATS sample; nil until the first one (firmware
	// before 2.4.23 never sends one).
	Stats      *nodeStats `json:"stats,omitempty"`
	uptimeAt   time.Time  // when UptimeSec was reported (HELLO or STATS); zero for a node read from disk
	offAt      time.Time  // when its last link closed; zero while linked (or never linked)
	Note       string     `json:"note,omitempty"`
	PendingOTA string     `json:"pending_ota,omitempty"` // firmware URL to push on next connection
	Slot       int        `json:"slot"`                  // gost slot pinned to this board (SOCKS user "slot-N"), 0 = none
	Pool       bool       `json:"pool"`                  // also serves the shared pool; false for a dedicated board
	// Sent means written to the connection, not acknowledged or applied.
	// A failure/timeout does not prove a firmware rollback.
	OTATarget       string `json:"ota_target,omitempty"` // firmware version expected after the update
	OTAStatus       string `json:"ota_status,omitempty"`
	OTAAt           string `json:"ota_at,omitempty"`
	OTAError        string `json:"ota_error,omitempty"`
	otaFromFW       string // firmware when the OTA was delivered
	otaUptime       uint64 // uptime when the OTA was delivered
	otaRevision     uint64 // changes for each queued/replaced/cancelled request
	otaClaimID      uint64 // unique live claim token; never persisted
	otaClaimActive  uint64
	otaDispatching  bool   // durable ambiguous-delivery marker across broker restart
	muxGeneration   uint64 // newest accepted mux link owns new OTA deliveries
	helloGeneration uint64

	// live counters (atomic)
	Idle               int64 `json:"idle_conns"`   // parked connections right now (legacy one-shot links)
	Mux                int64 `json:"mux_links"`    // live multiplexed links
	Active             int64 `json:"active_dials"` // relays in progress
	DialsOK            int64 `json:"dials_ok"`
	DialsErr           int64 `json:"dials_err"`
	DialsPinnedOffline int64 `json:"dials_pinned_offline"`
	BytesUp            int64 `json:"bytes_up"`
	BytesDown          int64 `json:"bytes_down"`
	lastSeenT          time.Time
	blk                atomic.Bool // mirror of Blocked for lock-free readers
	noPool             atomic.Bool // mirror of !Pool for lock-free readers
}

func (n *node) isBlocked() bool { return n.blk.Load() }

// inPool reports whether the node may carry unpinned (shared pool) streams.
func (n *node) inPool() bool { return !n.noPool.Load() }

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

// runningUptime is the node's uptime now: the last reported value plus the
// time since, frozen once its link closed. Firmware reports uptime in HELLO
// (seconds after boot) and, from 2.4.23, in every STATS sample; between
// reports the broker's clock carries it. A node read from disk has no report
// yet and shows the stored value.
func (n *node) runningUptime(now time.Time) uint64 {
	if n.uptimeAt.IsZero() {
		return n.UptimeSec
	}
	end := now
	if !n.offAt.IsZero() && n.offAt.Before(end) {
		end = n.offAt
	}
	if end.Before(n.uptimeAt) {
		return n.UptimeSec
	}
	return n.UptimeSec + uint64(end.Sub(n.uptimeAt)/time.Second)
}

// snapshot copies the node with its live counters read atomically, so the
// JSON encoder never reads fields that relays are updating.
func (n *node) snapshot() *node {
	return &node{MAC: n.MAC, Firmware: n.Firmware, RemoteIP: n.RemoteIP, FirstSeen: n.FirstSeen,
		LastSeen: n.LastSeen, UptimeSec: n.runningUptime(timeNow()), SSID: n.SSID, Chip: n.Chip, Blocked: n.Blocked, Note: n.Note,
		Stats:      n.Stats,
		MaxStreams: n.MaxStreams, LinkSince: n.LinkSince,
		PendingOTA: n.PendingOTA, Slot: n.Slot, Pool: n.Pool,
		OTATarget: n.OTATarget, OTAStatus: n.OTAStatus, OTAAt: n.OTAAt, OTAError: n.OTAError,
		lastSeenT: n.lastSeenT,
		Idle:      atomic.LoadInt64(&n.Idle), Mux: atomic.LoadInt64(&n.Mux), Active: atomic.LoadInt64(&n.Active),
		DialsOK: atomic.LoadInt64(&n.DialsOK), DialsErr: atomic.LoadInt64(&n.DialsErr),
		DialsPinnedOffline: atomic.LoadInt64(&n.DialsPinnedOffline),
		BytesUp:            atomic.LoadInt64(&n.BytesUp), BytesDown: atomic.LoadInt64(&n.BytesDown)}
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
func (r *registry) touch(mac, fw, ip string, uptime uint64, ssid, chip string) *node {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := timeNow() // keeps Go's monotonic reading: uptime arithmetic must not follow NTP steps
	n, ok := r.nodes[mac]
	if !ok {
		n = &node{MAC: mac, FirstSeen: now.UTC().Format(time.RFC3339), Pool: true}
		r.nodes[mac] = n
		r.saveLocked()
	}
	n.lastSeenT = now
	n.helloGeneration++
	n.LastSeen = now.UTC().Format(time.RFC3339)
	n.Stats = nil // a new link reports afresh (and a board rolled back below 2.4.23 never will)
	if fw != "" {
		n.Firmware = fw
	}
	if ip != "" {
		n.RemoteIP = ip
	}
	// A reboot shows as an uptime below the one we were carrying. The link's
	// death is noticed up to muxDeadAfter late, so the carried value can
	// overshoot by that much on a quick reconnect: allow it before calling
	// a reconnect a reboot.
	rebooted := uptime+uint64(muxDeadAfter/time.Second)+15 < n.runningUptime(now)
	n.UptimeSec, n.uptimeAt, n.offAt = uptime, now, time.Time{}
	if ssid != "" {
		n.SSID = ssid
	}
	if chip != "" {
		n.Chip = chip
	}
	changed := expireOTALocked(n, now)
	if resolveOTALocked(n, now, rebooted || uptime < n.otaUptime, chip != "") || changed {
		r.saveLocked()
	}
	return n
}

func (r *registry) list() []nodeView {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]nodeView, 0, len(r.nodes))
	changed := false
	for _, n := range r.nodes {
		changed = expireOTALocked(n, timeNow()) || changed
		out = append(out, nodeView{node: n.snapshot(), Online: n.online()})
	}
	if changed {
		r.saveLocked()
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
	ok, err := r.setOTAResult(mac, url, target)
	if err != nil {
		log.Printf("ota: request for %s not persisted: %v", mac, err)
	}
	return ok && err == nil
}

// Preserve the existing request on storage failure. A caller must not receive
// a successful queue/cancel response for a mutation that cannot survive restart.
func (r *registry) setOTAResult(mac, url, target string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[mac]
	if !ok {
		return false, nil
	}
	previous := struct {
		pending, target, status, at, reason string
		revision, active                    uint64
		dispatching                         bool
	}{n.PendingOTA, n.OTATarget, n.OTAStatus, n.OTAAt, n.OTAError, n.otaRevision, n.otaClaimActive, n.otaDispatching}
	n.PendingOTA = url
	n.otaRevision++
	n.otaClaimActive = 0
	n.otaDispatching = false
	n.OTAError = ""
	if url == "" {
		if n.OTAStatus == "queued" {
			n.OTATarget, n.OTAStatus, n.OTAAt = "", "", ""
		}
	} else {
		n.OTATarget, n.OTAStatus, n.OTAAt = target, "queued", timeNow().UTC().Format(time.RFC3339)
	}
	if err := r.saveLocked(); err != nil {
		n.PendingOTA, n.OTATarget, n.OTAStatus, n.OTAAt, n.OTAError = previous.pending, previous.target, previous.status, previous.at, previous.reason
		n.otaRevision, n.otaClaimActive, n.otaDispatching = previous.revision, previous.active, previous.dispatching
		return false, err
	}
	return true, nil
}

// chipOf is the chip a node reported in HELLO. Firmware before 2.4.22 never
// said, and every such board is an ESP32-S3, so "" reads as esp32s3.
func (r *registry) chipOf(mac string) (chip string, ok bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[mac]
	if !ok {
		return "", false
	}
	if n.Chip == "" {
		return "esp32s3", true
	}
	return n.Chip, true
}

// stats stores a STATS sample; the uptime it carries restarts the running
// clock (and proves the link is up).
func (r *registry) stats(n *node, st nodeStats) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := timeNow()
	st.At = now.UTC().Format(time.RFC3339)
	if st.UptimeSec > 0 {
		n.UptimeSec, n.uptimeAt, n.offAt = st.UptimeSec, now, time.Time{}
	}
	n.Stats = &st
}

// muxUp publishes only successfully accepted mux links. The count and metadata
// share the registry lock with linkDown, so closing an older link cannot clear
// the new link's timestamp or freeze its uptime.
func (r *registry) muxUp(n *node, maxStreams int) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	n.MaxStreams = maxStreams
	n.LinkSince = timeNow().UTC().Format(time.RFC3339)
	n.offAt = time.Time{}
	n.addMux(1)
	n.muxGeneration++
	return n.muxGeneration
}

// linkDown records that one of the node's links closed. With no link left
// its uptime stops running and last_seen becomes now (until here it was the
// HELLO time, so a board that ran for days was "last seen" days ago). A
// board that reconnected quickly still has its new link (the old TCP
// connection is only found dead later): nothing changes then.
func (r *registry) linkDown(n *node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if atomic.LoadInt64(&n.Mux) > 0 {
		return
	}
	now := timeNow()
	n.LinkSince = ""
	n.offAt = now
	n.LastSeen = now.UTC().Format(time.RFC3339)
	r.saveLocked()
}

var (
	errNoNode    = errors.New("no such node")
	errSlotTaken = errors.New("slot taken")
)

// setSlot pins slot N (> 0) to the node, moving it off any slot it held, or
// clears its slot (0). A slot another node holds is refused (errSlotTaken).
func (r *registry) setSlot(mac string, slot int) error { return r.setSlotPool(mac, slot, nil) }

// setSlotPool is setSlot that also sets whether the node serves the shared
// pool, unless pool is nil. A refused slot changes neither.
func (r *registry) setSlotPool(mac string, slot int, pool *bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.nodes[mac]
	if !ok {
		return errNoNode
	}
	if o := r.bySlotLocked(slot); o != nil && o != n {
		return errSlotTaken
	}
	n.Slot = slot
	if pool != nil {
		n.Pool = *pool
		n.noPool.Store(!*pool)
	}
	r.saveLocked()
	return nil
}

// bySlot returns the node holding slot N, or nil.
func (r *registry) bySlot(slot int) *node {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bySlotLocked(slot)
}

func (r *registry) bySlotLocked(slot int) *node {
	if slot <= 0 {
		return nil
	}
	for _, n := range r.nodes {
		if n.Slot == slot {
			return n
		}
	}
	return nil
}

// slotCount is the number of nodes holding a slot.
func (r *registry) slotCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := 0
	for _, n := range r.nodes {
		if n.Slot > 0 {
			c++
		}
	}
	return c
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
	MAC            string `json:"mac"`
	Firmware       string `json:"firmware,omitempty"`
	FirstSeen      string `json:"first_seen"`
	LastSeen       string `json:"last_seen"`
	UptimeSec      uint64 `json:"uptime_sec"`
	SSID           string `json:"ssid,omitempty"`
	Chip           string `json:"chip,omitempty"` // esp32s3 / esp32c3; "" before 2.4.22 firmware
	Blocked        bool   `json:"blocked"`
	Note           string `json:"note,omitempty"`
	Slot           int    `json:"slot,omitempty"`
	Pool           *bool  `json:"pool,omitempty"` // nil (stored before the flag existed) = true
	OTATarget      string `json:"ota_target,omitempty"`
	OTAStatus      string `json:"ota_status,omitempty"`
	OTAAt          string `json:"ota_at,omitempty"`
	OTAFromFW      string `json:"ota_from_fw,omitempty"`
	PendingOTA     string `json:"pending_ota,omitempty"`
	OTAUptime      uint64 `json:"ota_uptime,omitempty"`
	OTARevision    uint64 `json:"ota_revision,omitempty"`
	OTAError       string `json:"ota_error,omitempty"`
	OTADispatching bool   `json:"ota_dispatching,omitempty"`
}

func (r *registry) saveLocked() error {
	if r.file == "" {
		return nil
	}
	list := make([]persistedNode, 0, len(r.nodes))
	for _, n := range r.nodes {
		pool := n.Pool
		list = append(list, persistedNode{MAC: n.MAC, Firmware: n.Firmware, FirstSeen: n.FirstSeen, LastSeen: n.LastSeen,
			UptimeSec: n.UptimeSec, SSID: n.SSID, Chip: n.Chip, Blocked: n.Blocked, Note: n.Note, Slot: n.Slot, Pool: &pool,
			OTATarget: n.OTATarget, OTAStatus: n.OTAStatus, OTAAt: n.OTAAt, OTAFromFW: n.otaFromFW,
			PendingOTA: n.PendingOTA, OTAUptime: n.otaUptime, OTARevision: n.otaRevision, OTAError: n.OTAError, OTADispatching: n.otaDispatching})
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encode node records: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(r.file), 0o755); err != nil {
		return fmt.Errorf("create node directory: %w", err)
	}
	tmp := r.file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write node records: %w", err)
	}
	if err := os.Rename(tmp, r.file); err != nil {
		return fmt.Errorf("replace node records: %w", err)
	}
	return nil
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
	changed := false
	for _, p := range list {
		if p.Slot > 0 && r.bySlotLocked(p.Slot) != nil {
			log.Printf("nodes: slot %d listed twice, cleared on %s", p.Slot, p.MAC)
			p.Slot = 0 // first holder wins, so routing never depends on map order
		}
		pool := p.Pool == nil || *p.Pool
		r.nodes[p.MAC] = &node{MAC: p.MAC, Firmware: p.Firmware, FirstSeen: p.FirstSeen,
			LastSeen: p.LastSeen, UptimeSec: p.UptimeSec, SSID: p.SSID, Chip: p.Chip, Blocked: p.Blocked, Note: p.Note, Slot: p.Slot, Pool: pool,
			OTATarget: p.OTATarget, OTAStatus: p.OTAStatus, OTAAt: p.OTAAt, otaFromFW: p.OTAFromFW,
			PendingOTA: p.PendingOTA, otaUptime: p.OTAUptime, otaRevision: p.OTARevision, OTAError: p.OTAError}
		n := r.nodes[p.MAC]
		if p.OTADispatching && n.OTAStatus == "queued" {
			// The process died between claiming and recording the write result.
			// The board may have received the command; never blindly send twice.
			n.PendingOTA, n.OTAStatus, n.OTAError = "", "sent", "delivery_unconfirmed"
			changed = true
		}
		if n.OTAStatus == "queued" && n.PendingOTA == "" {
			// Older broker versions persisted the status without its URL.
			n.OTAStatus, n.OTAError = "failed", "request_missing"
			changed = true
		}
		changed = expireOTALocked(n, timeNow()) || changed
		r.nodes[p.MAC].blk.Store(p.Blocked)
		r.nodes[p.MAC].noPool.Store(!pool)
	}
	if changed {
		r.saveLocked()
	}
}
