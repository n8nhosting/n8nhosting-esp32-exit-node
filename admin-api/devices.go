package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
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
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("broker: %s", resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

// nodeStats mirrors the broker's latest STATS sample from a board (firmware
// 2.4.23+, every 30 s); absent for older firmware.
type nodeStats struct {
	At         string `json:"at"`
	UptimeSec  uint64 `json:"uptime_sec"`
	HeapFree   uint32 `json:"heap_free"` // internal heap, bytes
	HeapMin    uint32 `json:"heap_min"`
	HeapTotal  uint32 `json:"heap_total"`
	PSRAMFree  uint32 `json:"psram_free,omitempty"`
	PSRAMTotal uint32 `json:"psram_total,omitempty"`
	CPUPct     int    `json:"cpu_pct"`        // -1 = unknown
	RSSI       *int   `json:"rssi,omitempty"` // dBm; absent on older firmware
	AppBytes   uint32 `json:"app_bytes"`
	SlotBytes  uint32 `json:"slot_bytes"`
	FlashBytes uint32 `json:"flash_bytes"`
	Streams    int    `json:"streams"`
}

// brokerNode mirrors the broker's /nodes JSON.
type brokerNode struct {
	MuxLinks           int64      `json:"mux_links"`
	MaxStreams         int        `json:"max_streams"`
	LinkSince          string     `json:"link_since"`
	DialsPinnedOffline int64      `json:"dials_pinned_offline"`
	DialsOverflow      int64      `json:"dials_overflow"` // this slot's dials a same-IP sibling carried
	MAC                string     `json:"mac"`
	Online             bool       `json:"online"`
	Blocked            bool       `json:"blocked"`
	RemoteIP           string     `json:"remote_ip"`
	Firmware           string     `json:"firmware"`
	SSID               string     `json:"ssid"`
	Chip               string     `json:"chip"`       // esp32s3 / esp32c3 from HELLO; "" for firmware before 2.4.22 (all S3)
	UptimeSec          uint64     `json:"uptime_sec"` // running (the broker carries it between reports)
	FirstSeen          string     `json:"first_seen"`
	LastSeen           string     `json:"last_seen"`
	IdleConns          int64      `json:"idle_conns"`
	Active             int64      `json:"active_dials"`
	DialsOK            int64      `json:"dials_ok"`
	DialsErr           int64      `json:"dials_err"`
	BytesUp            int64      `json:"bytes_up"`
	BytesDown          int64      `json:"bytes_down"`
	PendingOTA         string     `json:"pending_ota,omitempty"`
	OTATarget          string     `json:"ota_target,omitempty"`
	OTAStatus          string     `json:"ota_status,omitempty"`
	OTAAt              string     `json:"ota_at,omitempty"`
	OTAError           string     `json:"ota_error,omitempty"`
	Stats              *nodeStats `json:"stats,omitempty"` // board health; nil before firmware 2.4.23
	// routing the broker holds for the board; a device view's own "slot" (the
	// record's) shadows Slot in JSON
	Slot int   `json:"slot"`
	Pool *bool `json:"pool"` // nil from a broker without the pool flag
}

type deviceView struct {
	brokerNode
	Status     string       `json:"status"`
	Tags       []string     `json:"tags,omitempty"`
	NextExpiry string       `json:"next_expiry"`
	Health     deviceHealth `json:"health"`
	Geo        geoInfo      `json:"geo"`
	Users      []string     `json:"users"` // usernames bound to this device

	// operator record (devices table); zero-valued when the MAC has no row
	Name                  string   `json:"name"`
	Kind                  string   `json:"kind"`
	Notes                 string   `json:"notes"`
	Slot                  int      `json:"slot"`
	SlotAuto              bool     `json:"slot_auto"`
	Port                  int      `json:"port"` // 3130+slot, 0 when unassigned
	CapacityMbps          float64  `json:"capacity_mbps"`
	CapacityMeasuredAt    string   `json:"capacity_measured_at"`
	CapacityActiveStreams int      `json:"capacity_active_streams"`
	CapacitySource        string   `json:"capacity_source"` // unmeasured, manual, or measured
	Lat                   *float64 `json:"lat"`
	Lon                   *float64 `json:"lon"`
	WifiPasswordSet       bool     `json:"wifi_password_set"` // the password itself is never listed
	MapsURL               string   `json:"maps_url"`          // the pasted Google Maps link, "" = none

	// the broker routes this board differently from the record (slot, or pool
	// flag when the broker reports one); saving the slot again re-pushes it
	SlotMismatch bool `json:"slot_mismatch"`

	// Public IP grouping is informational; seat limits belong to each MAC.
	IPGroup       string `json:"ip_group"`
	IPGroupSize   int    `json:"ip_group_size"`
	MaxUsers      int    `json:"max_users"`
	ActiveUsers   int    `json:"active_users"` // seats taken on this MAC, independent of public IP
	ReservedSeats int    `json:"reserved_seats"`
	Seats         int    `json:"-"` // seats taken on this board alone (admission)
}

// maxUsers is how many users a device may take: one for a dedicated board,
// else one per 5 Mbps of measured capacity up to 3 (0 while unmeasured).
func maxUsers(kind string, capacityMbps float64) int {
	if kind == "dedicated" {
		return 1
	}
	n := int(capacityMbps / 5)
	if capacityMbps <= 0 {
		return 0
	}
	if n < 1 {
		n = 1
	}
	if n > 3 {
		n = 3
	}
	return n
}

// slotPort is the gost port serving slot (3130+slot); 0 for no slot.
func slotPort(slot int) int {
	if slot <= 0 {
		return 0
	}
	return 3130 + slot
}

// slotCount is how many slot ports gost serves: slots 1..SLOT_COUNT.
func slotCount() int { return atoiDefault(envOr("SLOT_COUNT", "30"), 30) }

// Display-only defaults keep operator names and existing records untouched.
func boardDisplayName(mac, name string) string {
	if strings.TrimSpace(name) != "" {
		return name
	}
	hex := strings.ToUpper(strings.NewReplacer(":", "", "-", "", ".", "").Replace(mac))
	if len(hex) < 4 {
		return "Board"
	}
	return "Board-" + hex[len(hex)-4:]
}

func capacitySource(mbps float64, measuredAt string) string {
	if mbps <= 0 {
		return "unmeasured"
	}
	if measuredAt == "" {
		return "manual"
	}
	return "measured"
}

// buildDeviceViews merges broker nodes with device records, users and IP groups.
func (s *server) buildDeviceViews(nodes []brokerNode) []deviceView {
	return s.buildDeviceViewsWithHealth(nodes, true)
}

func (s *server) buildDeviceViewsWithHealth(nodes []brokerNode, rich bool) []deviceView {
	recs := map[string]*Device{}
	for _, d := range s.devices.List() {
		recs[d.MAC] = d
	}
	// nodes sharing a remote_ip form a group
	group := map[string][]string{}
	for _, n := range nodes {
		if n.RemoteIP != "" {
			group[n.RemoteIP] = append(group[n.RemoteIP], n.MAC)
		}
	}
	// users bound per MAC, and the seats they take (takesSeat) per MAC; a
	// measurement user is neither listed nor counted
	byMAC := map[string][]string{}
	activeByMAC := map[string]int{}
	nextExpiry := map[string]string{}
	now := time.Now()
	reservations := s.reservedCommerceSeats(now)
	for _, u := range s.store.List() {
		if u.DeviceMAC == "" || isReservedUsername(u.Username) {
			continue
		}
		byMAC[u.DeviceMAC] = append(byMAC[u.DeviceMAC], u.Username)
		if takesSeat(u, now) {
			activeByMAC[u.DeviceMAC]++
			if exp, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil {
				prev, _ := time.Parse(time.RFC3339, nextExpiry[u.DeviceMAC])
				if prev.IsZero() || exp.Before(prev) {
					nextExpiry[u.DeviceMAC] = u.ExpiresAt
				}
			}
		}
	}
	out := make([]deviceView, 0, len(nodes))
	for _, n := range nodes {
		users := byMAC[n.MAC]
		sort.Strings(users)
		v := deviceView{brokerNode: n, Users: users, Kind: "shared", IPGroup: n.RemoteIP, SlotAuto: true}
		if rich {
			v.Geo = s.geo.lookup(n.RemoteIP)
		}
		if d := recs[n.MAC]; d != nil {
			v.Name, v.Kind, v.Notes = d.Name, d.Kind, d.Notes
			v.Slot, v.CapacityMbps = d.Slot, d.CapacityMbps
			v.SlotAuto = d.SlotAuto
			v.CapacityMeasuredAt, v.CapacityActiveStreams = d.CapacityMeasuredAt, d.CapacityActiveStreams
			v.Lat, v.Lon = d.Lat, d.Lon
			v.MapsURL = d.MapsURL
			v.WifiPasswordSet = d.WifiPasswordEnc != ""
		}
		v.Name = boardDisplayName(n.MAC, v.Name)
		v.CapacitySource = capacitySource(v.CapacityMbps, v.CapacityMeasuredAt)
		v.Port = slotPort(v.Slot)
		v.SlotMismatch = n.Slot != v.Slot || (n.Pool != nil && *n.Pool != poolFlag(v.Kind))
		v.MaxUsers = maxUsers(v.Kind, v.CapacityMbps)
		v.Seats = activeByMAC[n.MAC]
		v.ActiveUsers = v.Seats
		v.ReservedSeats = reservations[n.MAC]
		v.NextExpiry = nextExpiry[n.MAC]
		if macs := group[n.RemoteIP]; len(macs) > 0 {
			v.IPGroupSize = len(macs)
		} else {
			v.IPGroupSize = 1
		}
		v.Status, v.Tags = deviceStatus(v, now)
		if rich {
			v.Health = s.healthFor(v, now)
		}
		out = append(out, v)
	}
	return out
}

// GET /api/devices — list ESP32 exit nodes enriched with geo/ISP, operator
// records, IP groups and the users bound to each device.
func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	var nodes []brokerNode
	if err := s.brokerGET("/nodes", &nodes); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "broker unreachable"})
		return
	}
	writeJSON(w, http.StatusOK, s.buildDeviceViews(nodes))
}

