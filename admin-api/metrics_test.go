package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestCounterDeltaResets(t *testing.T) {
	for _, c := range []struct {
		cur, prev int64
		reset     bool
		want      int64
	}{{150, 100, false, 50}, {20, 100, false, 20}, {200, 100, true, 200}, {-1, 100, false, 0}} {
		if got := counterDelta(c.cur, c.prev, c.reset); got != c.want {
			t.Errorf("%+v got %d", c, got)
		}
	}
}
func TestTelemetrySamplerRestartAndUnknownFailure(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	status := brokerSnapshot{Uptime: 100, BytesUp: 1000, BytesDown: 500, NodesOnline: 1, NodesTotal: 1}
	node := brokerNode{MAC: "aa", Online: true, BytesUp: 1000, BytesDown: 500, DialsOK: 20}
	failed := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if failed {
			w.WriteHeader(503)
			fmt.Fprint(w, `{}`)
			return
		}
		if r.URL.Path == "/status" {
			json.NewEncoder(w).Encode(status)
		} else {
			json.NewEncoder(w).Encode([]brokerNode{node})
		}
	}))
	defer srv.Close()
	t.Setenv("BROKER_STATUS_URL", srv.URL)
	s.sampleTelemetry(now)
	if s.telemetry.pending[now.Unix()].up != 0 {
		t.Fatal("first sample attributed lifetime bytes")
	}
	mu.Lock()
	status.Uptime = 160
	status.BytesUp = 1300
	node.BytesUp = 1300
	node.DialsOK = 25
	mu.Unlock()
	s.sampleTelemetry(now.Add(time.Minute))
	if s.telemetry.rateUp != 5 {
		t.Fatalf("bytes/s=%v", s.telemetry.rateUp)
	}
	mu.Lock()
	status.Uptime = 2
	status.BytesUp = 90
	node.BytesUp = 90
	node.DialsOK = 2
	mu.Unlock()
	s.sampleTelemetry(now.Add(2 * time.Minute))
	b := s.telemetry.pending[now.Unix()]
	if b.up != 390 || b.nodesByMAC["aa"].ok != 7 {
		t.Fatalf("restart bucket %+v node %+v", b, b.nodesByMAC["aa"])
	}
	mu.Lock()
	failed = true
	mu.Unlock()
	s.sampleTelemetry(now.Add(3 * time.Minute))
	if b.samples != 3 {
		t.Fatal("failed broker poll added fake offline sample")
	}
	mu.Lock()
	failed = false
	status.Uptime = 200
	mu.Unlock()
	s.sampleTelemetry(now.Add(5 * time.Minute))
	var up int64
	var online float64
	if err := s.store.DB().QueryRow(`SELECT bytes_up,online_frac FROM metrics_node WHERE step='5m' AND mac='aa' AND ts=?`, now.Unix()).Scan(&up, &online); err != nil {
		t.Fatal(err)
	}
	if up != 390 || online != 1 {
		t.Fatalf("up=%d online=%v", up, online)
	}
}
func TestObserverConnectionServicesAndPendingFlush(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	event := func(service string, conns uint64, add int64) {
		s.recordObserver(observerEvent{Service: service, Client: "alice", Stats: &observerStats{CurrentConns: conns}}, add, now)
	}
	event("http", 1, 10)
	event("socks", 0, 20)
	if s.connectedNow(now) != 1 {
		t.Fatal("zero on one service hid another live service")
	}
	s.recordObserver(observerEvent{Service: "http", Client: "_measure-test", Stats: &observerStats{CurrentConns: 2}}, 100, now)
	if s.connectedNow(now) != 1 {
		t.Fatal("measurement client counted")
	}
	if s.connectedNow(now.Add(61*time.Second)) != 0 {
		t.Fatal("stale connection counted")
	}
	// Make writes fail without deleting the pending byte accumulator.
	if _, err := s.store.DB().Exec(`CREATE TRIGGER reject_metric BEFORE INSERT ON metrics_user BEGIN SELECT RAISE(FAIL,'test');END`); err != nil {
		t.Fatal(err)
	}
	s.telemetry.mu.Lock()
	err := s.flushMetricsLocked(now.Add(5 * time.Minute))
	s.telemetry.mu.Unlock()
	if err == nil || s.telemetry.pending[now.Unix()].usersBytes["alice"] != 30 {
		t.Fatal("failed flush lost bytes")
	}
	s.store.DB().Exec(`DROP TRIGGER reject_metric`)
	s.telemetry.mu.Lock()
	err = s.flushMetricsLocked(now.Add(5 * time.Minute))
	s.telemetry.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	var got int64
	if err := s.store.DB().QueryRow(`SELECT bytes FROM metrics_user WHERE username='alice'`).Scan(&got); err != nil || got != 30 {
		t.Fatalf("bytes=%d err=%v", got, err)
	}
}
func TestMetricRollupRetentionBoundary(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Date(2026, 10, 21, 10, 30, 0, 0, time.UTC)
	hour := now.Unix() / 3600 * 3600
	oldHour := hour - 48*3600
	for i := 0; i < 12; i++ {
		_, err := s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'5m','aa',1,10,20,2,1,?)`, oldHour+int64(i*300), i)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.maintainMetrics(now); err != nil {
		t.Fatal(err)
	}
	if err := s.maintainMetrics(now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.maintainMetrics(now.Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var up, max int64
	if err := s.store.DB().QueryRow(`SELECT bytes_up,active_max FROM metrics_node WHERE step='1h' AND ts=?`, oldHour).Scan(&up, &max); err != nil {
		t.Fatal(err)
	}
	if up != 120 || max != 11 {
		t.Fatalf("rollup overwritten across fine retention boundary %d %d", up, max)
	}
	s.store.DB().Exec(`INSERT INTO metrics_user VALUES (?,'1h','old',1)`, now.Add(-91*24*time.Hour).Unix())
	if err := s.maintainMetrics(now); err != nil {
		t.Fatal(err)
	}
	var n int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM metrics_user WHERE username='old'`).Scan(&n)
	if n != 0 {
		t.Fatal("retention failed")
	}
}
func TestMetricsCanonicalQueryAndNoDoubleCounting(t *testing.T) {
	s, mux := newTestServer(t)
	now := time.Date(2026, 10, 21, 10, 30, 0, 0, time.UTC)
	hour := now.Unix() / 3600 * 3600
	s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'1h','aa',1,120,0,2,0,4)`, hour-3600)
	s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'5m','aa',1,10,0,1,0,2)`, hour-3600)
	s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'5m','aa',1,30,0,1,0,1)`, hour)
	call := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.handleMetricsAt(rr, httptest.NewRequest("GET", path, nil), now)
		return rr
	}
	rr := call("/api/metrics?entity=node&id=aa&range=24h&step=1h&fields=bytes_up")
	if rr.Code != 200 || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	var body struct {
		Series  map[string][]*float64
		Summary map[string]struct {
			Last, Prev, DeltaPct *float64 `json:"-"`
		}
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	values := body.Series["bytes_up"]
	if *values[len(values)-2] != 120 || *values[len(values)-1] != 30 {
		t.Fatalf("double counted or missing partial hour: %v", rr.Body)
	}
	for _, q := range []string{"entity=bad", "entity=node", "entity=node&id=aa&fields=nope", "range=7d&step=5m", "fields=bytes_up,bytes_down,nodes_online,nodes_total,mux_links,users_total,users_seated,users_connected,users_expired"} {
		if got := call("/api/metrics?" + q); got.Code != 400 {
			t.Errorf("%s: %d", q, got.Code)
		}
	}
	rr = call("/api/metrics?entity=node&id=unknown&fields=bytes_up")
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	series := out["series"].(map[string]any)["bytes_up"].([]any)
	for _, v := range series {
		if v != nil {
			t.Fatal("unknown id should have null history")
		}
	}
	rr = call("/api/metrics?entity=nodes&range=24h&fields=bytes_up")
	json.Unmarshal(rr.Body.Bytes(), &out)
	if out["totals"].(map[string]any)["aa"].(map[string]any)["bytes_up"] != float64(150) {
		t.Fatal(rr.Body.String())
	}
	unauthorized := httptest.NewRecorder()
	mux.ServeHTTP(unauthorized, httptest.NewRequest("GET", "/api/metrics", nil))
	if unauthorized.Code != 401 {
		t.Fatal("metrics requires admin")
	}
}
func TestHealthWeightsMissingStatsAndResourceThresholds(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Now()
	v := deviceView{brokerNode: brokerNode{MAC: "aa", Online: true, MaxStreams: 10, DialsOK: 90, DialsErr: 10}, Status: "online"}
	initial := s.healthFor(v, now)
	if initial.Availability24h != nil || initial.DialSuccess24h != nil || initial.Score != 100 {
		t.Fatalf("lifetime counters mislabelled as 24h history: %+v", initial)
	}
	if _, err := s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'5m','aa',1,0,0,90,10,0)`, now.Add(-5*time.Minute).Unix()/300*300); err != nil {
		t.Fatal(err)
	}
	base := s.healthFor(v, now)
	if base.Score != 96 {
		t.Fatalf("missing stats renormalization %+v", base)
	}
	v.Stats = &nodeStats{At: now.UTC().Format(time.RFC3339), HeapFree: 64 * 1024, HeapTotal: 128 * 1024, CPUPct: 60}
	h := s.healthFor(v, now)
	if h.Score != 97 || h.HeapHeadroom == nil || h.CPULoad == nil {
		t.Fatalf("healthy %+v", h)
	}
	v.Stats.HeapFree = 8 * 1024
	v.Stats.CPUPct = 100
	h = s.healthFor(v, now)
	if h.Score != 77 {
		t.Fatalf("resource zero boundary %+v", h)
	}
	v.Stats.CPUPct = -1
	h = s.healthFor(v, now)
	if h.CPULoad != nil || h.Score != 86 {
		t.Fatalf("unknown CPU must renormalize %+v", h)
	}
	v.Stats.At = now.Add(-121 * time.Second).Format(time.RFC3339)
	h = s.healthFor(v, now)
	if h.HeapHeadroom != nil || h.CPULoad != nil || h.Score != base.Score {
		t.Fatalf("stale stats counted %+v", h)
	}
	v.Stats.At = now.Format(time.RFC3339)
	v.Stats.HeapFree = v.Stats.HeapTotal + 1
	v.Stats.CPUPct = 101
	h = s.healthFor(v, now)
	if h.HeapHeadroom != nil || h.CPULoad != nil || h.Score != base.Score {
		t.Fatalf("invalid telemetry must be excluded: %+v", h)
	}
	for _, status := range []string{"offline", "updating", "blocked"} {
		v.Status = status
		h = s.healthFor(v, now)
		limit := 30
		if status == "blocked" {
			limit = 0
		}
		if h.Score > limit {
			t.Fatal("status cap failed", h)
		}
	}
}
func TestDeviceStatusPrecedenceAndC3Capacity(t *testing.T) {
	now := time.Now()
	v := deviceView{brokerNode: brokerNode{Online: true, Blocked: true, OTAStatus: "sent", OTAAt: now.Format(time.RFC3339)}, Kind: "shared"}
	want := func(expected string) {
		t.Helper()
		if got, _ := deviceStatus(v, now); got != expected {
			t.Fatalf("got %q want %q", got, expected)
		}
	}
	want("blocked")
	v.Blocked = false
	want("updating")
	v.OTAAt = now.Add(time.Hour).Format(time.RFC3339)
	want("ota_failed")
	v.Online = false
	v.OTAAt = now.Add(-16 * time.Minute).Format(time.RFC3339)
	want("offline")
	_, tags := deviceStatus(v, now)
	if len(tags) != 1 || tags[0] != "ota_stalled" {
		t.Fatal(tags)
	}
	v.Online = true
	v.OTAStatus = "failed"
	want("ota_failed")
	v.OTAStatus = ""
	want("unmeasured")
	v.CapacityMbps = 2.6
	want("online")
	for _, c := range []struct {
		mbps float64
		want int
	}{{0, 0}, {.01, 1}, {2.6, 1}, {4.99, 1}, {5, 1}, {10, 2}, {15, 3}, {100, 3}} {
		if got := maxUsers("shared", c.mbps); got != c.want {
			t.Errorf("%v got%d want%d", c.mbps, got, c.want)
		}
	}
	if maxUsers("dedicated", 0) != 1 {
		t.Fatal("dedicated changed")
	}
}
func TestSeatAvailabilityPerBoardEvenOnSameIP(t *testing.T) {
	a := deviceView{brokerNode: brokerNode{MAC: "a", Online: true}, Slot: 1, Kind: "shared", IPGroup: "same", CapacityMbps: 10, MaxUsers: 2, Seats: 1, Status: "online"}
	b := a
	b.MAC = "b"
	b.Slot = 2
	b.Seats = 0
	b.MaxUsers = 3
	if n := availableSeats([]deviceView{a, b}); n != 4 {
		t.Fatalf("independent free board seats were not summed: %d", n)
	}
	b.Kind = "dedicated"
	b.MaxUsers = 1
	b.Seats = 1
	if n := availableSeats([]deviceView{a, b}); n != 1 {
		t.Fatal("dedicated occupancy affected another board", n)
	}
	a.IPGroup = ""
	b.IPGroup = ""
	b.Kind = "shared"
	b.MaxUsers = 1
	b.Seats = 0
	if n := availableSeats([]deviceView{a, b}); n != 2 {
		t.Fatal("unknown IP groups collapsed", n)
	}
}

