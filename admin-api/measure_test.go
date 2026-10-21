package main

import (
	"context"
	"encoding/base64"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// serveAtRate answers like a download through a proxy: 200 with size bytes in
// chunks of chunk bytes, one every gap. Headers go out first, so the client's
// clock starts before the first chunk. It stops when the client goes away.
func serveAtRate(w http.ResponseWriter, r *http.Request, size, chunk int, gap time.Duration) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	f := w.(http.Flusher)
	f.Flush()
	buf := make([]byte, chunk)
	start := time.Now()
	for sent, i := 0, 1; sent < size; i++ {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(time.Until(start.Add(time.Duration(i) * gap))):
		}
		n := min(chunk, size-sent)
		if _, err := w.Write(buf[:n]); err != nil {
			return
		}
		f.Flush()
		sent += n
	}
}

// proxyUser decodes the Proxy-Authorization basic credentials of r.
func proxyUser(r *http.Request) (user, pass string, ok bool) {
	enc, found := strings.CutPrefix(r.Header.Get("Proxy-Authorization"), "Basic ")
	if !found {
		return "", "", false
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", "", false
	}
	return strings.Cut(string(raw), ":")
}

// useMeasureProxy points measureProxyURL at url for the test, recording the
// slots asked for.
func useMeasureProxy(t *testing.T, url string) func() []int {
	t.Helper()
	var mu sync.Mutex
	var slots []int
	prev := measureProxyURL
	measureProxyURL = func(slot int) string {
		mu.Lock()
		defer mu.Unlock()
		slots = append(slots, slot)
		return url
	}
	t.Cleanup(func() { measureProxyURL = prev })
	return func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), slots...)
	}
}

func measureUsers(s *server) []string {
	var out []string
	for _, u := range s.store.List() {
		if isReservedUsername(u.Username) {
			out = append(out, u.Username)
		}
	}
	return out
}

func TestMeasureViaProxyComputesMbps(t *testing.T) {
	// Pacing gives a useful observation window, not a fixed throughput oracle:
	// concurrent firmware/browser work can delay timers well beyond their gap.
	targets := make(chan string, 1)
	bodyTimes := make(chan time.Duration, 1)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targets <- r.URL.Host // a proxy is asked for the absolute URL
		started := time.Now()
		serveAtRate(w, r, 1<<20, 64<<10, 31250*time.Microsecond)
		// Record before the handler returns: the client needs its final EOF,
		// so this interval is enclosed by the outer client call below.
		bodyTimes <- time.Since(started)
	}))
	defer proxy.Close()
	started := time.Now()
	mbps, n, err := measureViaProxy(context.Background(), proxy.URL, "http://speedtest.invalid/speedtest.bin", 8<<20)
	outerElapsed := time.Since(started)
	if err != nil {
		t.Fatal(err)
	}
	if target := <-targets; target != "speedtest.invalid" {
		t.Fatalf("request did not go through the proxy: target %q", target)
	}
	if n != 1<<20 {
		t.Fatalf("bytes %d, want %d", n, 1<<20)
	}
	bodyElapsed := <-bodyTimes
	// The production timer excludes connection/headers and ends at body EOF.
	// Bound its interval using observed server/client clocks, allowing modest
	// scheduling and header/tail overhead. This still rejects bytes/s-as-Mbps,
	// a missing ×8 conversion, and an incorrect decimal scale.
	overhead := max(time.Duration(0), outerElapsed-bodyElapsed)
	fastestWindow := max(bodyElapsed-bodyElapsed/4-overhead, bodyElapsed/2)
	slowestWindow := outerElapsed + bodyElapsed/4
	bits := float64(n) * 8
	minMbps := bits / slowestWindow.Seconds() / 1e6
	maxMbps := bits / fastestWindow.Seconds() / 1e6
	if math.IsNaN(mbps) || math.IsInf(mbps, 0) || mbps < minMbps || mbps > maxMbps {
		t.Fatalf("mbps %.3f outside observed %.3f..%.3f (body=%v, outer=%v, bytes=%d)", mbps, minMbps, maxMbps, bodyElapsed, outerElapsed, n)
	}
}