// errBrokerNoNode is the broker answering that it has no node for the MAC.
var errBrokerNoNode = errors.New("broker: not found")

// poolFlag is whether a board of kind serves the shared pool: a dedicated
// board never does (spec §6.2.14).
func poolFlag(kind string) bool { return kind != "dedicated" }

// brokerSetSlot tells the broker to route slot to mac (0 clears) and whether
// the board also serves the shared pool. Clearing a MAC the broker does not
// know succeeds: the broker holds no slot for it; setting one is
// errBrokerNoNode.
func (s *server) brokerSetSlot(mac string, slot int, pool bool) error {
	body, _ := json.Marshal(struct {
		Slot int  `json:"slot"`
		Pool bool `json:"pool"`
	}{slot, pool})
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Post(s.brokerURL()+"/nodes/"+url.PathEscape(mac)+"/slot", "application/json", bytes.NewReader(body))
	if err != nil {
		return errors.New("broker unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 || (slot == 0 && resp.StatusCode == http.StatusNotFound) {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return errBrokerNoNode
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
	if m := strings.TrimSpace(string(msg)); m != "" {
		return fmt.Errorf("broker: %s", m)
	}
	return fmt.Errorf("broker: %s", resp.Status)
}

// pushSlotsToBroker re-sends every recorded slot and pool flag to the broker,
// which may have lost them (a broker volume reset, a forget, a failed
// rollback). Failures are logged and counted, never fatal.
func (s *server) pushSlotsToBroker() (failed int) {
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	for _, d := range s.devices.List() {
		if err := s.brokerSetSlot(d.MAC, d.Slot, poolFlag(d.Kind)); err != nil {
			failed++
			log.Printf("startup: slot %d (pool %v) for %s not pushed to the broker: %v", d.Slot, poolFlag(d.Kind), d.MAC, err)
		}
	}
	return failed
}

// PUT /api/devices/{mac} — create/update the operator record. Absent fields
// are left unchanged; wifi_password "" clears the stored password. slot N
// (1..SLOT_COUNT, 0 clears) is set on the broker first, with the pool flag of
// the resulting kind; a kind change alone pushes the current slot with the new
// flag (a board the broker does not know yet is skipped). If the broker
// refuses or is down, nothing in the request is saved.
func (s *server) handlePutDevice(w http.ResponseWriter, r *http.Request, mac string) {
	var in struct {
		Name         *string  `json:"name"`
		Kind         *string  `json:"kind"`
		Lat          *float64 `json:"lat"`
		Lon          *float64 `json:"lon"`
		Notes        *string  `json:"notes"`
		WifiPassword *string  `json:"wifi_password"`
		Slot         *int     `json:"slot"`
		MapsURL      *string  `json:"maps_url"`      // Google Maps share link; resolved into lat/lon here, "" clears the link
		CapacityMbps *float64 `json:"capacity_mbps"` // operator override; zero resets to unmeasured
	}
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(r.Body, 16384)).Decode(&raw); err != nil || raw == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	encoded, _ := json.Marshal(raw)
	if err := json.Unmarshal(encoded, &in); err != nil {
		writeJSON(w, 400, map[string]string{"error": "bad json"})
		return
	}
	if _, present := raw["capacity_mbps"]; present && (in.CapacityMbps == nil || math.IsNaN(*in.CapacityMbps) || math.IsInf(*in.CapacityMbps, 0) || *in.CapacityMbps < 0 || *in.CapacityMbps > 100000) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "capacity_mbps must be a number between 0 and 100000"})
		return
	}
	latNull := strings.TrimSpace(string(raw["lat"])) == "null"
	lonNull := strings.TrimSpace(string(raw["lon"])) == "null"
	clearCoords := latNull && lonNull
	if latNull != lonNull {
		writeJSON(w, 400, map[string]string{"error": "clear coordinates with lat:null and lon:null together"})
		return
	}
	if in.MapsURL != nil && strings.TrimSpace(*in.MapsURL) != "" {
		// a pasted link wins over typed coordinates; a link we cannot read
		// saves nothing, so the operator sees the error with the form intact
		lat, lon, status, msg := s.resolveMapsURL(r.Context(), *in.MapsURL)
		if status != http.StatusOK {
			writeJSON(w, status, map[string]string{"error": msg})
			return
		}
		in.Lat, in.Lon = &lat, &lon
		clearCoords = false
	}
	if in.Kind != nil && *in.Kind != "shared" && *in.Kind != "dedicated" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be shared or dedicated"})
		return
	}
	if in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lat must be between -90 and 90"})
		return
	}
	if in.Lon != nil && (*in.Lon < -180 || *in.Lon > 180) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lon must be between -180 and 180"})
		return
	}
	if n := slotCount(); in.Slot != nil && (*in.Slot < 0 || *in.Slot > n) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("slot must be between 1 and %d (0 clears)", n)})
		return
	}
	if in.CapacityMbps != nil {
		// Serialize operator overrides with live measurements, so a completed
		// download cannot silently overwrite a speed just saved in Settings.
		if !s.beginMeasure(mac) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "measurement in progress"})
			return
		}
		defer s.endMeasure(mac)
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	if in.Slot != nil && *in.Slot > 0 {
		holder, err := s.devices.BySlot(*in.Slot)
		if err == nil && holder.MAC != mac {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "slot taken"})
			return
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save device"})
			return
		}
	}
	prevSlot, prevKind := 0, "shared" // what a new record starts with
	if cur, err := s.devices.Get(mac); err == nil {
		prevSlot, prevKind = cur.Slot, cur.Kind
	} else if !errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save device"})
		return
	}
	newSlot, newKind := prevSlot, prevKind
	if in.Slot != nil {
		newSlot = *in.Slot
	}
	if in.Kind != nil {
		newKind = *in.Kind
	}
	pushed := false
	if in.Slot != nil || newKind != prevKind {
		err := s.brokerSetSlot(mac, newSlot, poolFlag(newKind))
		switch {
		case err == nil:
			pushed = true
		case in.Slot == nil && errors.Is(err, errBrokerNoNode):
			// a kind change for a board the broker has not seen: nothing routes to it yet
		default:
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
	}
	err := s.devices.Upsert(mac, func(d *Device) error {
		if in.Name != nil {
			d.Name = *in.Name
		}
		if in.Kind != nil {
			d.Kind = *in.Kind
		}
		if in.Lat != nil {
			d.Lat = in.Lat
		}
		if in.Lon != nil {
			d.Lon = in.Lon
		}
		if clearCoords {
			d.Lat, d.Lon = nil, nil
		}
		if in.Notes != nil {
			d.Notes = *in.Notes
		}
		if in.MapsURL != nil {
			d.MapsURL = strings.TrimSpace(*in.MapsURL)
		}
		if in.WifiPassword != nil {
			if *in.WifiPassword == "" {
				d.WifiPasswordEnc = ""
			} else {
				d.WifiPasswordEnc = encryptCred(*in.WifiPassword)
			}
		}
		if in.Slot != nil {
			d.Slot = *in.Slot
			d.SlotAuto = *in.Slot != 0
		}
		if in.CapacityMbps != nil {
			d.CapacityMbps = *in.CapacityMbps
			// Manual values are not automated measurements. Empty metadata also
			// makes zero a complete reset, without retaining a misleading date.
			d.CapacityMeasuredAt, d.CapacityActiveStreams = "", 0
		}
		return nil
	})
	if err != nil {
		if pushed && (newSlot != prevSlot || newKind != prevKind) {
			// keep the broker on the slot and pool flag the record still holds
			if rerr := s.brokerSetSlot(mac, prevSlot, poolFlag(prevKind)); rerr != nil {
				log.Printf("device %s: slot %d/kind %s not saved (%v), and the broker still has them: %v", mac, newSlot, newKind, err, rerr)
			}
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save device"})
		return
	}
	// the broker may not know this MAC (or be down): still answer with the record
	changed := []string{}
	for _, field := range []string{"name", "kind", "slot", "lat", "lon", "notes", "maps_url", "wifi_password", "capacity_mbps"} {
		if _, ok := raw[field]; ok {
			changed = append(changed, field)
		}
	}
	eventData := map[string]any{"fields": changed}
	if in.CapacityMbps != nil {
		eventData["source"], eventData["capacity_mbps"] = "manual", *in.CapacityMbps
	}
	s.emit("device.updated", "info", "device", strings.ToLower(mac), "admin", "Device settings updated", eventData)
	var nodes []brokerNode
	_ = s.brokerGET("/nodes", &nodes)
	known := false
	for _, n := range nodes {
		known = known || n.MAC == mac
	}
	if !known {
		nodes = append(nodes, brokerNode{MAC: mac})
	}
	for _, v := range s.buildDeviceViews(nodes) {
		if v.MAC == mac {
			writeJSON(w, http.StatusOK, v)
			return
		}
	}
}

// POST /api/devices/{mac}/reveal-wifi — the stored WiFi password, on demand.
func (s *server) handleRevealWifi(w http.ResponseWriter, r *http.Request, mac string) {
	d, err := s.devices.Get(mac)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	log.Printf("audit reveal-wifi mac=%s", mac)
	s.emit("device.wifi_revealed", "warn", "device", strings.ToLower(mac), "admin", "WiFi password revealed", nil)
	writeJSON(w, http.StatusOK, map[string]string{"wifi_password": decryptCred(d.WifiPasswordEnc)})
}

// PUT /api/devices/{mac}, POST /api/devices/{mac}/slot|reveal-wifi|measure (local); any
// other POST /api/devices/{mac}/block|allow|forget|ota|ota-cancel is proxied to the broker.
func (s *server) handleDeviceAction(w http.ResponseWriter, r *http.Request) {
	rest := r.URL.Path[len("/api/devices/"):]
	if r.Method == http.MethodPut && rest != "" && !strings.Contains(rest, "/") {
		s.handlePutDevice(w, r, rest)
		return
	}
	// POST …/slot {"slot": N} is the PUT's slot field, never a bare broker
	// passthrough, so the devices table and the broker cannot disagree
	if r.Method == http.MethodPost && strings.HasSuffix(rest, "/slot") {
		if mac := strings.TrimSuffix(rest, "/slot"); mac != "" && !strings.Contains(mac, "/") {
			s.handlePutDevice(w, r, mac)
			return
		}
	}
	if r.Method == http.MethodPost && strings.HasSuffix(rest, "/reveal-wifi") {
		if mac := strings.TrimSuffix(rest, "/reveal-wifi"); mac != "" && !strings.Contains(mac, "/") {
			s.handleRevealWifi(w, r, mac)
			return
		}
	}
	if r.Method == http.MethodPost && strings.HasSuffix(rest, "/measure") {
		if mac := strings.TrimSuffix(rest, "/measure"); mac != "" && !strings.Contains(mac, "/") {
			s.handleMeasure(w, r, mac)
			return
		}
	}
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	cl := &http.Client{Timeout: 5 * time.Second}
	resp, err := cl.Post(s.brokerURL()+"/nodes/"+rest, "application/json", r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "broker unreachable"})
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		parts := strings.Split(rest, "/")
		if len(parts) == 2 {
			kind := map[string]string{"block": "node.blocked", "allow": "node.allowed", "forget": "node.forgotten", "ota": "node.ota_queued", "ota-cancel": "node.ota_cancelled"}[parts[1]]
			if kind != "" {
				severity := "info"
				if parts[1] == "block" || parts[1] == "forget" {
					severity = "warn"
				}
				s.emit(kind, severity, "node", strings.ToLower(parts[0]), "admin", "Device action: "+parts[1], nil)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GET /api/overview — one call for the dashboard header cards.
func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	users := []*User{}
	active, over, expired, expiring, occupied := 0, 0, 0, 0, 0
	statusCounts := map[string]int{"active": 0, "disabled": 0, "expired": 0, "over_quota": 0}
	var monthBytes int64
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(bytes),0) FROM usage_monthly WHERE month=?`, monthKey(now)).Scan(&monthBytes); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "monthly traffic unavailable"})
		return
	}
	for _, original := range s.store.List() {
		if isReservedUsername(original.Username) {
			continue
		}
		u := effectiveUser(original, now)
		users = append(users, u)
		statusCounts[userStatus(u, now)]++
		if u.Enabled {
			active++
		}
		if at, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil {
			if now.After(at) {
				expired++
			} else if u.Enabled && at.Sub(now) <= 7*24*time.Hour {
				expiring++
			}
		}
		if u.QuotaGB > 0 && u.UsageBytes >= int64(u.QuotaGB*1024*1024*1024) {
			over++
		}
		if u.DeviceMAC != "" && takesSeat(u, now) {
			occupied++
		}
	}
	t := &s.telemetry
	t.mu.Lock()
	broker := map[string]any{}
	if t.previous != nil {
		encoded, _ := json.Marshal(t.previous)
		_ = json.Unmarshal(encoded, &broker)
	}
	nodes := append([]brokerNode(nil), t.nodes...)
	asOf := t.asOf
	broker["throughput_bps"] = map[string]float64{"up": t.rateUp, "down": t.rateDown}
	t.mu.Unlock()
	if asOf.IsZero() {
		// Startup and isolated handler tests still show live broker state. Rates
		// remain zero until the shared sampler has two observations.
		_ = s.brokerGET("/status", &broker)
		broker["throughput_bps"] = map[string]float64{"up": 0, "down": 0}
		_ = s.brokerGET("/nodes", &nodes)
	}
	views := s.buildDeviceViewsWithHealth(nodes, false)
	available := availableSeats(views)
	writeJSON(w, http.StatusOK, map[string]any{
		"users":    map[string]any{"total": len(users), "active": active, "over_quota": over, "expired": expired, "status_counts": statusCounts, "connected_now": s.connectedNow(now), "expiring_7d": expiring},
		"seats":    map[string]int{"occupied": occupied, "available": available},
		"alerts":   map[string]any{"counts": s.overviewAlertCounts()},
		"month_gb": float64(monthBytes) / (1024 * 1024 * 1024), "broker": broker,
		"as_of": asOf.UTC().Format(time.RFC3339), "stale": asOf.IsZero() || now.Sub(asOf) > 120*time.Second,
	})
}

// Each eligible, routable board contributes its own free seats, even when
// multiple boards use the same public IP.
func availableSeats(views []deviceView) int {
	free := 0
	for _, v := range views {
		if !v.Online || v.Blocked || v.Status == "updating" || v.Slot <= 0 || v.SlotMismatch {
			continue
		}
		if v.Kind != "dedicated" && v.CapacityMbps <= 0 {
			continue
		}
		if n := v.MaxUsers - v.Seats - v.ReservedSeats; n > 0 {
			free += n
		}
	}
	return free
}
