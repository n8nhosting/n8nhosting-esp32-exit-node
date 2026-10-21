package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
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
	Chip       string `json:"chip"` // esp32s3 / esp32c3 from HELLO; "" for firmware before 2.4.22 (all S3)
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
	// routing the broker holds for the board; a device view's own "slot" (the
	// record's) shadows Slot in JSON
	Slot int   `json:"slot"`
	Pool *bool `json:"pool"` // nil from a broker without the pool flag
}

type deviceView struct {
	brokerNode
	Geo   geoInfo  `json:"geo"`
	Users []string `json:"users"` // usernames bound to this device

	// operator record (devices table); zero-valued when the MAC has no row
	Name                  string   `json:"name"`
	Kind                  string   `json:"kind"`
	Notes                 string   `json:"notes"`
	Slot                  int      `json:"slot"`
	Port                  int      `json:"port"` // 3130+slot, 0 when unassigned
	CapacityMbps          float64  `json:"capacity_mbps"`
	CapacityMeasuredAt    string   `json:"capacity_measured_at"`
	CapacityActiveStreams int      `json:"capacity_active_streams"`
	Lat                   *float64 `json:"lat"`
	Lon                   *float64 `json:"lon"`
	WifiPasswordSet       bool     `json:"wifi_password_set"` // the password itself is never listed
	MapsURL               string   `json:"maps_url"`          // the pasted Google Maps link, "" = none

	// the broker routes this board differently from the record (slot, or pool
	// flag when the broker reports one); saving the slot again re-pushes it
	SlotMismatch bool `json:"slot_mismatch"`

	// devices behind one public IP share a group (one household line)
	IPGroup     string `json:"ip_group"`
	IPGroupSize int    `json:"ip_group_size"`
	MaxUsers    int    `json:"max_users"`
	ActiveUsers int    `json:"active_users"`
	Seats       int    `json:"-"` // seats taken on this board alone (admission)
}

// maxUsers is how many users a device may take: one for a dedicated board,
// else one per 5 Mbps of measured capacity up to 3 (0 while unmeasured).
func maxUsers(kind string, capacityMbps float64) int {
	if kind == "dedicated" {
		return 1
	}
	n := int(capacityMbps / 5)
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

// buildDeviceViews merges broker nodes with device records, users and IP groups.
func (s *server) buildDeviceViews(nodes []brokerNode) []deviceView {
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
	now := time.Now()
	for _, u := range s.store.List() {
		if u.DeviceMAC == "" || isReservedUsername(u.Username) {
			continue
		}
		byMAC[u.DeviceMAC] = append(byMAC[u.DeviceMAC], u.Username)
		if takesSeat(u, now) {
			activeByMAC[u.DeviceMAC]++
		}
	}
	out := make([]deviceView, 0, len(nodes))
	for _, n := range nodes {
		users := byMAC[n.MAC]
		sort.Strings(users)
		v := deviceView{brokerNode: n, Geo: s.geo.lookup(n.RemoteIP), Users: users, Kind: "shared", IPGroup: n.RemoteIP}
		if d := recs[n.MAC]; d != nil {
			v.Name, v.Kind, v.Notes = d.Name, d.Kind, d.Notes
			v.Slot, v.CapacityMbps = d.Slot, d.CapacityMbps
			v.CapacityMeasuredAt, v.CapacityActiveStreams = d.CapacityMeasuredAt, d.CapacityActiveStreams
			v.Lat, v.Lon = d.Lat, d.Lon
			v.MapsURL = d.MapsURL
			v.WifiPasswordSet = d.WifiPasswordEnc != ""
		}
		v.Port = slotPort(v.Slot)
		v.SlotMismatch = n.Slot != v.Slot || (n.Pool != nil && *n.Pool != poolFlag(v.Kind))
		v.MaxUsers = maxUsers(v.Kind, v.CapacityMbps)
		v.Seats = activeByMAC[n.MAC]
		if macs := group[n.RemoteIP]; len(macs) > 0 {
			v.IPGroupSize = len(macs)
			for _, m := range macs {
				v.ActiveUsers += activeByMAC[m]
			}
		} else {
			v.IPGroupSize = 1
			v.ActiveUsers = activeByMAC[n.MAC]
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
		MapsURL      *string  `json:"maps_url"` // Google Maps share link; resolved into lat/lon here, "" clears the link
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// GET /api/overview — one call for the dashboard header cards.
func (s *server) handleOverview(w http.ResponseWriter, r *http.Request) {
	users := []*User{}
	for _, u := range s.store.List() {
		if !isReservedUsername(u.Username) { // a measurement is not a customer
			users = append(users, u)
		}
	}
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
