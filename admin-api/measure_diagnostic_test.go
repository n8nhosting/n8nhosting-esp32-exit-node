package main

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasureDiagnosticOptions(t *testing.T) {
	for _, body := range []string{"", `{}`, `{"parallel":1}`, `{"parallel":2,"persist":false}`, `{"parallel":4,"duration_sec":30}`, `{"duration_sec":300}`} {
		if _, err := readMeasureOptions(strings.NewReader(body)); err != nil {
			t.Fatalf("valid %s: %v", body, err)
		}
	}
	for _, body := range []string{`null`, `[]`, `{"parallel":0}`, `{"parallel":3}`, `{"parallel":5}`, `{"parallel":1.5}`, `{"parallel":null}`, `{"persist":null}`, `{"duration_sec":29}`, `{"duration_sec":301}`, `{"duration_sec":-1}`, `{"duration_sec":"30"}`, `{"persist":"false"}`, `{"target":"https://other.invalid"}`, `{"proxy":"http://other.invalid"}`, `{} {}`, `{"parallel":`} {
		if _, err := readMeasureOptions(strings.NewReader(body)); err == nil {
			t.Fatalf("accepted invalid %s", body)
		}
	}
	opts, err := readMeasureOptions(nil)
	if err != nil || opts.Parallel != 1 || opts.DurationSec != 0 || !opts.Persist {
		t.Fatalf("defaults: %+v %v", opts, err)
	}
}

func TestMeasureDiagnosticParallelWorkersAccountBytes(t *testing.T) {
	for _, parallel := range []int{2, 4} {
		t.Run(string(rune('0'+parallel)), func(t *testing.T) {
			var active, maxActive, requests atomic.Int32
			var ready sync.WaitGroup
			ready.Add(parallel)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := active.Add(1)
				defer active.Add(-1)
				requests.Add(1)
				for old := maxActive.Load(); n > old && !maxActive.CompareAndSwap(old, n); old = maxActive.Load() {
				}
				ready.Done()
				ready.Wait()
				serveAtRate(w, r, 256<<10, 64<<10, 5*time.Millisecond)
			}))
			defer proxy.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			result := measureDiagnostic(ctx, proxy.URL, "http://speedtest.invalid/speedtest.bin", parallel, false)
			if !result.Complete || result.Bytes != int64(parallel)*(256<<10) || requests.Load() != int32(parallel) || maxActive.Load() != int32(parallel) {
				t.Fatalf("parallel result %+v, requests %d maxactive %d", result, requests.Load(), maxActive.Load())
			}
			if math.Abs(result.Mbps-float64(result.Bytes)*8/result.Seconds/1e6) > 1e-9 {
				t.Fatalf("aggregate not actual wall rate: %+v", result)
			}
			for i, r := range result.Streams {
				if r.Stream != i+1 || !r.Completed || r.Bytes != 256<<10 || r.Downloads != 1 || r.Error != "" || r.StopReason != "object_complete" {
					t.Fatalf("stream %+v", r)
				}
			}
		})
	}
}

func TestMeasureDiagnosticRepeatedDeadlineAndMinimum(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		serveAtRate(w, r, 128<<10, 64<<10, 5*time.Millisecond)
	}))
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Millisecond)
	defer cancel()
	start := time.Now()
	result := measureDiagnostic(ctx, proxy.URL, "http://speedtest.invalid/speedtest.bin", 2, true)
	if !result.Complete || time.Since(start) > time.Second || requests.Load() <= 2 {
		t.Fatalf("bounded repeat: %+v requests %d", result, requests.Load())
	}
	for _, r := range result.Streams {
		if !r.Completed || r.StopReason != "duration_elapsed" || r.Downloads < 2 || r.Bytes < measureMinBytes {
			t.Fatalf("timed stream %+v", r)
		}
	}
	tiny := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(make([]byte, 1000)) }))
	defer tiny.Close()
	result = measureDiagnostic(context.Background(), tiny.URL, "http://speedtest.invalid/speedtest.bin", 4, false)
	if result.Complete || result.Bytes != 4000 {
		t.Fatalf("tiny responses accepted: %+v", result)
	}
	for _, r := range result.Streams {
		if r.Completed || !strings.Contains(r.Error, "too little data") {
			t.Fatalf("tiny stream %+v", r)
		}
	}
}

