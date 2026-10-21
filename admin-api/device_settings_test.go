package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManualCapacitySettings(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const mac = "aa:bb:cc:dd:ee:01"
	fb.nodes = []brokerNode{{MAC: mac, Online: true}}
	if err := s.devices.Upsert(mac, func(d *Device) error {
		d.CapacityMbps, d.CapacityMeasuredAt, d.CapacityActiveStreams = 12, "2026-10-22T02:00:00Z", 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Omission preserves a real measurement and its metadata.
	rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"notes": "retained"})
	if rr.Code != 200 || out["capacity_mbps"] != float64(12) || out["capacity_source"] != "measured" || out["capacity_measured_at"] != "2026-10-22T02:00:00Z" || out["capacity_active_streams"] != float64(3) {
		t.Fatalf("omitted speed: %d %v", rr.Code, out)
	}
	// A small C3 override gets one seat and cannot retain a test timestamp.
	rr, out = doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"capacity_mbps": 2.6, "wifi_password": "private-pass"})
	if rr.Code != 200 || out["capacity_mbps"] != 2.6 || out["max_users"] != float64(1) || out["capacity_source"] != "manual" || out["capacity_measured_at"] != "" || out["capacity_active_streams"] != float64(0) {
		t.Fatalf("manual speed: %d %v", rr.Code, out)
	}
	// Read through a new store instance: speed and cleared metadata are in SQLite.
	d, err := NewDeviceStore(s.store.DB()).Get(mac)
	if err != nil || d.CapacityMbps != 2.6 || d.CapacityMeasuredAt != "" || d.CapacityActiveStreams != 0 || d.Notes != "retained" {
		t.Fatalf("persisted override: %+v %v", d, err)
	}
	var kind, payload string
	if err := s.store.DB().QueryRow(`SELECT kind,data FROM events WHERE subject_id=? ORDER BY id DESC LIMIT 1`, mac).Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(payload), &event); err != nil {
		t.Fatal(err)
	}
	if kind != "device.updated" || event["source"] != "manual" || event["capacity_mbps"] != 2.6 || strings.Contains(payload, "private-pass") {
		t.Fatalf("safe manual event: %s %s", kind, payload)
	}
	// Zero is an explicit reset, not an omitted value.
	rr, out = doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"capacity_mbps": 0})
	if rr.Code != 200 || out["capacity_mbps"] != float64(0) || out["max_users"] != float64(0) || out["capacity_source"] != "unmeasured" || out["capacity_measured_at"] != "" || out["capacity_active_streams"] != float64(0) {
		t.Fatalf("reset speed: %d %v", rr.Code, out)
	}
	if len(fb.callLog()) != 0 {
		t.Fatalf("speed-only settings must not change broker routing: %v", fb.callLog())
	}
}

func TestManualCapacityValidationAndMeasurementConflict(t *testing.T) {
	s, mux, _ := newTestServerWithBroker(t)
	const mac = "aa:bb:cc:dd:ee:02"
	for _, speed := range []string{"null", "-0.1", "100000.1", `"12"`, "true", "{}", "[]", "1e309", "NaN", "Infinity"} {
		t.Run(speed, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/devices/"+mac, strings.NewReader(`{"name":"must not save","capacity_mbps":`+speed+`}`))
			req.Header.Set("Authorization", "Bearer admintok")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("invalid speed %s: %d %s", speed, rr.Code, rr.Body.String())
			}
			if _, err := s.devices.Get(mac); err != ErrNotFound {
				t.Fatalf("invalid request saved a record: %v", err)
			}
		})
	}
	if !s.beginMeasure(mac) {
		t.Fatal("could not start measurement")
	}
	rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"capacity_mbps": 10})
	if rr.Code != http.StatusConflict || out["error"] != "measurement in progress" {
		t.Fatalf("concurrent override: %d %v", rr.Code, out)
	}
	s.endMeasure(mac)
	// Both boundaries are accepted after a measurement releases the lock.
	for _, speed := range []float64{0, 100000} {
		rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"capacity_mbps": speed})
		if rr.Code != 200 || out["capacity_mbps"] != speed {
			t.Fatalf("boundary %v: %d %v", speed, rr.Code, out)
		}
	}
}

func TestManualCapacityUsesExistingGroupAdmission(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const a, b = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"
	fb.nodes = []brokerNode{{MAC: a, RemoteIP: "27.1.1.1"}, {MAC: b, RemoteIP: "27.1.1.1"}}
	for _, name := range []string{"first", "second"} {
		createUser(t, mux, map[string]any{"username": name, "password": "pw"})
	}
	assign := func(name, mac string, want int) {
		t.Helper()
		rr, out := doJSON(t, mux, "POST", "/api/users/"+name+"/assign", "admintok", map[string]any{"device_mac": mac})
		if rr.Code != want {
			t.Fatalf("assign %s: %d %v, want %d", name, rr.Code, out, want)
		}
	}
	assign("first", a, 409) // no measured or manual capacity yet
	for _, mac := range []string{a, b} {
		if rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"capacity_mbps": 2.6}); rr.Code != 200 {
			t.Fatalf("save capacity: %d %v", rr.Code, out)
		}
	}
	assign("first", a, 200)
	assign("second", b, 409) // one IP still has only one seat
	if rr, _ := doJSON(t, mux, "PUT", "/api/devices/"+b, "admintok", map[string]any{"capacity_mbps": 10}); rr.Code != 200 {
		t.Fatal("could not raise capacity")
	}
	assign("second", b, 200)
	if rr, _ := doJSON(t, mux, "PUT", "/api/devices/"+a, "admintok", map[string]any{"capacity_mbps": 0}); rr.Code != 200 {
		t.Fatal("could not reset capacity")
	}
	// Reset does not remove existing users, but subsequent admission is blocked.
	u, _ := s.store.Get("first")
	if u.DeviceMAC != a {
		t.Fatal("reset must preserve existing user binding")
	}
	assign("first", a, 409)
}

func TestBoardDisplayNameFallback(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	for _, tc := range []struct{ mac, name, want string }{
		{"aa:bb:cc:dd:ee:01", "", "Board-EE01"},
		{"AC:A7:04:BA:AE:C0", "", "Board-AEC0"},
		{"aa-bb-cc-dd-00-12", "", "Board-0012"},
		{"aabb.ccdd.1234", "", "Board-1234"},
		{"aa:bb:cc:dd:ee:02", "  ", "Board-EE02"},
		{"aa:bb:cc:dd:ee:03", "My board", "My board"},
		{"aa:bb:cc:dd:ee:04", "Board 1", "Board 1"},
	} {
		if got := boardDisplayName(tc.mac, tc.name); got != tc.want {
			t.Errorf("name %q, %q: %q, want %q", tc.mac, tc.name, got, tc.want)
		}
	}
	const fresh, blank, custom = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"
	fb.nodes = []brokerNode{{MAC: fresh}, {MAC: blank}, {MAC: custom}}
	for mac, name := range map[string]string{blank: "  ", custom: "My board"} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.Name = name; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	for i, want := range []string{"Board-EE01", "Board-EE02", "My board"} {
		if list[i]["name"] != want {
			t.Errorf("device %d: %v, want %s", i, list[i]["name"], want)
		}
	}
	if _, err := s.devices.Get(fresh); err != ErrNotFound {
		t.Fatal("displaying a default must not create a record")
	}
	d, _ := s.devices.Get(blank)
	if d.Name != "  " {
		t.Fatal("displaying a default must not rewrite a record")
	}
}