func TestMeasureViaProxyFailuresAndDeadline(t *testing.T) {
	const target = "http://speedtest.invalid/speedtest.bin"
	// the proxy refuses: an error, whatever the body
	refuse := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusProxyAuthRequired)
		w.Write(make([]byte, 128<<10))
	}))
	defer refuse.Close()
	if _, _, err := measureViaProxy(context.Background(), refuse.URL, target, 8<<20); err == nil || !strings.Contains(err.Error(), "407") {
		t.Fatalf("407 must fail: %v", err)
	}
	// under 64 KiB is no measurement
	tiny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 1000))
	}))
	defer tiny.Close()
	if _, _, err := measureViaProxy(context.Background(), tiny.URL, target, 8<<20); err == nil || !strings.Contains(err.Error(), "too little data") {
		t.Fatalf("1000 bytes must fail with too little data: %v", err)
	}
	// maxBytes stops the read on a longer object
	long := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAtRate(w, r, 4<<20, 256<<10, time.Millisecond)
	}))
	defer long.Close()
	if _, n, err := measureViaProxy(context.Background(), long.URL, target, 1<<20); err != nil || n != 1<<20 {
		t.Fatalf("maxBytes 1 MiB: %d %v", n, err)
	}
	// an endless slow object is read until the deadline, then measured
	endless := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveAtRate(w, r, 1<<30, 64<<10, 20*time.Millisecond)
	}))
	defer endless.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	mbps, n, err := measureViaProxy(ctx, endless.URL, target, 8<<20)
	if err != nil || n < 64<<10 || mbps <= 0 {
		t.Fatalf("deadline: %.2f Mbps, %d bytes, %v", mbps, n, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("deadline not enforced: took %v", took)
	}
}

func TestMeasureEndpointStoresCapacityAndCleansUp(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const mac = "aa:bb:cc:dd:ee:01"
	fb.nodes = []brokerNode{{MAC: mac, RemoteIP: "27.1.1.1", Active: 3}}
	if err := s.devices.Upsert(mac, func(d *Device) error { d.Slot = 5; return nil }); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPEEDTEST_URL", "http://speedtest.invalid/speedtest.bin")

	// the fake gost slot service lets in only what the real slot auther
	// would: a valid user bound to the board holding slot 5
	var mu sync.Mutex
	var seen []string
	fail := false
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := proxyUser(r)
		if ok {
			ok, _ = s.store.Authorize(user, pass, time.Now())
		}
		if ok {
			ok, _ = s.portAllowed(user, "5")
		}
		mu.Lock()
		seen = append(seen, user)
		failing := fail
		mu.Unlock()
		switch {
		case !ok || !isReservedUsername(user) || r.URL.String() != "http://speedtest.invalid/speedtest.bin":
			http.Error(w, "denied", http.StatusProxyAuthRequired)
		case failing:
			http.Error(w, "upstream down", http.StatusBadGateway)
		default:
			serveAtRate(w, r, 1<<20, 128<<10, 5*time.Millisecond)
		}
	}))
	defer proxy.Close()
	slots := useMeasureProxy(t, proxy.URL)

	// a leftover measurement user is never listed
	if err := s.store.Create(&User{Username: "_measure-old", PassHash: hashPassword("x"), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	rr, out := doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("measure: %d %v", rr.Code, out)
	}
	if got := slots(); len(got) != 1 || got[0] != 5 {
		t.Fatalf("proxy asked for slots %v, want [5]", got)
	}
	capMbps, _ := out["capacity_mbps"].(float64)
	if capMbps <= 0 || capMbps != math.Round(capMbps*10)/10 {
		t.Fatalf("capacity_mbps %v: want > 0, rounded to 0.1", out["capacity_mbps"])
	}
	if out["bytes"] != float64(1<<20) || out["active_streams"] != float64(3) {
		t.Fatalf("response: %v", out)
	}
	if secs, _ := out["seconds"].(float64); secs <= 0 {
		t.Fatalf("seconds: %v", out)
	}
	if strings.Contains(rr.Body.String(), "_measure") {
		t.Fatalf("response names the measurement user: %s", rr.Body.String())
	}
	d, err := s.devices.Get(mac)
	if err != nil {
		t.Fatal(err)
	}
	if d.CapacityMbps != capMbps || d.CapacityActiveStreams != 3 || d.Slot != 5 {
		t.Fatalf("stored: %+v", d)
	}
	if at, err := time.Parse(time.RFC3339, d.CapacityMeasuredAt); err != nil || time.Since(at) > time.Minute || at.Location() != time.UTC {
		t.Fatalf("capacity_measured_at %q: %v", d.CapacityMeasuredAt, err)
	}
	mu.Lock()
	user := seen[0]
	mu.Unlock()
	if len(user) != len(measurePrefix)+6 || strings.Trim(user[len(measurePrefix):], "0123456789abcdef") != "" {
		t.Fatalf("measurement user %q: want %s<6hex>", user, measurePrefix)
	}
	if left := measureUsers(s); len(left) != 1 || left[0] != "_measure-old" {
		t.Fatalf("measurement user left behind: %v", left)
	}
	_, list := doJSONList(t, mux, "GET", "/api/users", "admintok")
	for _, u := range list {
		if isReservedUsername(u["username"].(string)) {
			t.Fatalf("GET /api/users lists %v", u["username"])
		}
	}
	// nor counted on the overview, nor named in the device list
	ov := httptest.NewRecorder()
	s.handleOverview(ov, httptest.NewRequest("GET", "/api/overview", nil))
	if !strings.Contains(ov.Body.String(), `"total":0`) {
		t.Fatalf("overview counts a measurement user: %s", ov.Body.String())
	}
	s.store.Update("_measure-old", func(u *User) error { u.DeviceMAC = mac; return nil })
	_, devs := doJSONList(t, mux, "GET", "/api/devices", "admintok")
	if users, _ := devs[0]["users"].([]any); len(devs) != 1 || len(users) != 0 {
		t.Fatalf("device users: %v", devs)
	}

	// a failed measurement answers 502, keeps the stored capacity and still
	// removes its user, and the stale _measure-old now bound to the board
	mu.Lock()
	fail = true
	mu.Unlock()
	fb.mu.Lock()
	fb.nodes = []brokerNode{{MAC: mac, RemoteIP: "27.1.1.1", Active: 7}}
	fb.mu.Unlock()
	rr, out = doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", nil)
	if rr.Code != http.StatusBadGateway || !strings.HasPrefix(out["error"].(string), "measure failed: ") {
		t.Fatalf("failed measure: %d %v", rr.Code, out)
	}
	if d2, _ := s.devices.Get(mac); d2.CapacityMbps != capMbps || d2.CapacityActiveStreams != 3 || d2.CapacityMeasuredAt != d.CapacityMeasuredAt {
		t.Fatalf("failed measure changed the record: %+v", d2)
	}
	if left := measureUsers(s); len(left) != 0 {
		t.Fatalf("measurement user left behind after failure: %v", left)
	}

	// an unreachable broker does not fail the measurement: 0 active streams
	mu.Lock()
	fail = false
	mu.Unlock()
	t.Setenv("BROKER_STATUS_URL", "http://127.0.0.1:1")
	rr, out = doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", nil)
	if rr.Code != http.StatusOK || out["active_streams"] != float64(0) {
		t.Fatalf("measure without broker: %d %v", rr.Code, out)
	}
	if d3, _ := s.devices.Get(mac); d3.CapacityActiveStreams != 0 || d3.CapacityMbps <= 0 {
		t.Fatalf("stored without broker: %+v", d3)
	}
}

