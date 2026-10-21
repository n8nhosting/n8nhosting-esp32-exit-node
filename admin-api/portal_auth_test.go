package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type portalTestTransport func(*http.Request) (*http.Response, error)

func (fn portalTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func portalTestResponse(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func portalTestClient(t *testing.T, fn portalTestTransport) {
	t.Helper()
	old := portalHTTPClient
	portalHTTPClient = &http.Client{Transport: fn}
	t.Cleanup(func() { portalHTTPClient = old })
}
func portalTestServer(t *testing.T) (*server, *http.ServeMux) {
	t.Helper()
	for _, key := range []string{"RESEND_API_KEY", "EMAIL_FROM", "TURNSTILE_SECRET", "TURNSTILE_SITE_KEY", "STRIPE_SECRET_KEY", "STRIPE_WEBHOOK_SECRET", "PAYMENT_BANK_NAME", "PAYMENT_ACCOUNT_NUMBER", "PAYMENT_ACCOUNT_NAME"} {
		t.Setenv(key, "")
	}
	t.Setenv("PORTAL_BASE_URL", "https://portal.test/portal/")
	t.Setenv("SLIP_STORAGE_DIR", t.TempDir())
	initCredKey("", "portal-test")
	st := openTestStore(t, filepath.Join(t.TempDir(), "users.json"))
	s := &server{store: st, devices: NewDeviceStore(st.DB()), adminToken: "admin-test"}
	mux := http.NewServeMux()
	registerPortalRoutes(mux, s)
	registerCommerceRoutes(mux, s)
	return s, mux
}
func configurePortalTest(t *testing.T) {
	t.Helper()
	t.Setenv("RESEND_API_KEY", "re_secret_fixture")
	t.Setenv("EMAIL_FROM", "Proxy <noreply@example.test>")
	t.Setenv("TURNSTILE_SECRET", "turn_secret_fixture")
	t.Setenv("TURNSTILE_SITE_KEY", "public-site-key")
}
func portalRequest(mux http.Handler, method, path, body string, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://portal.test"+path, strings.NewReader(body))
	req.RemoteAddr = "192.0.2.10:1234"
	req.Header.Set("Origin", "https://portal.test")
	req.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}
func portalSQL(t *testing.T, s *server, query string, args ...any) {
	t.Helper()
	if _, err := s.store.DB().Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func portalFixtureIdentity(t *testing.T, s *server, id string) (*http.Cookie, string) {
	t.Helper()
	now := portalTime(time.Now())
	portalSQL(t, s, `INSERT INTO customers(id,email,verified_at,aup_version,aup_accepted_at,created_at) VALUES(?,?,?,?,?,?)`, id, id+"@example.test", now, portalAUPVersion, now, now)
	raw := randomPortalID("")
	csrf := portalHash("csrf:" + raw)
	portalSQL(t, s, `INSERT INTO portal_sessions(hash,customer_id,csrf_hash,expires_at) VALUES(?,?,?,?)`, portalHash(raw), id, portalHash(csrf), portalTime(time.Now().Add(time.Hour)))
	return &http.Cookie{Name: portalCookie, Value: raw}, csrf
}

func TestPortalUnconfiguredFailsClosedWithoutSecrets(t *testing.T) {
	s, mux := portalTestServer(t)
	calls := 0
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("must not call providers")
	})
	rr := portalRequest(mux, "GET", "/portal/api/config", "", nil, "")
	var config map[string]any
	json.Unmarshal(rr.Body.Bytes(), &config)
	for _, k := range []string{"signup_ready", "login_ready", "stripe_ready", "manual_ready"} {
		if config[k] != false {
			t.Fatalf("unconfigured %s: %s", k, rr.Body.String())
		}
	}
	rr = portalRequest(mux, "POST", "/portal/api/auth/request", `{"email":"a@example.test","aup_version":"2026-10-22","turnstile_token":"x"}`, nil, "")
	if rr.Code != 503 || calls != 0 {
		t.Fatalf("failclosed: %d calls%d", rr.Code, calls)
	}
	var n int
	s.store.DB().QueryRow(`SELECT count(*) FROM customers`).Scan(&n)
	if n != 0 {
		t.Fatal("created unverified customer while providers unavailable")
	}
	t.Setenv("PORTAL_BASE_URL", "https://user:secret@portal.test/")
	rr = portalRequest(mux, "GET", "/portal/api/config", "", nil, "")
	if strings.Contains(rr.Body.String(), "secret") {
		t.Fatal("invalid configured URL leaked credentials")
	}
}

func TestPortalSignupMagicLinkSingleUseSessionAndCSRF(t *testing.T) {
	s, mux := portalTestServer(t)
	configurePortalTest(t)
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "challenges.cloudflare.com" {
			t.Fatalf("unexpected provider %s", r.URL)
		}
		r.ParseForm()
		if r.Form.Get("secret") != "turn_secret_fixture" || r.Form.Get("response") != "valid-turnstile" {
			t.Fatal("siteverify form")
		}
		return portalTestResponse(200, `{"success":true,"hostname":"portal.test","action":"signup"}`), nil
	})
	rr := portalRequest(mux, "POST", "/portal/api/auth/request", `{"email":"Owner@Example.test","aup_version":"2026-10-22","turnstile_token":"valid-turnstile"}`, nil, "")
	if rr.Code != 202 || strings.Contains(rr.Body.String(), "token") {
		t.Fatalf("signup: %d %s", rr.Code, rr.Body.String())
	}
	var customer, storedHash, encrypted string
	if err := s.store.DB().QueryRow(`SELECT customer_id,hash FROM portal_tokens`).Scan(&customer, &storedHash); err != nil {
		t.Fatal(err)
	}
	if err := s.store.DB().QueryRow(`SELECT body_enc FROM email_outbox`).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	var beforeVersion, beforeAccepted string
	if err := s.store.DB().QueryRow(`SELECT aup_version,aup_accepted_at FROM customers WHERE id=?`, customer).Scan(&beforeVersion, &beforeAccepted); err != nil {
		t.Fatal(err)
	}
	if beforeVersion != "" || beforeAccepted != "" {
		t.Fatal("unverified signup recorded consent")
	}
	text := decryptCred(encrypted)
	start := strings.Index(text, "https://")
	end := strings.Index(text[start:], "\n")
	link, err := url.Parse(text[start : start+end])
	if err != nil {
		t.Fatal(err)
	}
	_, query, _ := strings.Cut(link.Fragment, "?")
	values, _ := url.ParseQuery(query)
	token := values.Get("token")
	if link.RawQuery != "" || token == "" || storedHash != portalHash(token) || strings.Contains(encrypted, token) {
		t.Fatalf("token confidentiality: %s", link)
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- portalRequest(mux, "POST", "/portal/api/auth/verify", `{"token":"`+token+`"}`, nil, "")
		}()
	}
	wg.Wait()
	close(responses)
	var successful *httptest.ResponseRecorder
	successes, failures := 0, 0
	for response := range responses {
		if response.Code == 200 {
			successful = response
			successes++
		} else if response.Code == 400 {
			failures++
		} else {
			t.Fatalf("verify status %d %s", response.Code, response.Body.String())
		}
	}
	if successes != 1 || failures != 1 {
		t.Fatalf("singleuse successes%d failures%d", successes, failures)
	}
	cookie := successful.Result().Cookies()[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("cookie: %+v", cookie)
	}
	var out struct {
		Customer portalIdentity `json:"customer"`
		CSRF     string         `json:"csrf_token"`
	}
	json.Unmarshal(successful.Body.Bytes(), &out)
	if out.Customer.ID != customer || out.Customer.Email != "owner@example.test" || out.CSRF == "" {
		t.Fatalf("identity: %+v", out)
	}
	rr = portalRequest(mux, "GET", "/portal/api/me", "", cookie, "")
	if rr.Code != 200 || strings.Contains(rr.Body.String(), cookie.Value) {
		t.Fatalf("me: %d %s", rr.Code, rr.Body.String())
	}
	rr = portalRequest(mux, "POST", "/portal/api/auth/logout", `{}`, cookie, "")
	if rr.Code != 403 {
		t.Fatalf("missingcsrf: %d", rr.Code)
	}
	req := httptest.NewRequest("POST", "https://portal.test/portal/api/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set("Origin", "https://evil.test")
	req.Header.Set("X-CSRF-Token", out.CSRF)
	evil := httptest.NewRecorder()
	mux.ServeHTTP(evil, req)
	if evil.Code != 403 {
		t.Fatal("crossorigin logout allowed")
	}
	rr = portalRequest(mux, "POST", "/portal/api/auth/logout", `{}`, cookie, out.CSRF)
	if rr.Code != 200 {
		t.Fatalf("logout: %d %s", rr.Code, rr.Body.String())
	}
	rr = portalRequest(mux, "GET", "/portal/api/me", "", cookie, "")
	if rr.Code != 401 {
		t.Fatal("revoked session still works")
	}
}

