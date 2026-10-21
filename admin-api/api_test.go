package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*server, *http.ServeMux) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	s := &server{store: st, devices: NewDeviceStore(st.DB()), adminToken: "admintok", payToken: "paytok", usageMode: "delta", lastSeen: map[string]int64{},
		geo:  &geoCache{data: map[string]geoEntry{}, url: "http://127.0.0.1:1/"},
		maps: newMapsResolver()} // tests that resolve links swap in fakeMaps()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/geo/resolve", s.requireAdmin(s.handleGeoResolve))
	mux.HandleFunc("/api/overview", s.requireAdmin(s.handleOverview))
	mux.HandleFunc("/api/metrics", s.requireAdmin(s.handleMetrics))
	mux.HandleFunc("/api/events", s.requireAdmin(s.handleEvents))
	mux.HandleFunc("/api/alerts", s.requireAdmin(s.handleAlerts))
	mux.HandleFunc("/gost/auth", s.handleAuth)
	mux.HandleFunc("/gost/limiter", s.handleLimiter)
	mux.HandleFunc("/gost/observer", s.handleObserver)
	mux.HandleFunc("/api/users", s.requireAdmin(s.handleUsers))
	mux.HandleFunc("/api/users/", s.requireAdmin(s.handleUser))
	mux.HandleFunc("/webhook/payment", s.handlePayment)
	mux.HandleFunc("/api/devices", s.requireAdmin(s.handleDevices))
	mux.HandleFunc("/api/slots", s.requireAdmin(s.handleSlots))
	mux.HandleFunc("/api/devices/", s.requireAdmin(s.handleDeviceAction))
	return s, mux
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path, token string, body any) (*httptest.ResponseRecorder, map[string]any) {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var out map[string]any
	json.Unmarshal(rr.Body.Bytes(), &out)
	return rr, out
}

func TestAdminAuthRequired(t *testing.T) {
	_, mux := newTestServer(t)
	rr, _ := doJSON(t, mux, "GET", "/api/users", "", nil)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rr.Code)
	}
	rr, _ = doJSON(t, mux, "GET", "/api/users", "admintok", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200 with token, got %d", rr.Code)
	}
}

func TestUserLifecycleAndAuthPlugin(t *testing.T) {
	_, mux := newTestServer(t)
	// create user
	rr, _ := doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{
		"username": "alice", "password": "pw1", "rate_mbps": 10, "quota_gb": 1})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d", rr.Code)
	}
	// auth plugin: correct creds -> ok
	rr, out := doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "alice", "password": "pw1", "client": "1.2.3.4:5"})
	if rr.Code != 200 || out["ok"] != true || out["id"] != "alice" {
		t.Fatalf("auth ok failed: %v", out)
	}
	// wrong password -> deny
	_, out = doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "alice", "password": "bad"})
	if out["ok"] != false {
		t.Fatalf("expected deny on bad password: %v", out)
	}
	// disable -> deny
	doJSON(t, mux, "POST", "/api/users/alice/disable", "admintok", nil)
	_, out = doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "alice", "password": "pw1"})
	if out["ok"] != false {
		t.Fatalf("expected deny when disabled")
	}
	// enable -> ok again
	doJSON(t, mux, "POST", "/api/users/alice/enable", "admintok", nil)
	_, out = doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "alice", "password": "pw1"})
	if out["ok"] != true {
		t.Fatalf("expected ok after re-enable")
	}
}

func TestExpiryDenies(t *testing.T) {
	_, mux := newTestServer(t)
	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "bob", "password": "x", "expires_at": past})
	_, out := doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "bob", "password": "x"})
	if out["ok"] != false {
		t.Fatalf("expired user should be denied")
	}
	// extend reactivates
	doJSON(t, mux, "POST", "/api/users/bob/extend", "admintok", map[string]any{"days": 7})
	_, out = doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "bob", "password": "x"})
	if out["ok"] != true {
		t.Fatalf("extended user should be allowed")
	}
}

func TestLimiterPlugin(t *testing.T) {
	_, mux := newTestServer(t)
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "cara", "password": "x", "rate_mbps": 8})
	// 8 Mbps = 1,000,000 bytes/s
	_, out := doJSON(t, mux, "POST", "/gost/limiter", "", map[string]any{"scope": "client", "client": "cara"})
	if int64(out["in"].(float64)) != 1_000_000 || int64(out["out"].(float64)) != 1_000_000 {
		t.Fatalf("rate wrong: %v", out)
	}
	// unknown client -> unlimited (0)
	_, out = doJSON(t, mux, "POST", "/gost/limiter", "", map[string]any{"scope": "client", "client": ""})
	if int64(out["in"].(float64)) != 0 {
		t.Fatalf("empty client should be unlimited")
	}
}