func diagnosticFixture(t *testing.T) (*server, *http.ServeMux, string) {
	t.Helper()
	s, mux, fb := newTestServerWithBroker(t)
	const mac = "aa:bb:cc:dd:ee:31"
	fb.nodes = []brokerNode{{MAC: mac, Slot: 1, Active: 2}}
	if err := s.devices.Upsert(mac, func(d *Device) error {
		d.Slot = 1
		d.CapacityMbps = 7.7
		d.CapacityMeasuredAt = "2026-10-20T00:00:00Z"
		d.CapacityActiveStreams = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SPEEDTEST_URL", "http://speedtest.invalid/speedtest.bin")
	return s, mux, mac
}

func TestMeasureDiagnosticEndpointDoesNotPersistOrExposeCredentials(t *testing.T) {
	s, mux, mac := diagnosticFixture(t)
	var mu sync.Mutex
	seen := map[string]bool{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := proxyUser(r)
		if ok {
			ok, _ = s.store.Authorize(user, pass, time.Now())
		}
		if !ok || !isReservedUsername(user) || r.URL.String() != "http://speedtest.invalid/speedtest.bin" {
			http.Error(w, "denied", 407)
			return
		}
		mu.Lock()
		seen[user] = true
		mu.Unlock()
		serveAtRate(w, r, 128<<10, 64<<10, 5*time.Millisecond)
	}))
	defer proxy.Close()
	useMeasureProxy(t, proxy.URL)
	rr, out := doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", map[string]any{"parallel": 4, "persist": false})
	if rr.Code != 200 || out["persisted"] != false || out["all_complete"] != true || out["bytes"] != float64(4*128<<10) || out["parallel"] != float64(4) {
		t.Fatalf("diagnostic: %d %v", rr.Code, out)
	}
	if len(out["streams"].([]any)) != 4 || out["aggregate_mbps"].(float64) <= 0 || out["seconds"].(float64) <= 0 || strings.Contains(rr.Body.String(), "_measure-") {
		t.Fatalf("bad/sensitive result %s", rr.Body.String())
	}
	mu.Lock()
	count := len(seen)
	mu.Unlock()
	if count != 1 {
		t.Fatalf("workers must share one temporary account, got %d", count)
	}
	d, _ := s.devices.Get(mac)
	if d.CapacityMbps != 7.7 || d.CapacityMeasuredAt != "2026-10-20T00:00:00Z" || d.CapacityActiveStreams != 3 {
		t.Fatalf("diagnostic changed capacity %+v", d)
	}
	if len(measureUsers(s)) != 0 || !s.beginMeasure(mac) {
		t.Fatal("temporary user or measurement guard leaked")
	}
	s.endMeasure(mac)
	if eventCount(t, s, "device.measured") != 0 || eventCount(t, s, "device.diagnostic") != 1 {
		t.Fatal("diagnostic must not emit a saved-capacity event")
	}
}

func TestMeasureDiagnosticFailureAndCancelCleanUpAllWorkers(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial_failure", true: "cancel"}[cancelRequest], func(t *testing.T) {
			s, mux, mac := diagnosticFixture(t)
			var requests, active atomic.Int32
			entered := make(chan struct{}, 4)
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				active.Add(1)
				defer active.Add(-1)
				n := requests.Add(1)
				entered <- struct{}{}
				if cancelRequest {
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					return
				}
				if n == 1 {
					http.Error(w, "upstream unavailable", 502)
					return
				}
				serveAtRate(w, r, 128<<10, 64<<10, 5*time.Millisecond)
			}))
			defer proxy.Close()
			useMeasureProxy(t, proxy.URL)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req := httptest.NewRequest("POST", "/api/devices/"+mac+"/measure", strings.NewReader(`{"parallel":4,"duration_sec":30,"persist":false}`)).WithContext(ctx)
			if !cancelRequest {
				req = httptest.NewRequest("POST", "/api/devices/"+mac+"/measure", strings.NewReader(`{"parallel":4,"persist":false}`)).WithContext(ctx)
			}
			req.Header.Set("Authorization", "Bearer admintok")
			rr := httptest.NewRecorder()
			done := make(chan struct{})
			go func() { mux.ServeHTTP(rr, req); close(done) }()
			for i := 0; i < 4; i++ {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("workers did not start")
				}
			}
			if cancelRequest {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("handler did not wait/stop workers")
			}
			var out map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if rr.Code != 502 || out["persisted"] != false || out["all_complete"] != false || len(out["streams"].([]any)) != 4 {
				t.Fatalf("failed diagnostic %d %s", rr.Code, rr.Body.String())
			}
			if !cancelRequest && out["bytes"] != float64(3*128<<10) {
				t.Fatalf("partial bytes lost: %v", out)
			}
			if len(measureUsers(s)) != 0 || !s.beginMeasure(mac) {
				t.Fatal("temporary user/guard leaked after failure")
			}
			s.endMeasure(mac)
			d, _ := s.devices.Get(mac)
			if d.CapacityMbps != 7.7 {
				t.Fatal("failed diagnostic persisted capacity")
			}
			deadline := time.Now().Add(time.Second)
			for active.Load() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if active.Load() != 0 {
				t.Fatal("proxy workers left active")
			}
		})
	}
}

func TestMeasureDiagnosticValidationBeforeTrafficAndExplicitPersistence(t *testing.T) {
	s, mux, mac := diagnosticFixture(t)
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		serveAtRate(w, r, 128<<10, 64<<10, 5*time.Millisecond)
	}))
	defer proxy.Close()
	useMeasureProxy(t, proxy.URL)
	for _, body := range []map[string]any{{"parallel": 3}, {"duration_sec": 301}, {"target": "http://other.invalid"}} {
		rr, _ := doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", body)
		if rr.Code != 400 || requests.Load() != 0 || len(measureUsers(s)) != 0 {
			t.Fatalf("invalid options caused traffic/state: %d %v", rr.Code, body)
		}
	}
	rr, out := doJSON(t, mux, "POST", "/api/devices/"+mac+"/measure", "admintok", map[string]any{"parallel": 2})
	d, _ := s.devices.Get(mac)
	if rr.Code != 200 || out["persisted"] != true || out["all_complete"] != true || d.CapacityMbps != out["capacity_mbps"] || d.CapacityMeasuredAt == "2026-10-20T00:00:00Z" || d.CapacityActiveStreams != 2 {
		t.Fatalf("explicit parallel persistence: %d %v %+v", rr.Code, out, d)
	}
	if eventCount(t, s, "device.measured") != 1 || eventCount(t, s, "device.diagnostic") != 0 || len(measureUsers(s)) != 0 {
		t.Fatal("persistence event or user cleanup mismatch")
	}
}
