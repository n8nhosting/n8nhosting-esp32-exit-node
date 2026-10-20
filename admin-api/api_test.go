package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func newTestServer(t *testing.T) (*server, *http.ServeMux) {
	st, err := NewStore(filepath.Join(t.TempDir(), "users.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := &server{store: st, adminToken: "admintok", payToken: "paytok", usageMode: "delta", lastSeen: map[string]int64{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/gost/auth", s.handleAuth)
	mux.HandleFunc("/gost/limiter", s.handleLimiter)
	mux.HandleFunc("/gost/observer", s.handleObserver)
	mux.HandleFunc("/api/users", s.requireAdmin(s.handleUsers))
	mux.HandleFunc("/api/users/", s.requireAdmin(s.handleUser))
	mux.HandleFunc("/webhook/payment", s.handlePayment)
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
	st, _ := NewStore(filepath.Join(t.TempDir(), "u.json"))
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