func TestObserverQuotaEnforcement(t *testing.T) {
	s, mux := newTestServer(t)
	// quota 1 GB
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "dan", "password": "x", "quota_gb": 1})
	// report 1.5 GB of traffic via observer (delta mode)
	oneGB := int64(1024 * 1024 * 1024)
	doJSON(t, mux, "POST", "/gost/observer", "", map[string]any{"events": []any{
		map[string]any{"kind": "handler", "service": "s0", "client": "dan", "type": "stats",
			"stats": map[string]any{"inputBytes": oneGB, "outputBytes": oneGB / 2}},
	}})
	// now over quota -> auth denied
	_, out := doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "dan", "password": "x"})
	if out["ok"] != false {
		t.Fatalf("over-quota user should be denied, usage=%d", func() int64 { u, _ := s.store.Get("dan"); return u.UsageBytes }())
	}
	// reset quota -> allowed again
	doJSON(t, mux, "POST", "/api/users/dan/reset-quota", "admintok", nil)
	_, out = doJSON(t, mux, "POST", "/gost/auth", "", map[string]any{"username": "dan", "password": "x"})
	if out["ok"] != true {
		t.Fatalf("after reset should be allowed")
	}
}

func TestObserverCumulativeMode(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "u.json"))
	s := &server{store: st, adminToken: "a", payToken: "p", usageMode: "cumulative", lastSeen: map[string]int64{}}
	st.Create(&User{Username: "e", PassHash: hashPassword("x"), Enabled: true, QuotaGB: 10})
	ev := func(total int64) observerReq {
		return observerReq{Events: []observerEvent{{Kind: "handler", Service: "s0", Client: "e", Type: "stats",
			Stats: &observerStats{InputBytes: uint64(total), OutputBytes: 0}}}}
	}
	post := func(r observerReq) {
		b, _ := json.Marshal(r)
		req := httptest.NewRequest("POST", "/gost/observer", bytes.NewReader(b))
		s.handleObserver(httptest.NewRecorder(), req)
	}
	post(ev(100)) // cumulative 100 -> add 100
	post(ev(250)) // cumulative 250 -> add 150
	post(ev(50))  // counter reset (new conn) -> add 50
	u, _ := st.Get("e")
	if u.UsageBytes != 300 {
		t.Fatalf("cumulative accounting wrong: got %d want 300", u.UsageBytes)
	}
}

// gost calls the observer concurrently (one callback per service), so the
// cumulative-mode last-seen totals must be guarded; run with -race.
func TestObserverCumulativeConcurrent(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "u.json"))
	s := &server{store: st, adminToken: "a", payToken: "p", usageMode: "cumulative", lastSeen: map[string]int64{}}
	const clients, steps = 8, 20
	for i := 0; i < clients; i++ {
		if err := st.Create(&User{Username: fmt.Sprintf("c%d", i), PassHash: "x", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(client string) {
			defer wg.Done()
			for k := 1; k <= steps; k++ { // each callback reports the running total
				b, _ := json.Marshal(observerReq{Events: []observerEvent{{Kind: "handler", Service: "s0", Client: client, Type: "stats",
					Stats: &observerStats{InputBytes: uint64(100 * k)}}}})
				s.handleObserver(httptest.NewRecorder(), httptest.NewRequest("POST", "/gost/observer", bytes.NewReader(b)))
			}
		}(fmt.Sprintf("c%d", i))
	}
	wg.Wait()
	for i := 0; i < clients; i++ {
		if u, err := st.Get(fmt.Sprintf("c%d", i)); err != nil || u.UsageBytes != 100*steps {
			t.Fatalf("c%d usage = %+v %v, want %d", i, u, err, 100*steps)
		}
	}
}

func TestPaymentWebhook(t *testing.T) {
	_, mux := newTestServer(t)
	// bad token
	req := httptest.NewRequest("POST", "/webhook/payment", bytes.NewReader([]byte(`{"username":"frank"}`)))
	req.Header.Set("X-Webhook-Token", "wrong")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("bad token should be 401, got %d", rr.Code)
	}
	// good token creates + activates
	req = httptest.NewRequest("POST", "/webhook/payment", bytes.NewReader([]byte(`{"username":"frank","days":30,"quota_gb":50,"rate_mbps":20}`)))
	req.Header.Set("X-Webhook-Token", "paytok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("payment should succeed, got %d", rr.Code)
	}
	// frank now exists, is enabled, and has the purchased plan
	_, u := doJSON(t, mux, "GET", "/api/users/frank", "admintok", nil)
	if u["username"] != "frank" || u["enabled"] != true || u["quota_gb"].(float64) != 50 {
		t.Fatalf("payment did not activate user correctly: %v", u)
	}
}

func TestLegacyPlaintextMigration(t *testing.T) {
	f := filepath.Join(t.TempDir(), "users.json")
	// v1.0 record shape
	os.WriteFile(f, []byte(`[{"username":"old","password":"Secret1","enabled":true,"usage_month":"2026-10"}]`), 0o600)
	st := openTestStore(t, f)
	if ok, why := st.Authorize("old", "Secret1", time.Now()); !ok {
		t.Fatalf("legacy user should authenticate after migration: %s", why)
	}
	u, _ := st.Get("old")
	if u.LegacyPassword != "" || !strings.HasPrefix(u.PassHash, "pbkdf2$") {
		t.Fatalf("not migrated to hash: %+v", u)
	}
	data, _ := os.ReadFile(f)
	if strings.Contains(string(data), "Secret1") {
		t.Fatal("plaintext password still on disk after migration")
	}
}