func TestSeatAvailabilityRequiresEligibleRoutableBoard(t *testing.T) {
	base := deviceView{brokerNode: brokerNode{MAC: "a", Online: true}, Slot: 1, Kind: "shared", CapacityMbps: 10, MaxUsers: 2, Seats: 1, Status: "online"}
	for _, tc := range []struct {
		name string
		edit func(*deviceView)
		want int
	}{
		{"normal", func(v *deviceView) {}, 1},
		{"offline", func(v *deviceView) { v.Online = false }, 0},
		{"blocked", func(v *deviceView) { v.Blocked = true }, 0},
		{"updating", func(v *deviceView) { v.Status = "updating" }, 0},
		{"no slot", func(v *deviceView) { v.Slot = 0 }, 0},
		{"routing mismatch", func(v *deviceView) { v.SlotMismatch = true }, 0},
		{"unmeasured", func(v *deviceView) { v.CapacityMbps = 0 }, 0},
		{"full", func(v *deviceView) { v.Seats = 2 }, 0},
		{"overbooked", func(v *deviceView) { v.Seats = 3 }, 0},
		{"dedicated without measurement", func(v *deviceView) { v.Kind = "dedicated"; v.MaxUsers = 1; v.Seats = 0; v.CapacityMbps = 0 }, 1},
		{"measured C3", func(v *deviceView) { v.CapacityMbps = 2.6; v.MaxUsers = 1; v.Seats = 0 }, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := base
			tc.edit(&v)
			if got := availableSeats([]deviceView{v}); got != tc.want {
				t.Fatalf("available=%d want%d", got, tc.want)
			}
		})
	}
}
func TestUserStatusMonthlyProjection(t *testing.T) {
	now := time.Now()
	u := &User{Enabled: true, QuotaGB: 1, UsageBytes: 2 * 1024 * 1024 * 1024, UsageMonth: monthKey(now)}
	if userStatus(u, now) != "over_quota" {
		t.Fatal("quota")
	}
	u.UsageMonth = "2000-01"
	if userStatus(u, now) != "active" || u.UsageBytes == 0 {
		t.Fatal("projection must roll without mutating store data")
	}
	u.ExpiresAt = now.Add(-time.Hour).Format(time.RFC3339)
	if userStatus(u, now) != "expired" {
		t.Fatal("expiry")
	}
	u.Enabled = false
	if userStatus(u, now) != "disabled" {
		t.Fatal("disabled precedence")
	}
}
func TestNullableCoordinatePairCompatibility(t *testing.T) {
	s, mux, _ := newTestServerWithBroker(t)
	mac := "aa:bb:cc:dd:ee:ff"
	for _, body := range []map[string]any{{"lat": 1.0, "lon": 2.0}, {"lat": 3.0}} {
		rr, _ := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", body)
		if rr.Code != 200 {
			t.Fatal(rr.Code, rr.Body)
		}
	}
	d, _ := s.devices.Get(mac)
	if *d.Lat != 3 || *d.Lon != 2 {
		t.Fatal("partial numeric update regressed")
	}
	rr, _ := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"lat": nil})
	if rr.Code != 400 {
		t.Fatal("partial null should fail")
	}
	rr, _ = doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"lat": nil, "lon": nil})
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body)
	}
	d, _ = s.devices.Get(mac)
	if d.Lat != nil || d.Lon != nil {
		t.Fatal("coordinate clear failed")
	}
}

