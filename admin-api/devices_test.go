package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeBroker stands in for the broker's status API: it serves GET /nodes from
// nodes and records POST /nodes/{mac}/<action> as "<path> <body>" in calls.
// POSTs answer 200 {"ok":true}, except /slot while slotStatus is set.
type fakeBroker struct {
	*httptest.Server
	mu         sync.Mutex
	nodes      []brokerNode
	calls      []string
	slotStatus int
}

func newFakeBroker(t *testing.T) *fakeBroker {
	fb := &fakeBroker{}
	fb.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/nodes":
			json.NewEncoder(w).Encode(fb.nodes)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/nodes/"):
			b, _ := io.ReadAll(r.Body)
			fb.calls = append(fb.calls, r.URL.Path+" "+string(b))
			if code := fb.slotStatus; code != 0 && strings.HasSuffix(r.URL.Path, "/slot") {
				msg := http.StatusText(code)
				if code == http.StatusConflict {
					msg = "slot taken" // what the broker says
				}
				http.Error(w, msg, code)
				return
			}
			w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(fb.Close)
	t.Setenv("BROKER_STATUS_URL", fb.URL)
	return fb
}

// setSlotStatus makes POST /nodes/{mac}/slot answer code; 0 restores 200.
func (fb *fakeBroker) setSlotStatus(code int) {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.slotStatus = code
}

// callLog is a copy of the POSTs recorded so far.
func (fb *fakeBroker) callLog() []string {
	fb.mu.Lock()
	defer fb.mu.Unlock()
	return append([]string(nil), fb.calls...)
}

func newTestServerWithBroker(t *testing.T) (*server, *http.ServeMux, *fakeBroker) {
	fb := newFakeBroker(t)
	s, mux := newTestServer(t)
	return s, mux, fb
}

// doJSONList is doJSON for endpoints that return a JSON array.
func doJSONList(t *testing.T, mux *http.ServeMux, method, path, token string) (*httptest.ResponseRecorder, []map[string]any) {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("list decode: %v (%s)", err, rr.Body.String())
	}
	return rr, out
}

func TestDevicePutAndReveal(t *testing.T) {
	_, mux, fb := newTestServerWithBroker(t)
	fb.nodes = []brokerNode{{MAC: "aa:bb:cc:dd:ee:01", RemoteIP: "27.1.1.1", Online: true}}
	rr, d := doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"name": "Home A", "kind": "dedicated", "lat": 13.75, "lon": 100.5, "wifi_password": "s3cret"})
	if rr.Code != 200 || d["name"] != "Home A" || d["kind"] != "dedicated" || d["wifi_password_set"] != true || d["lat"] != 13.75 {
		t.Fatalf("%d %v", rr.Code, d)
	}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	if _, has := list[0]["wifi_password"]; has {
		t.Fatal("password must never be in the list")
	}
	if strings.Contains(rr.Body.String(), "s3cret") {
		t.Fatal("password leaked in PUT response")
	}
	_, rv := doJSON(t, mux, "POST", "/api/devices/aa:bb:cc:dd:ee:01/reveal-wifi", "admintok", nil)
	if rv["wifi_password"] != "s3cret" {
		t.Fatalf("reveal: %v", rv)
	}
	rr, _ = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"lat": 123})
	if rr.Code != 400 {
		t.Fatal("lat out of range must be 400")
	}
	rr, _ = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"lon": -181})
	if rr.Code != 400 {
		t.Fatal("lon out of range must be 400")
	}
	rr, _ = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"kind": "weird"})
	if rr.Code != 400 {
		t.Fatal("bad kind must be 400")
	}
	// absent leaves unchanged; empty clears
	_, d = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"notes": "n"})
	if d["wifi_password_set"] != true || d["name"] != "Home A" || d["notes"] != "n" {
		t.Fatalf("absent fields must stay: %v", d)
	}
	_, d = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"wifi_password": ""})
	if d["wifi_password_set"] != false {
		t.Fatalf("empty must clear: %v", d)
	}
	_, rv = doJSON(t, mux, "POST", "/api/devices/aa:bb:cc:dd:ee:01/reveal-wifi", "admintok", nil)
	if rv["wifi_password"] != "" {
		t.Fatalf("reveal after clear: %v", rv)
	}
}

