package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSameIPBoardsConcurrentAssignTakeOnlyOwnLastSeat(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const a, b = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"
	fb.nodes = []brokerNode{{MAC: a, RemoteIP: "27.1.1.1"}, {MAC: b, RemoteIP: "27.1.1.1"}}
	for _, mac := range []string{a, b} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.CapacityMbps = 2.6; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	const attempts = 12
	for i := 0; i < attempts; i++ {
		createUser(t, mux, map[string]any{"username": fmt.Sprintf("race%d", i), "password": "pw"})
	}
	start := make(chan struct{})
	results := make(chan int, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			mac := a
			if i%2 == 1 {
				mac = b
			}
			req := httptest.NewRequest("POST", fmt.Sprintf("/api/users/race%d/assign", i), strings.NewReader(`{"device_mac":"`+mac+`"}`))
			req.Header.Set("Authorization", "Bearer admintok")
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)
			results <- rr.Code
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	ok := 0
	for code := range results {
		if code == http.StatusOK {
			ok++
		} else if code != http.StatusConflict {
			t.Fatalf("unexpected assignment response %d", code)
		}
	}
	if ok != 2 {
		t.Fatalf("same IP boards should admit one each, got %d successes", ok)
	}
	counts := map[string]int{}
	for _, u := range s.store.List() {
		if u.DeviceMAC != "" {
			counts[u.DeviceMAC]++
		}
	}
	if counts[a] != 1 || counts[b] != 1 {
		t.Fatalf("independent last seats oversold: %v", counts)
	}
}

func TestOverviewSummarizesIndependentBoardSeats(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const a, b, dedicated = "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02", "aa:bb:cc:dd:ee:03"
	fb.nodes = []brokerNode{{MAC: a, RemoteIP: "27.1.1.1", Online: true, Slot: 1}, {MAC: b, RemoteIP: "27.1.1.1", Online: true, Slot: 2}, {MAC: dedicated, RemoteIP: "27.1.1.1", Online: true, Slot: 3}}
	for i, mac := range []string{a, b, dedicated} {
		if err := s.devices.Upsert(mac, func(d *Device) error {
			d.Slot = i + 1
			d.CapacityMbps = 2.6
			if mac == a {
				d.CapacityMbps = 10
			}
			if mac == dedicated {
				d.Kind, d.CapacityMbps = "dedicated", 0
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	createUser(t, mux, map[string]any{"username": "shared", "password": "pw"})
	createUser(t, mux, map[string]any{"username": "dedicated", "password": "pw"})
	bindUser(t, s, "shared", a)
	bindUser(t, s, "dedicated", dedicated)
	rr, out := doJSON(t, mux, "GET", "/api/overview", "admintok", nil)
	seats, _ := out["seats"].(map[string]any)
	if rr.Code != 200 || seats["occupied"] != float64(2) || seats["available"] != float64(2) {
		t.Fatalf("overview same-IP seats: %d %v", rr.Code, out)
	}
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	for i, want := range []float64{1, 0, 1} {
		if list[i]["active_users"] != want || list[i]["ip_group_size"] != float64(3) {
			t.Fatalf("board occupancy / informational IP metadata: %v", list[i])
		}
	}
}
