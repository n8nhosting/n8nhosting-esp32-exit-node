package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// createUser makes a proxy user through the API, failing the test otherwise.
func createUser(t *testing.T, mux *http.ServeMux, body map[string]any) {
	t.Helper()
	if rr, out := doJSON(t, mux, "POST", "/api/users", "admintok", body); rr.Code != http.StatusCreated {
		t.Fatalf("create %v: %d %v", body["username"], rr.Code, out)
	}
}

// bindUser sets a user's device directly in the store (no admission check).
func bindUser(t *testing.T, s *server, name, mac string) {
	t.Helper()
	if err := s.store.Update(name, func(u *User) error { u.DeviceMAC = mac; return nil }); err != nil {
		t.Fatalf("bind %s: %v", name, err)
	}
}

func TestAssignRespectsCapacityRule(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	a, b := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02" // two boards behind one public IP
	ded, unmeasured := "aa:bb:cc:dd:ee:03", "aa:bb:cc:dd:ee:04"
	recordOnly := "aa:bb:cc:dd:ee:05" // a device record the broker does not list
	fb.nodes = []brokerNode{
		{MAC: a, RemoteIP: "27.1.1.1"}, {MAC: b, RemoteIP: "27.1.1.1"},
		{MAC: ded, RemoteIP: "8.8.8.8"}, {MAC: unmeasured, RemoteIP: "9.9.9.9"},
	}
	for _, d := range []struct {
		mac, kind string
		capMbps   float64
	}{{a, "shared", 12}, {b, "shared", 12}, {ded, "dedicated", 0}, {recordOnly, "dedicated", 0}} {
		if err := s.devices.Upsert(d.mac, func(x *Device) error { x.Kind, x.CapacityMbps = d.kind, d.capMbps; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"u1", "u2", "u3", "u4", "u5"} {
		createUser(t, mux, map[string]any{"username": name, "password": "pw"})
	}
	// a measurement user bound into the group takes no seat
	if err := s.store.Create(&User{Username: "_measure-ab12cd", PassHash: hashPassword("x"), Enabled: true, DeviceMAC: a}); err != nil {
		t.Fatal(err)
	}
	assign := func(user, mac string) (int, map[string]any) {
		rr, out := doJSON(t, mux, "POST", "/api/users/"+user+"/assign", "admintok", map[string]any{"device_mac": mac})
		return rr.Code, out
	}
	refused := func(user, mac string, code int, want map[string]any) {
		t.Helper()
		got, out := assign(user, mac)
		if got != code {
			t.Fatalf("assign %s -> %s: %d %v, want %d %v", user, mac, got, out, code, want)
		}
		for k, v := range want {
			if out[k] != v {
				t.Fatalf("assign %s -> %s: %v, want %v", user, mac, out, want)
			}
		}
	}
	ok := func(user, mac string) {
		t.Helper()
		if code, out := assign(user, mac); code != http.StatusOK || out["device_mac"] != mac {
			t.Fatalf("assign %s -> %s: %d %v", user, mac, code, out)
		}
	}

	// Each board at 12 Mbps has two independent seats, despite one public IP.
	ok("u1", a)
	ok("u2", b)
	ok("u3", a)
	ok("u4", b)
	full := map[string]any{"error": "capacity", "reason": "shared device full (2/2)"}
	refused("u5", a, http.StatusConflict, full)
	refused("u5", b, http.StatusConflict, full)
	ok("u1", a)                                 // self-reassignment does not take a second seat
	refused("u1", b, http.StatusConflict, full) // transfers need room on target
	// PUT device_mac is held to the same rule
	if rr, out := doJSON(t, mux, "PUT", "/api/users/u5", "admintok", map[string]any{"device_mac": a}); rr.Code != http.StatusConflict || out["reason"] != "shared device full (2/2)" {
		t.Fatalf("PUT device_mac into a full board: %d %v", rr.Code, out)
	}

	// dedicated: exactly one user
	ok("u3", ded)
	ok("u3", ded) // re-assigning the holder is fine
	refused("u4", ded, http.StatusConflict, map[string]any{"error": "capacity", "reason": "dedicated device already has a user"})

	// a shared board that was never measured takes nobody
	refused("u4", unmeasured, http.StatusConflict, map[string]any{"error": "capacity", "reason": "capacity not measured"})

	// unknown to both the broker and the devices table
	refused("u4", "aa:bb:cc:dd:ee:99", http.StatusNotFound, map[string]any{"error": "unknown device"})
	refused("nobody", a, http.StatusNotFound, map[string]any{"error": "not found"})

	// refused assignments leave the user where they were
	for name, want := range map[string]string{"u1": a, "u3": ded, "u4": b, "u5": ""} {
		u, err := s.store.Get(name)
		if err != nil || u.DeviceMAC != want {
			t.Fatalf("%s bound to %q (%v), want %q", name, u.DeviceMAC, err, want)
		}
	}

	// a device known only from its record is assignable
	ok("u4", recordOnly)
	ok("u1", b) // moving to another board works after that board frees a seat
	// unassign is unchanged
	if rr, out := doJSON(t, mux, "POST", "/api/users/u1/unassign", "admintok", nil); rr.Code != http.StatusOK || out["device_mac"] != nil {
		t.Fatalf("unassign: %d %v", rr.Code, out)
	}
	ok("u5", a) // a seat freed up
}

// Dedicated ownership covers exactly one board, never others sharing its IP.
func TestDedicatedBoardsHaveIndependentSeatsOnSameIP(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	ded, other, shared := "aa:bb:cc:dd:ee:11", "aa:bb:cc:dd:ee:12", "aa:bb:cc:dd:ee:13"
	fb.nodes = []brokerNode{{MAC: ded, RemoteIP: "27.2.2.2"}, {MAC: other, RemoteIP: "27.2.2.2"}, {MAC: shared, RemoteIP: "27.2.2.2"}}
	for _, mac := range []string{ded, other} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.Kind = "dedicated"; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.devices.Upsert(shared, func(d *Device) error { d.CapacityMbps = 20; return nil }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"u1", "u2", "u3", "u4", "u5", "off"} {
		createUser(t, mux, map[string]any{"username": name, "password": "pw"})
	}
	// neither a disabled user nor a measurement user on the shared board is "active"
	bindUser(t, s, "off", shared)
	doJSON(t, mux, "POST", "/api/users/off/disable", "admintok", nil)
	if err := s.store.Create(&User{Username: "_measure-cd34ef", PassHash: hashPassword("x"), Enabled: true, DeviceMAC: shared}); err != nil {
		t.Fatal(err)
	}
	assign := func(user, mac string, code int, reason string) {
		t.Helper()
		rr, out := doJSON(t, mux, "POST", "/api/users/"+user+"/assign", "admintok", map[string]any{"device_mac": mac})
		if rr.Code != code || (reason != "" && (out["error"] != "capacity" || out["reason"] != reason)) {
			t.Fatalf("assign %s -> %s: %d %v, want %d %q", user, mac, rr.Code, out, code, reason)
		}
	}

	assign("u1", ded, http.StatusOK, "")
	assign("u2", shared, http.StatusOK, "")
	assign("u3", other, http.StatusOK, "")
	assign("u1", ded, http.StatusOK, "")
	assign("u2", ded, http.StatusConflict, "dedicated device already has a user")
	assign("u2", other, http.StatusConflict, "dedicated device already has a user")
	assign("u4", shared, http.StatusOK, "")
	assign("u5", shared, http.StatusOK, "") // independent shared board reaches 3
	assign("u1", shared, http.StatusConflict, "shared device full (3/3)")
	u, _ := s.store.Get("u1")
	if u.DeviceMAC != ded {
		t.Fatal("failed transfer changed the dedicated holder")
	}
	doJSON(t, mux, "POST", "/api/users/u3/unassign", "admintok", nil)
	assign("u2", other, http.StatusOK, "") // shared IP occupancy does not block it
}

func TestAuthScopedToSlotPort(t *testing.T) {
	s, mux := newTestServer(t)
	slotted, unslotted := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"
	if err := s.devices.Upsert(slotted, func(d *Device) error { d.Slot = 3; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.devices.Upsert(unslotted, func(d *Device) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for name, mac := range map[string]string{"bound": slotted, "legacy": unslotted, "free": ""} {
		createUser(t, mux, map[string]any{"username": name, "password": "pw"})
		bindUser(t, s, name, mac)
	}
	auth := func(user, pass, slot string) map[string]any {
		path := "/gost/auth"
		if slot != "" {
			path += "?slot=" + slot
		}
		_, out := doJSON(t, mux, "POST", path, "", map[string]any{"username": user, "password": pass, "client": "1.2.3.4:5"})
		return out
	}
	for _, c := range []struct {
		user, slot string
		ok         bool
	}{
		{"bound", "3", true}, {"bound", "4", false}, {"bound", "", false},
		{"free", "", true}, {"free", "3", false},
		// bound to a board without a slot yet: keeps the pool port until one is assigned
		{"legacy", "", true}, {"legacy", "3", false},
		{"bound", "x", false}, {"bound", "0", false},
	} {
		out := auth(c.user, "pw", c.slot)
		if out["ok"] != c.ok {
			t.Errorf("auth %s slot=%q: %v, want ok=%v", c.user, c.slot, out, c.ok)
		}
		if c.ok && out["id"] != c.user {
			t.Errorf("auth %s slot=%q: id %v", c.user, c.slot, out["id"])
		}
	}
	// the slot port still checks the password
	if out := auth("bound", "bad", "3"); out["ok"] != false {
		t.Fatalf("bad password on the right port: %v", out)
	}
}

func TestLimiterCappedByDeviceCapacity(t *testing.T) {
	s, mux := newTestServer(t)
	dev, unmeasured := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"
	if err := s.devices.Upsert(dev, func(d *Device) error { d.CapacityMbps = 12; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.devices.Upsert(unmeasured, func(d *Device) error { return nil }); err != nil {
		t.Fatal(err)
	}
	for _, u := range []struct {
		name string
		rate float64
		mac  string
	}{
		{"big", 100, dev}, {"zero", 0, dev}, {"slow", 5, dev},
		{"free", 20, ""}, {"unm", 20, unmeasured},
	} {
		createUser(t, mux, map[string]any{"username": u.name, "password": "pw", "rate_mbps": u.rate})
		bindUser(t, s, u.name, u.mac)
	}
	// a measurement user is made through the store, as the measurement flow does
	if err := s.store.Create(&User{Username: "_measure-ab12cd", PassHash: hashPassword("pw"), Enabled: true, RateMbps: 5, DeviceMAC: dev}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		client string
		want   int64
	}{
		{"big", 12_000_000 / 8},  // capped at the board's capacity
		{"zero", 12_000_000 / 8}, // unlimited means the board's capacity
		{"slow", 5_000_000 / 8},  // below capacity: the user's own rate
		{"free", 20_000_000 / 8}, // unbound: the user's rate, as before
		{"unm", 20_000_000 / 8},  // board never measured: the user's rate
		{"_measure-ab12cd", 0},   // a measurement is never capped by the last one
		{"nobody", 0},            // unknown client: unlimited, as before
	} {
		_, out := doJSON(t, mux, "POST", "/gost/limiter", "", map[string]any{"scope": "client", "client": c.client})
		in, _ := out["in"].(float64)
		o, _ := out["out"].(float64)
		if int64(in) != c.want || int64(o) != c.want {
			t.Errorf("limiter %s: %v, want in=out=%d", c.client, out, c.want)
		}
	}
}

// The "_measure-" prefix buys no rate cap and no seat, so no HTTP boundary may
// create such a user: not the admin API, not a payment.
func TestReservedUsernameRefusedOverHTTP(t *testing.T) {
	s, mux := newTestServer(t)
	rr, out := doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "_measure-x", "password": "pw", "rate_mbps": 10})
	if rr.Code != http.StatusBadRequest || out["error"] != "reserved username" {
		t.Fatalf("POST /api/users _measure-x: %d %v", rr.Code, out)
	}
	pay := func(body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", "/webhook/payment", strings.NewReader(body))
		req.Header.Set("X-Webhook-Token", "paytok")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := pay(`{"username":"_measure-y","days":30,"rate_mbps":20}`); code != http.StatusBadRequest || out["error"] != "reserved username" {
		t.Fatalf("payment for _measure-y: %d %v", code, out)
	}
	for _, name := range []string{"_measure-x", "_measure-y"} {
		if _, err := s.store.Get(name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%s was created: %v", name, err)
		}
	}
	// nor may a payment extend a live measurement user
	if err := s.store.Create(&User{Username: "_measure-z", PassHash: hashPassword("pw"), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if code, out := pay(`{"username":"_measure-z","days":30}`); code != http.StatusBadRequest {
		t.Fatalf("payment for an existing _measure-z: %d %v", code, out)
	}
	if u, err := s.store.Get("_measure-z"); err != nil || u.ExpiresAt != "" {
		t.Fatalf("_measure-z changed: %+v %v", u, err)
	}
	// ordinary names still work on both paths
	createUser(t, mux, map[string]any{"username": "measure-ok", "password": "pw"})
	if code, out := pay(`{"username":"buyer","days":30}`); code != http.StatusOK || out["username"] != "buyer" {
		t.Fatalf("payment for buyer: %d %v", code, out)
	}
}

// Usernames are [A-Za-z0-9._-]{1,64} wherever a user is created over HTTP, so
// a name can never break out of the dashboard's markup or the proxy URL.
func TestUsernameCharsetEnforced(t *testing.T) {
	s, mux := newTestServer(t)
	bad := []string{"ab'c", `a"b`, "a b", "<b>", "a/b", "a:b", "a@b", "ä", strings.Repeat("a", 65)}
	for _, name := range bad {
		rr, out := doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": name, "password": "pw"})
		if rr.Code != http.StatusBadRequest || out["error"] != "invalid username" {
			t.Errorf("POST /api/users %q: %d %v, want 400 invalid username", name, rr.Code, out)
		}
	}
	pay := func(name string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]any{"username": name, "days": 30})
		req := httptest.NewRequest("POST", "/webhook/payment", strings.NewReader(string(body)))
		req.Header.Set("X-Webhook-Token", "paytok")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	for _, name := range []string{"ab'c", `x"y`} {
		if code, out := pay(name); code != http.StatusBadRequest || out["error"] != "invalid username" {
			t.Errorf("payment for %q: %d %v, want 400 invalid username", name, code, out)
		}
	}
	for _, name := range append(bad, `x"y`) {
		if _, err := s.store.Get(name); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%q was created: %v", name, err)
		}
	}
	// the whole charset, up to 64 characters, still works on both paths; the
	// reserved prefix is still refused
	createUser(t, mux, map[string]any{"username": "A.b_c-9", "password": "pw"})
	createUser(t, mux, map[string]any{"username": strings.Repeat("z", 64), "password": "pw"})
	if code, out := pay("buyer.1"); code != http.StatusOK || out["username"] != "buyer.1" {
		t.Fatalf("payment for buyer.1: %d %v", code, out)
	}
	if rr, out := doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "_measure-q", "password": "pw"}); rr.Code != http.StatusBadRequest || out["error"] != "reserved username" {
		t.Fatalf("reserved prefix: %d %v", rr.Code, out)
	}
}

func TestDeviceSlotAssignmentForwardsToBroker(t *testing.T) {
	initCredKey("", "admintok")
	t.Setenv("PROXY_PUBLIC_HOST", "203.0.113.9")
	t.Setenv("PROXY_PUBLIC_PORT", "3128")
	s, mux, fb := newTestServerWithBroker(t)
	a, b := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"
	fb.nodes = []brokerNode{{MAC: a, RemoteIP: "27.1.1.1"}, {MAC: b, RemoteIP: "8.8.8.8"}}
	put := func(mac string, body map[string]any) (int, map[string]any) {
		rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", body)
		return rr.Code, out
	}
	lastCall := func(want string, n int) {
		t.Helper()
		calls := fb.callLog()
		if len(calls) != n || calls[n-1] != want {
			t.Fatalf("broker calls %q, want %d ending %q", calls, n, want)
		}
	}

	// slot 3 goes to the broker first, then the record; other fields ride along,
	// and the push carries the pool flag of the resulting kind
	code, d := put(a, map[string]any{"slot": 3, "name": "Home A", "kind": "dedicated"})
	if code != http.StatusOK || d["slot"] != float64(3) || d["port"] != float64(3133) || d["name"] != "Home A" || d["kind"] != "dedicated" {
		t.Fatalf("PUT slot 3: %d %v", code, d)
	}
	lastCall("/nodes/"+a+"/slot {\"slot\":3,\"pool\":false}", 1)

	// another board cannot take a held slot; the broker is not asked
	if code, out := put(b, map[string]any{"slot": 3}); code != http.StatusConflict || out["error"] != "slot taken" {
		t.Fatalf("duplicate slot: %d %v", code, out)
	}
	// 1..SLOT_COUNT (default 30); 0 clears
	for _, n := range []int{-1, 31} {
		if code, out := put(b, map[string]any{"slot": n}); code != http.StatusBadRequest {
			t.Fatalf("slot %d: %d %v", n, code, out)
		}
	}
	if n := len(fb.callLog()); n != 1 {
		t.Fatalf("refused PUTs reached the broker: %q", fb.callLog())
	}

	// the broker refuses: 502, and nothing in the request is saved
	fb.setSlotStatus(http.StatusConflict)
	code, out := put(b, map[string]any{"slot": 4, "name": "Home B"})
	if msg, _ := out["error"].(string); code != http.StatusBadGateway || !strings.HasPrefix(msg, "broker") {
		t.Fatalf("broker refusal: %d %v", code, out)
	}
	if _, err := s.devices.Get(b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a refused PUT left a record: %v", err)
	}
	fb.setSlotStatus(0)

	// SLOT_COUNT widens the range
	t.Setenv("SLOT_COUNT", "40")
	if code, out := put(b, map[string]any{"slot": 31}); code != http.StatusOK || out["port"] != float64(3161) {
		t.Fatalf("slot 31 with SLOT_COUNT=40: %d %v", code, out)
	}
	lastCall("/nodes/"+b+"/slot {\"slot\":31,\"pool\":true}", 3)

	// a user bound to the slotted board connects on its slot port
	createUser(t, mux, map[string]any{"username": "gina", "password": "Pw1"})
	if rr, out := doJSON(t, mux, "POST", "/api/users/gina/assign", "admintok", map[string]any{"device_mac": a}); rr.Code != http.StatusOK {
		t.Fatalf("assign: %d %v", rr.Code, out)
	}
	_, cr := doJSON(t, mux, "POST", "/api/users/gina/credentials", "admintok", nil)
	if cr["port"] != "3133" || cr["connection"] != "gina:Pw1@203.0.113.9:3133" || cr["socks5_url"] != "socks5h://gina:Pw1@203.0.113.9:3133" {
		t.Fatalf("credentials on slot 3: %v", cr)
	}

	// slot 0 clears the broker and the record; credentials fall back to the pool port
	code, d = put(a, map[string]any{"slot": 0})
	if code != http.StatusOK || d["slot"] != float64(0) || d["port"] != float64(0) || d["name"] != "Home A" {
		t.Fatalf("PUT slot 0: %d %v", code, d)
	}
	lastCall("/nodes/"+a+"/slot {\"slot\":0,\"pool\":false}", 4)
	_, cr = doJSON(t, mux, "POST", "/api/users/gina/credentials", "admintok", nil)
	if cr["port"] != "3128" || cr["connection"] != "gina:Pw1@203.0.113.9:3128" {
		t.Fatalf("credentials without a slot: %v", cr)
	}

	// POST …/slot is the same operation, never a bare broker passthrough
	if rr, out := doJSON(t, mux, "POST", "/api/devices/"+a+"/slot", "admintok", map[string]any{"slot": 5}); rr.Code != http.StatusOK || out["port"] != float64(3135) {
		t.Fatalf("POST slot: %d %v", rr.Code, out)
	}
	lastCall("/nodes/"+a+"/slot {\"slot\":5,\"pool\":false}", 5)
	if dev, err := s.devices.Get(a); err != nil || dev.Slot != 5 {
		t.Fatalf("POST slot not recorded: %+v %v", dev, err)
	}
}

// A dedicated board never serves the shared pool (spec §6.2.14): changing a
// device's kind pushes its current slot with the new pool flag to the broker,
// even when it has no slot; a board the broker does not know yet is fine.
func TestDeviceKindChangePushesPoolFlag(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	a, b, c := "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"
	fb.nodes = []brokerNode{{MAC: a, RemoteIP: "27.1.1.1"}, {MAC: b, RemoteIP: "8.8.8.8"}}
	for mac, slot := range map[string]int{a: 4, c: 6} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.Slot = slot; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	put := func(mac string, body map[string]any) (int, map[string]any) {
		rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", body)
		return rr.Code, out
	}
	calls := func(want ...string) {
		t.Helper()
		if got := fb.callLog(); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("broker calls %q, want %q", got, want)
		}
	}

	if code, d := put(a, map[string]any{"kind": "dedicated"}); code != http.StatusOK || d["kind"] != "dedicated" || d["slot"] != float64(4) {
		t.Fatalf("kind -> dedicated: %d %v", code, d)
	}
	want := []string{"/nodes/" + a + "/slot {\"slot\":4,\"pool\":false}"}
	calls(want...)
	// no kind change, no push
	put(a, map[string]any{"name": "Home A"})
	put(a, map[string]any{"kind": "dedicated"})
	calls(want...)
	if code, _ := put(a, map[string]any{"kind": "shared"}); code != http.StatusOK {
		t.Fatalf("kind -> shared: %d", code)
	}
	want = append(want, "/nodes/"+a+"/slot {\"slot\":4,\"pool\":true}")
	calls(want...)

	// a board without a slot (and without a record yet) still gets its pool flag
	if code, d := put(b, map[string]any{"kind": "dedicated"}); code != http.StatusOK || d["kind"] != "dedicated" || d["slot"] != float64(0) {
		t.Fatalf("slotless kind -> dedicated: %d %v", code, d)
	}
	want = append(want, "/nodes/"+b+"/slot {\"slot\":0,\"pool\":false}")
	calls(want...)

	// the broker has no node for c yet: the 404 is ignored and the kind saved
	fb.setSlotStatus(http.StatusNotFound)
	if code, d := put(c, map[string]any{"kind": "dedicated"}); code != http.StatusOK || d["kind"] != "dedicated" {
		t.Fatalf("kind change on a board the broker does not know: %d %v", code, d)
	}
	// any other broker failure saves nothing
	fb.setSlotStatus(http.StatusInternalServerError)
	if code, out := put(c, map[string]any{"kind": "shared"}); code != http.StatusBadGateway {
		t.Fatalf("kind change with the broker failing: %d %v", code, out)
	}
	if dev, err := s.devices.Get(c); err != nil || dev.Kind != "dedicated" {
		t.Fatalf("a refused kind change was saved: %+v %v", dev, err)
	}
}
