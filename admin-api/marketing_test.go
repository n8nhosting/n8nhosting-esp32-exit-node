package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func marketingRequest(mux http.Handler, method, path, body string, admin bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://portal.test"+path, strings.NewReader(body))
	if admin {
		r.Header.Set("Authorization", "Bearer admin-test")
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}
func TestMarketingValidationAndPersistence(t *testing.T) {
	s, mux := portalTestServer(t)
	if rr := marketingRequest(mux, "GET", "/api/marketing", "", false); rr.Code != 401 {
		t.Fatal(rr.Code)
	}
	before := marketingRequest(mux, "GET", "/portal/api/marketing", "", false)
	if !strings.Contains(before.Body.String(), `"enabled":false`) || strings.Contains(before.Body.String(), "updated_at") {
		t.Fatal(before.Body.String())
	}
	m := MarketingSettings{Enabled: true, Analytics: MarketingAnalytics{GA4ID: "G-TEST1234"}, SEO: map[string]map[string]MarketingSEO{"en": {"plans": {Title: `Thailand <proxy> "plans"`, Description: "Choose a measured plan", Keywords: "Thailand proxy, SOCKS5 Thailand"}}}}
	raw, _ := json.Marshal(m)
	rr := marketingRequest(mux, "PUT", "/api/marketing", string(raw), true)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	var saved MarketingSettings
	json.Unmarshal(rr.Body.Bytes(), &saved)
	loaded, err := s.marketingSettings(context.Background())
	if err != nil || loaded.UpdatedAt == "" || loaded.Analytics.GA4ID != "G-TEST1234" {
		t.Fatal(loaded, err)
	}
	// Close the original store, then read using a fresh process-equivalent handle.
	var seq int
	var name, path string
	if err = s.store.DB().QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = s.store.DB().Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fresh.DB().Close() })
	s.store = fresh
	restarted, err := s.marketingSettings(context.Background())
	if err != nil || restarted.Analytics.GA4ID != loaded.Analytics.GA4ID || restarted.UpdatedAt != loaded.UpdatedAt {
		t.Fatal("settings lost after restart", restarted, err)
	}
	public := marketingRequest(mux, "GET", "/portal/api/marketing", "", false)
	if public.Code != 200 || !strings.Contains(public.Body.String(), "G-TEST1234") || public.Body.String() == before.Body.String() {
		t.Fatal(public.Body.String())
	}
	// A stale editor cannot silently overwrite an operator's newer configuration.
	rr = marketingRequest(mux, "PUT", "/api/marketing", string(raw), true)
	if rr.Code != 409 {
		t.Fatal(rr.Code)
	}
	saved.Enabled = false
	raw, _ = json.Marshal(saved)
	rr = marketingRequest(mux, "PUT", "/api/marketing", string(raw), true)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	public = marketingRequest(mux, "GET", "/portal/api/marketing", "", false)
	if strings.Contains(public.Body.String(), "G-TEST1234") {
		t.Fatal("disabled ID leaked", public.Body.String())
	}
	var events int
	s.store.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind='marketing.updated' AND data NOT LIKE '%G-TEST1234%'`).Scan(&events)
	if events != 2 {
		t.Fatal(events)
	}
	for _, body := range []string{`{"enabled":true}`, `{"analytics":{"ga4_id":"<script>alert(1)</script>"}}`, `{"analytics":{"ga4_id":"G-ABCD","gtm_id":"GTM-ABCD"}}`, `{"analytics":{"google_ads_label":"label"}}`, `{"analytics":{"meta_pixel_id":"https://example.org"}}`, `{"seo":{"xx":{"plans":{"title":"x"}}}}`, `{"seo":{"en":{"orders":{"title":"x"}}}}`, `{"script":"alert(1)"}`, `{} garbage`, "{\"seo\":{\"th\":{\"plans\":{\"description\":\"" + strings.Repeat("ก", 321) + "\"}}}}"} {
		if rr := marketingRequest(mux, "PUT", "/api/marketing", body, true); rr.Code != 400 {
			t.Fatalf("accepted %s: %d %s", body, rr.Code, rr.Body.String())
		}
	}
}
func TestMarketingV7MigrationAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v7.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, m := range migrations[:7] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`CREATE TABLE schema_version(version INTEGER PRIMARY KEY);INSERT INTO schema_version VALUES(7);INSERT INTO users(username,enabled) VALUES('existing',1)`); err != nil {
		t.Fatal(err)
	}
	original := migrations[7]
	migrations[7] = original + ` INSERT INTO nonexistent VALUES(1);`
	err = migrate(db)
	migrations[7] = original
	if err == nil {
		t.Fatal("migration unexpectedly committed")
	}
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='marketing_settings'`).Scan(&n)
	if n != 0 {
		t.Fatal("partial migration survived")
	}
	if err = migrate(db); err != nil {
		t.Fatal(err)
	}
	var version, enabled int
	db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version)
	db.QueryRow(`SELECT enabled FROM users WHERE username='existing'`).Scan(&enabled)
	if version != 8 || enabled != 1 {
		t.Fatal(version, enabled)
	}
	var cfg string
	if err = db.QueryRow(`SELECT config FROM marketing_settings WHERE id=1`).Scan(&cfg); err != nil || !strings.Contains(cfg, `"enabled":false`) {
		t.Fatal(cfg, err)
	}
}
func TestPortalLocalizedSEOPrivacyAndDiscovery(t *testing.T) {
	s, mux := portalTestServer(t)
	// Override plain-text metadata with markup. It must remain text in HTML.
	m := MarketingSettings{Enabled: true, Analytics: MarketingAnalytics{GA4ID: "G-TEST1234"}, SEO: map[string]map[string]MarketingSEO{"en": {"plans": {Title: `Proxy </title><script>alert(1)</script>`, Description: `Quote " & <tag>`, Keywords: "Thailand proxy"}}}}
	raw, _ := json.Marshal(m)
	s.store.DB().Exec(`UPDATE marketing_settings SET config=?`, string(raw))
	for _, locale := range portalLocales {
		for _, page := range portalPublicPages {
			dict := portalDictionary(locale)
			for _, field := range []string{"title", "description", "keywords", "h1", "intro", "body", "faq.question", "faq.answer"} {
				if strings.TrimSpace(dict["seo."+page+"."+field]) == "" {
					t.Fatalf("missing %s seo.%s.%s", locale, page, field)
				}
			}
			path := "/portal/" + locale + "/"
			if page != "home" {
				path += page
			}
			rr := marketingRequest(mux, "GET", path, "", false)
			body := rr.Body.String()
			if rr.Code != 200 || rr.Header().Get("Content-Language") != locale || !strings.Contains(body, `<html lang="`+locale+`"`) || !strings.Contains(body, `href="https://portal.test`+path+`"`) || strings.Count(body, `hreflang=`) < 7 || !strings.Contains(body, `og:image`) || !strings.Contains(body, `id="publicHeading"`) {
				t.Fatalf("%s %d %s", path, rr.Code, body)
			}
			if !strings.Contains(rr.Header().Get("Content-Security-Policy"), "www.googletagmanager.com") {
				t.Fatal("enabled public CSP")
			}
			if !strings.Contains(body, html.EscapeString(dict["seo."+page+".body"])) || !strings.Contains(body, html.EscapeString(dict["seo."+page+".faq.answer"])) || strings.Index(body, `id="publicContent"`) > strings.Index(body, `id="page"`) {
				t.Fatalf("missing stable SSR body/FAQ %s", path)
			}
		}
	}
	legacy := marketingRequest(mux, "GET", "/portal/", "", false)
	if strings.Contains(legacy.Header().Get("Content-Security-Policy"), "googletagmanager") {
		t.Fatal("legacy hash document permits tracking")
	}
	rr := marketingRequest(mux, "GET", "/portal/en/plans", "", false)
	if strings.Contains(rr.Body.String(), `</title><script>alert(1)</script>`) || !strings.Contains(rr.Body.String(), `&lt;script&gt;`) {
		t.Fatal("unescaped SEO")
	}
	for _, path := range []string{"/portal/en/account", "/portal/ja/orders", "/portal/ru/verify?token=private", "/portal/en/?session_id=private"} {
		rr := marketingRequest(mux, "GET", path, "", false)
		if rr.Code != 200 || !strings.Contains(rr.Header().Get("X-Robots-Tag"), "noindex") || strings.Contains(rr.Header().Get("Content-Security-Policy"), "googletagmanager") || strings.Contains(rr.Body.String(), `rel="canonical"`) {
			t.Fatalf("private response %s: %d %s", path, rr.Code, rr.Header())
		}
	}
	rr = marketingRequest(mux, "GET", "/portal/sitemap.xml", "", false)
	if rr.Code != 200 || strings.Count(rr.Body.String(), "<loc>") != 18 || strings.Contains(rr.Body.String(), "account") {
		t.Fatal(rr.Body.String())
	}
	rr = marketingRequest(mux, "GET", "/robots.txt", "", false)
	if !strings.Contains(rr.Body.String(), "Sitemap: https://portal.test/portal/sitemap.xml") || !strings.Contains(rr.Body.String(), "Disallow: /portal/api/") {
		t.Fatal(rr.Body.String())
	}
	rr = marketingRequest(mux, "GET", "/portal/invalid/plans", "", false)
	if rr.Code != 404 {
		t.Fatal(rr.Code)
	}
	t.Setenv("PORTAL_BASE_URL", "")
	req := httptest.NewRequest("GET", "https://evil.test/portal/en/", nil)
	req.Header.Set("X-Forwarded-Host", "evil.test")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") || strings.Contains(w.Body.String(), "evil.test") || strings.Contains(w.Body.String(), `rel="canonical"`) {
		t.Fatal("unsafe fallback origin")
	}
	rr = marketingRequest(mux, "GET", "/portal/sitemap.xml", "", false)
	if rr.Code != 503 {
		t.Fatal(rr.Code)
	}
}