func TestCredentialsAndAssign(t *testing.T) {
	initCredKey("", "admintok")
	os.Setenv("PROXY_PUBLIC_HOST", "203.0.113.9")
	os.Setenv("PROXY_PUBLIC_PORT", "3128")
	defer os.Unsetenv("PROXY_PUBLIC_HOST")
	s, mux, fb := newTestServerWithBroker(t)
	// assign needs a board the broker or the devices table knows, with room
	fb.nodes = []brokerNode{{MAC: "aa:bb:cc:dd:ee:ff", RemoteIP: "27.1.1.1"}}
	if err := s.devices.Upsert("aa:bb:cc:dd:ee:ff", func(d *Device) error { d.Kind = "dedicated"; return nil }); err != nil {
		t.Fatal(err)
	}
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "gina", "password": "Pw1", "rate_mbps": 5, "quota_gb": 2})

	// credentials returns the connection string with the real password
	_, out := doJSON(t, mux, "POST", "/api/users/gina/credentials", "admintok", nil)
	if out["password"] != "Pw1" {
		t.Fatalf("expected decrypted password, got %v", out["password"])
	}
	if out["connection"] != "gina:Pw1@203.0.113.9:3128" {
		t.Fatalf("connection string wrong: %v", out["connection"])
	}
	if out["socks5_url"] != "socks5h://gina:Pw1@203.0.113.9:3128" {
		t.Fatalf("socks url wrong: %v", out["socks5_url"])
	}

	// assign to a device, visible on the user
	if rr, out := doJSON(t, mux, "POST", "/api/users/gina/assign", "admintok", map[string]any{"device_mac": "aa:bb:cc:dd:ee:ff"}); rr.Code != http.StatusOK {
		t.Fatalf("assign: %d %v", rr.Code, out)
	}
	_, u := doJSON(t, mux, "GET", "/api/users/gina", "admintok", nil)
	if u["device_mac"] != "aa:bb:cc:dd:ee:ff" {
		t.Fatalf("assign failed: %v", u["device_mac"])
	}
	doJSON(t, mux, "POST", "/api/users/gina/unassign", "admintok", nil)
	_, u2 := doJSON(t, mux, "GET", "/api/users/gina", "admintok", nil)
	if u2["device_mac"] != nil && u2["device_mac"] != "" {
		t.Fatalf("unassign failed: %v", u2["device_mac"])
	}
}

func TestCredEncRoundTrip(t *testing.T) {
	initCredKey("", "k")
	enc := encryptCred("s3cr3t!")
	if enc == "" || enc == "s3cr3t!" {
		t.Fatal("not encrypted")
	}
	if decryptCred(enc) != "s3cr3t!" {
		t.Fatal("roundtrip failed")
	}
	// wrong key cannot decrypt
	initCredKey("", "other")
	if decryptCred(enc) == "s3cr3t!" {
		t.Fatal("decrypted with wrong key")
	}
}

func TestGeoLookupCachesAndDegrades(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(`{"status":"success","country":"Thailand","regionName":"Bangkok","city":"Bangkok","isp":"TRUE","lat":13.7,"lon":100.5}`))
	}))
	defer srv.Close()
	g := &geoCache{data: map[string]geoEntry{}, url: srv.URL + "/"}
	a := g.lookup("1.2.3.4")
	b := g.lookup("1.2.3.4")
	if a.ISP != "TRUE" || a.City != "Bangkok" || b.Country != "Thailand" {
		t.Fatalf("geo parse wrong: %+v", a)
	}
	if calls != 1 {
		t.Fatalf("expected 1 upstream call (cached), got %d", calls)
	}
	bad := &geoCache{data: map[string]geoEntry{}, url: "http://127.0.0.1:1/"}
	if bad.lookup("9.9.9.9").ISP != "" {
		t.Fatal("geo should degrade to empty on error")
	}
}

func TestUpdateDistinguishesAbsentFromZero(t *testing.T) {
	_, mux := newTestServer(t)
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "bob", "password": "pw", "rate_mbps": 20, "quota_gb": 5})
	// absent fields: unchanged
	doJSON(t, mux, "PUT", "/api/users/bob", "admintok", map[string]any{"note": "x"})
	_, u := doJSON(t, mux, "GET", "/api/users/bob", "admintok", nil)
	if u["rate_mbps"] != 20.0 || u["quota_gb"] != 5.0 {
		t.Fatalf("absent must not change: %v", u)
	}
	// explicit zero: unlimited
	doJSON(t, mux, "PUT", "/api/users/bob", "admintok", map[string]any{"rate_mbps": 0, "quota_gb": 0})
	_, u = doJSON(t, mux, "GET", "/api/users/bob", "admintok", nil)
	if u["rate_mbps"] != 0.0 || u["quota_gb"] != 0.0 {
		t.Fatalf("zero must reset: %v", u)
	}
}