func TestDeviceListWithoutRow(t *testing.T) {
	_, mux, fb := newTestServerWithBroker(t)
	fb.nodes = []brokerNode{{MAC: "aa:bb:cc:dd:ee:09", RemoteIP: "9.9.9.9", Chip: "esp32c3"}}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	if len(list) != 1 || list[0]["kind"] != "shared" || list[0]["lat"] != nil || list[0]["port"] != float64(0) || list[0]["max_users"] != float64(0) {
		t.Fatalf("zero-valued device: %v", list)
	}
	if list[0]["chip"] != "esp32c3" {
		t.Fatalf("chip must pass through from the broker: %v", list[0]["chip"])
	}
	if v, ok := list[0]["lat"]; !ok || v != nil {
		t.Fatalf("lat must be present as null: %v", list[0])
	}
}

// A board whose broker routing differs from its record (the broker lost the
// slot, holds another one, or still pools a dedicated board) is flagged so the
// operator sees the drift and can re-push it from the editor.
func TestDeviceSlotMismatch(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	yes := true
	inSync, lost, other, pooled, extra, oldBroker := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03",
		"aa:bb:cc:dd:ee:04", "aa:bb:cc:dd:ee:05", "aa:bb:cc:dd:ee:06"
	fb.nodes = []brokerNode{
		{MAC: inSync, Slot: 1, Pool: &yes},
		{MAC: lost},                        // record slot 2, broker none
		{MAC: other, Slot: 9},              // record slot 3, broker slot 9
		{MAC: pooled, Slot: 4, Pool: &yes}, // record dedicated, broker still pools it
		{MAC: extra, Slot: 7},              // no record: the broker holds a slot the console does not
		{MAC: oldBroker, Slot: 5},          // a broker without the pool field: slot alone decides
	}
	for _, d := range []struct {
		mac, kind string
		slot      int
	}{{inSync, "shared", 1}, {lost, "shared", 2}, {other, "shared", 3}, {pooled, "dedicated", 4}, {oldBroker, "dedicated", 5}} {
		if err := s.devices.Upsert(d.mac, func(x *Device) error { x.Kind, x.Slot = d.kind, d.slot; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	got := map[string]any{}
	for _, d := range list {
		got[d["mac"].(string)] = d["slot_mismatch"]
	}
	want := map[string]any{inSync: false, lost: true, other: true, pooled: true, extra: true, oldBroker: false}
	for mac, w := range want {
		if got[mac] != w {
			t.Errorf("%s: slot_mismatch = %v, want %v", mac, got[mac], w)
		}
	}
	// the view keeps the record's slot, not the broker's
	for _, d := range list {
		if d["mac"] == other && (d["slot"] != float64(3) || d["port"] != float64(3133)) {
			t.Fatalf("view must show the record's slot: %v", d)
		}
	}
}

// On start admin-api re-sends every recorded slot and pool flag to the broker
// (it may have lost them); a failure is logged and does not stop the rest.
func TestStartupRepushesSlots(t *testing.T) {
	s, _, fb := newTestServerWithBroker(t)
	for _, d := range []struct {
		mac, kind string
		slot      int
	}{{"aa:bb:cc:dd:ee:02", "shared", 2}, {"aa:bb:cc:dd:ee:01", "dedicated", 0}, {"aa:bb:cc:dd:ee:03", "dedicated", 5}} {
		if err := s.devices.Upsert(d.mac, func(x *Device) error { x.Kind, x.Slot = d.kind, d.slot; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if failed := s.pushSlotsToBroker(); failed != 0 {
		t.Fatalf("%d pushes failed against a healthy broker", failed)
	}
	want := []string{
		"/nodes/aa:bb:cc:dd:ee:01/slot {\"slot\":0,\"pool\":false}",
		"/nodes/aa:bb:cc:dd:ee:02/slot {\"slot\":2,\"pool\":true}",
		"/nodes/aa:bb:cc:dd:ee:03/slot {\"slot\":5,\"pool\":false}",
	}
	if got := fb.callLog(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("broker calls %q, want %q", got, want)
	}
	// a broker that refuses every push: all three are tried and reported
	fb.setSlotStatus(http.StatusInternalServerError)
	if failed := s.pushSlotsToBroker(); failed != 3 {
		t.Fatalf("failed = %d, want 3", failed)
	}
	if n := len(fb.callLog()); n != 6 {
		t.Fatalf("%d broker calls, want 6", n)
	}
}

func TestDeviceIPGroupAndMaxUsers(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	a, b, c := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"
	fb.nodes = []brokerNode{
		{MAC: a, RemoteIP: "27.1.1.1"}, {MAC: b, RemoteIP: "27.1.1.1"}, {MAC: c, RemoteIP: "8.8.8.8"},
	}
	// a: shared, capacity 12 -> min(3, 2) = 2; b: unmeasured -> 0; c: dedicated -> 1
	if err := s.devices.Upsert(a, func(d *Device) error { d.CapacityMbps = 12; d.Slot = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.devices.Upsert(c, func(d *Device) error { d.Kind = "dedicated"; return nil }); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	// measurement users exist only through the store (the HTTP API refuses the prefix)
	if err := s.store.Create(&User{Username: "_measure-x", PassHash: hashPassword("pw"), Enabled: true, DeviceMAC: a}); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct{ name, mac, exp string }{
		{"u1", a, ""}, {"u2", b, ""}, {"u3", b, past}, {"u4", c, ""},
	} {
		if rr, _ := doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": u.name, "password": "pw", "expires_at": u.exp}); rr.Code != 201 {
			t.Fatalf("create %s: %d", u.name, rr.Code)
		}
		if err := s.store.Update(u.name, func(x *User) error { x.DeviceMAC = u.mac; x.Enabled = true; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// disabled user in the group must not count
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "u5", "password": "pw"})
	s.store.Update("u5", func(x *User) error { x.DeviceMAC = a; x.Enabled = false; return nil })

	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	by := map[string]map[string]any{}
	for _, d := range list {
		by[d["mac"].(string)] = d
	}
	if by[a]["ip_group"] != "27.1.1.1" || by[a]["ip_group_size"] != float64(2) || by[c]["ip_group_size"] != float64(1) {
		t.Fatalf("groups: %v", list)
	}
	if by[a]["max_users"] != float64(2) || by[b]["max_users"] != float64(0) || by[c]["max_users"] != float64(1) {
		t.Fatalf("max_users: %v", list)
	}
	// group {a,b}: u1 (a) + u2 (b) active; u3 expired, _measure-x and u5 excluded
	if by[a]["active_users"] != float64(2) || by[b]["active_users"] != float64(2) || by[c]["active_users"] != float64(1) {
		t.Fatalf("active_users: %v", list)
	}
	if by[a]["port"] != float64(3132) || by[b]["port"] != float64(0) {
		t.Fatalf("port: %v", list)
	}
	// shared capacity caps at 3
	s.devices.Upsert(b, func(d *Device) error { d.CapacityMbps = 100; return nil })
	_, list = doJSONList(t, mux, "GET", "/api/devices", "admintok")
	for _, d := range list {
		if d["mac"] == b && d["max_users"] != float64(3) {
			t.Fatalf("cap 3: %v", d)
		}
	}
}

// The broker's health sample (firmware 2.4.23+) reaches the dashboard as-is;
// a board on older firmware has no stats object at all.
func TestDevicesCarryStats(t *testing.T) {
	_, mux, fb := newTestServerWithBroker(t)
	fb.nodes = []brokerNode{
		{MAC: "aa:bb:cc:dd:ee:09", Online: true, UptimeSec: 3600, Stats: &nodeStats{At: "2026-10-21T12:00:00Z", UptimeSec: 3600,
			HeapFree: 140000, HeapMin: 26000, HeapTotal: 320000, CPUPct: 12, AppBytes: 1245184, SlotBytes: 2031616, FlashBytes: 4194304, Streams: 2}},
		{MAC: "aa:bb:cc:dd:ee:08", Online: true, UptimeSec: 50},
	}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	byMac := map[string]map[string]any{}
	for _, d := range list {
		byMac[d["mac"].(string)] = d
	}
	st, _ := byMac["aa:bb:cc:dd:ee:09"]["stats"].(map[string]any)
	if st == nil || st["heap_free"].(float64) != 140000 || st["cpu_pct"].(float64) != 12 || st["flash_bytes"].(float64) != 4194304 || st["at"] != "2026-10-21T12:00:00Z" {
		t.Fatalf("stats: %v", byMac["aa:bb:cc:dd:ee:09"]["stats"])
	}
	if v, has := byMac["aa:bb:cc:dd:ee:08"]["stats"]; has && v != nil {
		t.Fatalf("old firmware must not get a stats object: %v", v)
	}
	if byMac["aa:bb:cc:dd:ee:09"]["uptime_sec"].(float64) != 3600 {
		t.Fatal("uptime must pass through")
	}
}