func TestMeasureRequiresSlot(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const mac = "aa:bb:cc:dd:ee:02"
	fb.nodes = []brokerNode{{MAC: mac, RemoteIP: "27.1.1.1"}}
	slots := useMeasureProxy(t, "http://127.0.0.1:1")

	rr, out := doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("no device record: %d %v, want 404", rr.Code, out)
	}
	if err := s.devices.Upsert(mac, func(d *Device) error { d.Name = "unslotted"; return nil }); err != nil {
		t.Fatal(err)
	}
	rr, out = doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", nil)
	if rr.Code != http.StatusConflict || out["error"] != "no slot" {
		t.Fatalf("unslotted: %d %v, want 409 no slot", rr.Code, out)
	}
	if got := slots(); len(got) != 0 || len(measureUsers(s)) != 0 {
		t.Fatalf("unslotted device was measured: slots %v, users %v", got, measureUsers(s))
	}
}

func TestMeasureInProgress(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	const mac, other = "aa:bb:cc:dd:ee:03", "aa:bb:cc:dd:ee:04"
	fb.nodes = []brokerNode{{MAC: mac}, {MAC: other}}
	for i, m := range []string{mac, other} {
		if err := s.devices.Upsert(m, func(d *Device) error { d.Slot = i + 1; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	// a download of hold.invalid waits inside the proxy until released
	entered, release := make(chan struct{}, 1), make(chan struct{})
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Host == "hold.invalid" {
			entered <- struct{}{}
			<-release
		}
		serveAtRate(w, r, 128<<10, 64<<10, time.Millisecond)
	}))
	defer proxy.Close()
	useMeasureProxy(t, proxy.URL)
	measure := func(m string) (int, map[string]any) {
		rr, out := doJSON(t, mux, "POST", "/api/devices/"+m+"/measure", "admintok", nil)
		return rr.Code, out
	}

	t.Setenv("SPEEDTEST_URL", "http://hold.invalid/speedtest.bin")
	done := make(chan int, 1)
	go func() {
		code, _ := measure(mac)
		done <- code
	}()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("first measurement never reached the proxy")
	}
	t.Setenv("SPEEDTEST_URL", "http://speedtest.invalid/speedtest.bin")
	code, out := measure(mac)
	otherCode, otherOut := measure(other) // the guard is per device
	close(release)
	first := <-done
	if code != http.StatusConflict || out["error"] != "measurement in progress" {
		t.Fatalf("second measurement of the same device: %d %v", code, out)
	}
	if otherCode != http.StatusOK {
		t.Fatalf("another device during the measurement: %d %v", otherCode, otherOut)
	}
	if first != http.StatusOK {
		t.Fatalf("first measurement: %d", first)
	}
	// the guard is released once the measurement ends
	if code, out := measure(mac); code != http.StatusOK {
		t.Fatalf("measure after the first finished: %d %v", code, out)
	}
	if left := measureUsers(s); len(left) != 0 {
		t.Fatalf("measurement users left behind: %v", left)
	}
}