func TestSamplerSerializesOverlappingTicks(t *testing.T) {
	s, _ := newTestServer(t)
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	finished := make(chan struct{}, 2)
	var mu sync.Mutex
	active, maxActive := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		active++
		if active > maxActive {
			maxActive = active
		}
		mu.Unlock()
		defer func() { mu.Lock(); active--; mu.Unlock() }()
		if r.URL.Path == "/status" {
			entered <- struct{}{}
			<-release
			fmt.Fprint(w, `{"uptime_sec":10}`)
		} else {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer srv.Close()
	t.Setenv("BROKER_STATUS_URL", srv.URL)
	now := time.Now()
	go func() { s.sampleTelemetry(now); finished <- struct{}{} }()
	<-entered
	go func() { s.sampleTelemetry(now.Add(time.Minute)); finished <- struct{}{} }()
	select {
	case <-entered:
		t.Fatal("sampler calls overlapped")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	<-finished
	<-finished
	mu.Lock()
	defer mu.Unlock()
	if maxActive != 1 {
		t.Fatalf("max simultaneous requests %d", maxActive)
	}
}
func TestOverviewSeatAndStatusContracts(t *testing.T) {
	s, mux, fb := newTestServerWithBroker(t)
	now := time.Now()
	fb.nodes = []brokerNode{{MAC: "a", Online: true, RemoteIP: "same", Slot: 1}, {MAC: "b", Online: true, RemoteIP: "same", Slot: 2}}
	for i, mac := range []string{"a", "b"} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.CapacityMbps = 15; d.Slot = i + 1; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.store.Create(&User{Username: "one", Enabled: true, DeviceMAC: "a", ExpiresAt: now.Add(48 * time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := s.store.Create(&User{Username: "disabled", Enabled: false, DeviceMAC: "b"}); err != nil {
		t.Fatal(err)
	}
	rr, out := doJSON(t, mux, "GET", "/api/overview", "admintok", nil)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body)
	}
	seats := out["seats"].(map[string]any)
	if seats["occupied"] != float64(1) || seats["available"] != float64(5) {
		t.Fatalf("same IP seats %+v", seats)
	}
	users := out["users"].(map[string]any)
	if users["expiring_7d"] != float64(1) || users["status_counts"].(map[string]any)["disabled"] != float64(1) {
		t.Fatal(users)
	}
	views := s.buildDeviceViews(fb.nodes)
	if views[0].NextExpiry == "" {
		t.Fatal("next expiry missing")
	}
}
func TestDailyGaugeAggregationAndUserSparseCoverage(t *testing.T) {
	fields := []metricField{{"online_frac", "avg"}}
	// One previous complete hour and two fine samples of the current hour
	// must have equal per-hour gauge weight in a daily display.
	rows := []metricRow{{ts: 0, step: "1h", values: []float64{1}}, {ts: 3600, step: "5m", values: []float64{0}}, {ts: 3900, step: "5m", values: []float64{0}}}
	a := aggregateMetricRows(rows, fields, 86400)
	if got := a[0][0].value("avg"); got != .5 {
		t.Fatal("unequal hourly gauge weighting", got)
	}
	s, _ := newTestServer(t)
	now := time.Now().UTC().Truncate(time.Hour).Add(30 * time.Minute)
	ts := now.Unix()/300*300 - 300
	if err := s.store.Create(&User{Username: "idle", Enabled: true, CreatedAt: now.Add(-time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.DB().Exec(`INSERT INTO metrics_fleet VALUES (?,'5m',1,1,1,1,1,0,0,0,0,0,0,0)`, ts); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	s.handleMetricsAt(rr, httptest.NewRequest("GET", "/api/metrics?entity=user&id=idle&fields=bytes", nil), now)
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	v := out["series"].(map[string]any)["bytes"].([]any)
	zeros := 0
	nulls := 0
	for _, n := range v {
		if n == nil {
			nulls++
		} else if n == float64(0) {
			zeros++
		}
	}
	if zeros != 1 || nulls == 0 {
		t.Fatalf("coverage should zero just observed period: %s", rr.Body)
	}
	summary := out["summary"].(map[string]any)["bytes"].(map[string]any)
	if summary["last"] != float64(0) {
		t.Fatal("idle latest complete window must be zero", summary)
	}
}

func TestMaintenanceRollsUnprocessedFineRowsAfterLongDowntime(t *testing.T) {
	s, _ := newTestServer(t)
	now := time.Now().UTC().Truncate(time.Hour)
	unrolled := now.Add(-72 * time.Hour).Unix()
	existing := now.Add(-80 * time.Hour).Unix()
	for i := 0; i < 12; i++ {
		if _, err := s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'5m','aa',1,10,0,1,0,1)`, unrolled+int64(i*300)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.store.DB().Exec(`INSERT INTO metrics_node VALUES (?,'1h','aa',1,321,0,1,0,1)`, existing); err != nil {
		t.Fatal(err)
	}
	if err := s.maintainMetrics(now); err != nil {
		t.Fatal(err)
	}
	for ts, want := range map[int64]int64{unrolled: 120, existing: 321} {
		var got int64
		if err := s.store.DB().QueryRow(`SELECT bytes_up FROM metrics_node WHERE ts=? AND step='1h' AND mac='aa'`, ts).Scan(&got); err != nil || got != want {
			t.Fatalf("%d got%d want%d err%v", ts, got, want, err)
		}
	}
	var fine int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM metrics_node WHERE step='5m'`).Scan(&fine)
	if fine != 0 {
		t.Fatal("old fine rows not pruned")
	}
}