func TestMarketingCorruptConfigFailsClosed(t *testing.T) {
	s, mux := portalTestServer(t)
	for _, raw := range []string{`not-json`, `{"enabled":true,"analytics":{"ga4_id":"<script>"}}`, `{"enabled":true,"analytics":{}}`} {
		if _, err := s.store.DB().Exec(`UPDATE marketing_settings SET config=?`, raw); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/portal/api/marketing", "/portal/en/plans"} {
			rr := marketingRequest(mux, "GET", path, "", false)
			if rr.Code != 503 || strings.Contains(rr.Header().Get("Content-Security-Policy"), "googletagmanager") {
				t.Fatal(path, rr.Code, rr.Header())
			}
		}
	}
	for _, path := range []string{"/", "/index.html", "/missing"} {
		r := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		dashboardHandler().ServeHTTP(w, r)
		if !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
			t.Fatal(path, "dashboard indexable")
		}
	}
	w := httptest.NewRecorder()
	logReq(mux).ServeHTTP(w, httptest.NewRequest("GET", "/portal/api/me", nil))
	if !strings.Contains(w.Header().Get("X-Robots-Tag"), "noindex") {
		t.Fatal("private API indexable")
	}
}

func TestPortalPrivateMetadataUsesLocale(t *testing.T) {
	_, mux := portalTestServer(t)
	for _, locale := range portalLocales {
		dict := portalDictionary(locale)
		for page, key := range map[string]string{"account": "ui.my.plans", "subscriptions": "ui.my.plans", "orders": "ui.orders", "login": "ui.sign.in", "verify": "ui.sign.in", "checkout": "ui.sign.in"} {
			rr := marketingRequest(mux, "GET", "/portal/"+locale+"/"+page, "", false)
			expected := "<title>" + html.EscapeString(dict[key]+" | Proxy Server") + "</title>"
			if rr.Code != 200 || dict[key] == "" || !strings.Contains(rr.Body.String(), expected) || !strings.Contains(rr.Body.String(), html.EscapeString(dict["seo.home.description"])) {
				t.Fatalf("%s/%s missing localized private metadata: %d", locale, page, rr.Code)
			}
			if !strings.Contains(rr.Header().Get("X-Robots-Tag"), "noindex") || strings.Contains(rr.Body.String(), `rel="canonical"`) || strings.Contains(rr.Body.String(), `property="og:`) {
				t.Fatalf("%s/%s lost private indexing boundary", locale, page)
			}
		}
	}
}
