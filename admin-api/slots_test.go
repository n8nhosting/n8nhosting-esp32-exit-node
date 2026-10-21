package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAutoSlotsMigrationAndSafeUniqueConflict(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v5.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_version(version INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := db.Exec(migrations[i]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO schema_version(version) VALUES(?)`, i+1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO devices(mac,name,slot) VALUES('old','Legacy',NULL),('held','Custom',2)`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	ds := NewDeviceStore(db)
	old, err := ds.Get("old")
	if err != nil || !old.SlotAuto || old.Slot != 0 || old.Name != "Legacy" {
		t.Fatalf("legacy migration: %+v %v", old, err)
	}
	if err := ds.Upsert("old", func(d *Device) error { d.Slot = 2; return nil }); err == nil {
		t.Fatal("UNIQUE slot collision must fail")
	}
	held, err := ds.Get("held")
	if err != nil || held.Slot != 2 || held.Name != "Custom" {
		t.Fatalf("conflict deleted/changed holder: %+v %v", held, err)
	}
	old, err = ds.Get("old")
	if err != nil || old.Slot != 0 || old.Name != "Legacy" {
		t.Fatalf("conflict changed original: %+v %v", old, err)
	}
}

func TestAutoSlotsLowestFreeDurableAndBrokerUnion(t *testing.T) {
	s, _, _ := newTestServerWithBroker(t)
	for mac, slot := range map[string]int{"preserved": 1, "offline-record-only": 2} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.Slot, d.Name = slot, "keep name"; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.devices.Upsert("cleared", func(d *Device) error { d.SlotAuto = false; return nil }); err != nil {
		t.Fatal(err)
	}
	// Existing records with no slot are eligible too, as approved for migration.
	if err := s.devices.Upsert("legacy", func(d *Device) error { d.Notes = "keep notes"; return nil }); err != nil {
		t.Fatal(err)
	}
	nodes := []brokerNode{{MAC: "new", Online: true}, {MAC: "legacy"}, {MAC: "broker-held", Slot: 3}, {MAC: "cleared"}, {MAC: "preserved", Slot: 1}}
	reserved, err := s.devices.reserveAutomaticSlots(nodes, 30)
	if err != nil || len(reserved) != 3 {
		t.Fatalf("reserve: %+v %v", reserved, err)
	}
	for mac, want := range map[string]int{"preserved": 1, "offline-record-only": 2, "broker-held": 3, "legacy": 4, "new": 5, "cleared": 0} {
		d, err := s.devices.Get(mac)
		if err != nil || d.Slot != want {
			t.Fatalf("%s: %+v %v, want slot %d", mac, d, err, want)
		}
	}
	d, _ := s.devices.Get("legacy")
	if d.Notes != "keep notes" || !d.SlotAuto {
		t.Fatalf("reservation changed operator data: %+v", d)
	}
	reserved, err = s.devices.reserveAutomaticSlots(nodes, 30)
	if err != nil || len(reserved) != 0 {
		t.Fatalf("repeated reservation: %+v %v", reserved, err)
	}
	if len(s.store.List()) != 0 {
		t.Fatal("reserving a slot must never create/assign a customer")
	}
}

func TestAutoSlotsConcurrentExhaustion(t *testing.T) {
	s, _ := newTestServer(t)
	nodes := []brokerNode{}
	for i := 0; i < 32; i++ {
		nodes = append(nodes, brokerNode{MAC: fmt.Sprintf("board-%02d", i)})
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.devices.reserveAutomaticSlots(nodes, 30)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	records := s.devices.List()
	if len(records) != 30 {
		t.Fatalf("reserved %d records, want 30", len(records))
	}
	seen := map[int]bool{}
	for _, d := range records {
		if d.Slot < 1 || d.Slot > 30 || seen[d.Slot] {
			t.Fatalf("invalid or duplicate slot: %+v", d)
		}
		seen[d.Slot] = true
	}
}

func TestAutoSlotsAdoptBrokerOnlyDedicatedPolicy(t *testing.T) {
	s, _ := newTestServer(t)
	pool := false
	b := newRoutingTestBroker(t, []brokerNode{{MAC: "dedicated", Slot: 6, Pool: &pool}})
	nodes, err := s.brokerNodesWithRouting()
	d, _ := s.devices.Get("dedicated")
	if err != nil || d == nil || d.Slot != 6 || d.Kind != "dedicated" || nodes[0].Pool == nil || *nodes[0].Pool {
		t.Fatalf("broker-only dedicated policy changed: %+v %+v %v", d, nodes, err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls != 0 {
		t.Fatal("adopting broker-only policy must not change its working routing")
	}
}

// Unlike fakeBroker, this fixture persists routing requests in its live state.
type routingTestBroker struct {
	*httptest.Server
	mu      sync.Mutex
	nodes   []brokerNode
	fail    bool
	calls   int
	entered chan struct{}
	release chan struct{}
}

func newRoutingTestBroker(t *testing.T, nodes []brokerNode) *routingTestBroker {
	b := &routingTestBroker{nodes: nodes}
	b.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		if r.Method == "GET" && r.URL.Path == "/nodes" {
			json.NewEncoder(w).Encode(b.nodes)
			return
		}
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/slot") {
			b.calls++
			if b.entered != nil {
				close(b.entered)
				b.entered = nil
				<-b.release
			}
			if b.fail {
				http.Error(w, "temporary failure", 503)
				return
			}
			var body struct {
				Slot int  `json:"slot"`
				Pool bool `json:"pool"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad request", 400)
				return
			}
			mac := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/nodes/"), "/slot")
			for i := range b.nodes {
				if b.nodes[i].MAC == mac {
					b.nodes[i].Slot = body.Slot
					pool := body.Pool
					b.nodes[i].Pool = &pool
					fmt.Fprint(w, `{"ok":true}`)
					return
				}
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(b.Close)
	t.Setenv("BROKER_STATUS_URL", b.URL)
	return b
}

func TestAutoSlotsPendingRetryRestartAndClearOptOut(t *testing.T) {
	s, mux := newTestServer(t)
	const mac = "aa:bb:cc:dd:ee:01"
	b := newRoutingTestBroker(t, []brokerNode{{MAC: mac, Online: true}})
	b.fail = true
	nodes, err := s.brokerNodesWithRouting()
	if err != nil || len(nodes) != 1 || nodes[0].Slot != 0 {
		t.Fatalf("failed broker push: %v %v", nodes, err)
	}
	d, err := s.devices.Get(mac)
	if err != nil || d.Slot != 1 || !d.SlotAuto {
		t.Fatalf("reservation must survive failure: %+v %v", d, err)
	}
	if eventCount(t, s, "device.slot_reserved") != 1 {
		t.Fatal("missing reservation event")
	}
	// A new server instance models restart; the pending slot survives in SQLite.
	restarted := &server{store: s.store, devices: NewDeviceStore(s.store.DB())}
	b.mu.Lock()
	b.fail = false
	b.mu.Unlock()
	nodes, err = restarted.brokerNodesWithRouting()
	if err != nil || nodes[0].Slot != 1 {
		t.Fatalf("retry: %+v %v", nodes, err)
	}
	b.mu.Lock()
	calls := b.calls
	b.mu.Unlock()
	if _, err := restarted.brokerNodesWithRouting(); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	if b.calls != calls {
		t.Error("stable routing must not be pushed repeatedly")
	}
	b.mu.Unlock()
	if eventCount(t, s, "device.slot_reserved") != 1 {
		t.Fatal("retry/restart duplicated reservation event")
	}
	rr, out := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"slot": 0})
	if rr.Code != 200 || out["slot_auto"] != false || out["slot"] != float64(0) {
		t.Fatalf("explicit clear: %d %v", rr.Code, out)
	}
	if _, err := restarted.brokerNodesWithRouting(); err != nil {
		t.Fatal(err)
	}
	d, _ = s.devices.Get(mac)
	if d.Slot != 0 || d.SlotAuto {
		t.Fatal("explicit clear was automatically re-assigned")
	}
	// A later manual positive assignment is preserved on further polls.
	rr, out = doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"slot": 7})
	if rr.Code != 200 || out["slot_auto"] != true {
		t.Fatalf("manual positive: %d %v", rr.Code, out)
	}
	nodes, err = s.brokerNodesWithRouting()
	if err != nil || nodes[0].Slot != 7 {
		t.Fatalf("manual assignment changed: %+v %v", nodes, err)
	}
	// A later registration uses the existing sampler path, not a new ticker.
	b.mu.Lock()
	b.nodes = append(b.nodes, brokerNode{MAC: "future", Online: true})
	b.mu.Unlock()
	if _, err := s.brokerNodesWithRouting(); err != nil {
		t.Fatal(err)
	}
	d, _ = s.devices.Get("future")
	if d == nil || d.Slot != 1 || eventCount(t, s, "device.slot_reserved") != 2 {
		t.Fatalf("future registration: %+v", d)
	}
}

func TestAutoSlotsManualEditCannotBeOverwrittenByPendingPoll(t *testing.T) {
	s, mux := newTestServer(t)
	const mac = "aa:bb:cc:dd:ee:02"
	b := newRoutingTestBroker(t, []brokerNode{{MAC: mac}})
	entered, release := make(chan struct{}), make(chan struct{})
	b.entered, b.release = entered, release
	poll := make(chan error, 1)
	go func() { _, err := s.brokerNodesWithRouting(); poll <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("reservation did not reach broker")
	}
	// Reading SQLite while broker HTTP is blocked proves no transaction/DB
	// connection is held across the network call.
	read := make(chan *Device, 1)
	go func() { d, _ := s.devices.Get(mac); read <- d }()
	select {
	case d := <-read:
		if d == nil || d.Slot != 1 {
			t.Fatal("reservation was not committed before broker HTTP")
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("broker HTTP holds the SQLite connection")
	}
	updated := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr, _ := doJSON(t, mux, "PUT", "/api/devices/"+mac, "admintok", map[string]any{"slot": 4})
		updated <- rr
	}()
	close(release)
	if err := <-poll; err != nil {
		t.Fatal(err)
	}
	if rr := <-updated; rr.Code != 200 {
		t.Fatalf("manual edit: %d %s", rr.Code, rr.Body.String())
	}
	nodes, err := s.brokerNodesWithRouting()
	d, _ := s.devices.Get(mac)
	if err != nil || d.Slot != 4 || nodes[0].Slot != 4 {
		t.Fatalf("stale poll overwrote manual assignment: %+v %+v %v", d, nodes, err)
	}
}

func TestAvailableSlotsIncludesOfflineRecordsAndDoesNotReserve(t *testing.T) {
	s, mux := newTestServer(t)
	newRoutingTestBroker(t, []brokerNode{{MAC: "live", Slot: 1}, {MAC: "broker-only-offline", Slot: 3}})
	for mac, slot := range map[string]int{"live": 1, "missing-offline-record": 2} {
		if err := s.devices.Upsert(mac, func(d *Device) error { d.Slot = slot; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	rr, out := doJSON(t, mux, "GET", "/api/slots?mac=new", "admintok", nil)
	if rr.Code != 200 || out["slot_count"] != float64(30) || out["port_base"] != float64(3130) || out["current_slot"] != float64(0) || out["suggested_slot"] != float64(4) {
		t.Fatalf("slots: %d %v", rr.Code, out)
	}
	available := out["available"].([]any)
	if len(available) != 27 || available[0].(map[string]any)["port"] != float64(3134) {
		t.Fatalf("available: %v", available)
	}
	if len(s.devices.List()) != 2 {
		t.Fatal("GET /api/slots mutated reservations")
	}
	rr, out = doJSON(t, mux, "GET", "/api/slots?mac=live", "admintok", nil)
	if rr.Code != 200 || out["current_slot"] != float64(1) || out["suggested_slot"] != float64(1) {
		t.Fatalf("own reservation must remain selectable: %d %v", rr.Code, out)
	}
	if rr, _ := doJSON(t, mux, "GET", "/api/slots", "", nil); rr.Code != 401 {
		t.Fatal("slots API must require admin auth")
	}
}