func TestPortalTurnstileStrictHostnameActionAndAUP(t *testing.T) {
	for _, response := range []string{`{"success":false}`, `{"success":true,"hostname":"evil.test","action":"signup"}`, `{"success":true,"hostname":"portal.test","action":"login"}`, `not-json`} {
		t.Run(response, func(t *testing.T) {
			s, mux := portalTestServer(t)
			configurePortalTest(t)
			portalTestClient(t, func(*http.Request) (*http.Response, error) { return portalTestResponse(200, response), nil })
			rr := portalRequest(mux, "POST", "/portal/api/auth/request", `{"email":"new@example.test","aup_version":"2026-10-22","turnstile_token":"x"}`, nil, "")
			var n int
			s.store.DB().QueryRow(`SELECT count(*) FROM customers`).Scan(&n)
			if rr.Code != 403 || n != 0 {
				t.Fatalf("badchallenge accepted: %d customers%d", rr.Code, n)
			}
		})
	}
}

func TestPortalExpiredTokensSessionsAndDurableRateLimits(t *testing.T) {
	s, mux := portalTestServer(t)
	cookie, csrf := portalFixtureIdentity(t, s, "expired")
	portalSQL(t, s, `UPDATE portal_sessions SET expires_at=?`, portalTime(time.Now().Add(-time.Second)))
	if rr := portalRequest(mux, "POST", "/portal/api/auth/logout", `{}`, cookie, csrf); rr.Code != 401 {
		t.Fatal("expired session allowed")
	}
	token := randomPortalID("")
	portalSQL(t, s, `INSERT INTO portal_tokens(hash,customer_id,expires_at) VALUES(?,?,?)`, portalHash(token), "expired", portalTime(time.Now().Add(-time.Second)))
	if rr := portalRequest(mux, "POST", "/portal/api/auth/verify", `{"token":"`+token+`"}`, nil, ""); rr.Code != 400 {
		t.Fatal("expired link allowed")
	}
	wg := sync.WaitGroup{}
	var mu sync.Mutex
	accepted := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.portalRate(context.Background(), "same-email", 5, time.Hour) {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if accepted != 5 {
		t.Fatalf("limit raced: %d", accepted)
	}
	restarted := &server{store: s.store}
	if restarted.portalRate(context.Background(), "same-email", 5, time.Hour) {
		t.Fatal("restart reset rate limit")
	}
	if err := s.prunePortalAuth(context.Background(), time.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	s.store.DB().QueryRow(`SELECT count(*) FROM portal_tokens`).Scan(&n)
	if n != 0 {
		t.Fatal("expired auth retention failed")
	}
}

func TestPortalClientIPTrustBoundary(t *testing.T) {
	req := httptest.NewRequest("POST", "https://portal.test/", nil)
	req.RemoteAddr = "10.0.0.2:80"
	req.Header.Set("X-Forwarded-For", "203.0.113.99, 192.0.2.10")
	t.Setenv("PORTAL_TRUSTED_PROXY_CIDRS", "")
	if got := portalRemoteIP(req); got != "10.0.0.2" {
		t.Fatalf("untrusted forwarding used: %s", got)
	}
	t.Setenv("PORTAL_TRUSTED_PROXY_CIDRS", "10.0.0.0/8")
	if got := portalRemoteIP(req); got != "192.0.2.10" {
		t.Fatalf("client spoofed trusted forwarding: %s", got)
	}
	req.Header.Set("X-Forwarded-For", "192.0.2.10,10.0.0.3")
	if got := portalRemoteIP(req); got != "192.0.2.10" {
		t.Fatalf("trusted chain not followed: %s", got)
	}
	req.Header.Set("X-Forwarded-For", "malformed")
	if got := portalRemoteIP(req); got != "10.0.0.2" {
		t.Fatalf("malformed forwarding trusted: %s", got)
	}
}

func TestPortalConfiguredBaseNormalizesPortalPath(t *testing.T) {
	_, mux := portalTestServer(t)
	for _, base := range []string{"https://portal.test", "https://portal.test/portal", "https://portal.test/portal/"} {
		t.Setenv("PORTAL_BASE_URL", base)
		rr := portalRequest(mux, "GET", "/portal/api/config", "", nil, "")
		var result struct {
			URL string `json:"portal_url"`
		}
		if json.Unmarshal(rr.Body.Bytes(), &result) != nil || result.URL != "https://portal.test/portal/" {
			t.Fatalf("noncanonical base %q: %s", base, rr.Body.String())
		}
	}
}

func TestPortalEmailRequestCannotChangeVerifiedCustomerConsent(t *testing.T) {
	s, mux := portalTestServer(t)
	portalFixtureIdentity(t, s, "buyer")
	configurePortalTest(t)
	previous := portalTime(time.Now().Add(-30 * 24 * time.Hour))
	portalSQL(t, s, `UPDATE customers SET aup_version='previous-version',aup_accepted_at=? WHERE id='buyer'`, previous)
	portalTestClient(t, func(*http.Request) (*http.Response, error) {
		return portalTestResponse(200, `{"success":true,"hostname":"portal.test","action":"signup"}`), nil
	})
	rr := portalRequest(mux, "POST", "/portal/api/auth/request", `{"email":"buyer@example.test","aup_version":"2026-10-22","turnstile_token":"valid"}`, nil, "")
	if rr.Code != 202 {
		t.Fatalf("request %d %s", rr.Code, rr.Body.String())
	}
	var version, accepted, encrypted string
	s.store.DB().QueryRow(`SELECT aup_version,aup_accepted_at FROM customers WHERE id='buyer'`).Scan(&version, &accepted)
	if version != "previous-version" || accepted != previous {
		t.Fatal("third-party request changed existing verified consent")
	}
	if err := s.store.DB().QueryRow(`SELECT body_enc FROM email_outbox WHERE kind='magic_link'`).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	text := decryptCred(encrypted)
	start := strings.Index(text, "https://")
	if start < 0 {
		t.Fatal("missing magic link")
	}
	end := strings.Index(text[start:], "\n")
	link, err := url.Parse(text[start : start+end])
	if err != nil {
		t.Fatal(err)
	}
	_, query, _ := strings.Cut(link.Fragment, "?")
	values, _ := url.ParseQuery(query)
	token := values.Get("token")
	rr = portalRequest(mux, "POST", "/portal/api/auth/verify", `{"token":"`+token+`"}`, nil, "")
	if rr.Code != 200 {
		t.Fatalf("verify %d %s", rr.Code, rr.Body.String())
	}
	s.store.DB().QueryRow(`SELECT aup_version,aup_accepted_at FROM customers WHERE id='buyer'`).Scan(&version, &accepted)
	if version != portalAUPVersion || accepted == previous || accepted == "" {
		t.Fatal("verified owner consent not applied")
	}
}
