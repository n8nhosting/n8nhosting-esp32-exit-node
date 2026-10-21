package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"
)

const portalAUPVersion = "2026-10-22"
const portalCookie = "__Host-portal_session"

// Fixed provider destinations. Tests substitute a transport, never disable TLS.
var portalHTTPClient = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

type portalIdentity struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}
type portalIdentityKey struct{}

func randomPortalID(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}
func portalHash(raw string) string  { h := sha256.Sum256([]byte(raw)); return hex.EncodeToString(h[:]) }
func portalTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func portalError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
func portalJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 16385))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("invalid body")
	}
	return nil
}
func portalBase() (*url.URL, bool) {
	u, err := url.Parse(os.Getenv("PORTAL_BASE_URL"))
	valid := err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" && (u.Path == "" || u.Path == "/portal" || u.Path == "/portal/")
	if valid {
		u.Path = "/portal/"
	}
	return u, valid
}
func emailConfigured() bool {
	_, err := mail.ParseAddress(os.Getenv("EMAIL_FROM"))
	return strings.TrimSpace(os.Getenv("RESEND_API_KEY")) != "" && err == nil
}
func turnstileConfigured() bool {
	return strings.TrimSpace(os.Getenv("TURNSTILE_SECRET")) != "" && strings.TrimSpace(os.Getenv("TURNSTILE_SITE_KEY")) != ""
}
func stripeConfigured() bool {
	return strings.TrimSpace(os.Getenv("STRIPE_SECRET_KEY")) != "" && strings.TrimSpace(os.Getenv("STRIPE_WEBHOOK_SECRET")) != ""
}
func manualConfigured() bool {
	return strings.TrimSpace(os.Getenv("PAYMENT_BANK_NAME")) != "" && strings.TrimSpace(os.Getenv("PAYMENT_ACCOUNT_NUMBER")) != "" && strings.TrimSpace(os.Getenv("PAYMENT_ACCOUNT_NAME")) != ""
}

func stripeLiveMode() bool {
	key := os.Getenv("STRIPE_SECRET_KEY")
	return strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "rk_live_")
}

// Called by main; no provider request or mail send occurs during registration.
func registerPortalRoutes(mux *http.ServeMux, s *server) {
	registerPortalStatic(mux)
	mux.HandleFunc("GET /portal/api/config", s.handlePortalConfig)
	mux.HandleFunc("POST /portal/api/auth/request", s.handlePortalRequest)
	mux.HandleFunc("POST /portal/api/auth/verify", s.handlePortalVerify)
	mux.HandleFunc("GET /portal/api/me", s.requirePortal(s.handlePortalMe))
	mux.HandleFunc("POST /portal/api/auth/logout", s.requirePortal(s.handlePortalLogout))
	registerPaymentRoutes(mux, s)
	mux.HandleFunc("GET /api/email-log", s.requireAdmin(s.handleEmailLog))
}

func (s *server) handlePortalConfig(w http.ResponseWriter, r *http.Request) {
	base, baseOK := portalBase()
	ready := baseOK && emailConfigured()
	reasons := []string{}
	if !baseOK {
		reasons = append(reasons, "portal_url_not_configured")
	}
	if !emailConfigured() {
		reasons = append(reasons, "email_not_configured")
	}
	if !turnstileConfigured() {
		reasons = append(reasons, "turnstile_not_configured")
	}
	if !stripeConfigured() {
		reasons = append(reasons, "stripe_not_configured")
	}
	if !manualConfigured() {
		reasons = append(reasons, "bank_not_configured")
	}
	publicURL := ""
	if baseOK {
		publicURL = base.String()
	}
	mode := "disabled"
	if stripeConfigured() {
		mode = "test"
		if stripeLiveMode() {
			mode = "live"
		}
	}
	out := map[string]any{"signup_ready": ready && turnstileConfigured(), "login_ready": ready && turnstileConfigured(), "stripe_ready": baseOK && stripeConfigured(), "stripe_mode": mode, "manual_ready": manualConfigured(), "turnstile_site_key": os.Getenv("TURNSTILE_SITE_KEY"), "aup_version": portalAUPVersion, "portal_url": publicURL, "reasons": reasons}
	if manualConfigured() {
		out["bank"] = map[string]string{"name": os.Getenv("PAYMENT_BANK_NAME"), "account_number": os.Getenv("PAYMENT_ACCOUNT_NUMBER"), "account_name": os.Getenv("PAYMENT_ACCOUNT_NAME")}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}

func portalOriginOK(r *http.Request) bool {
	u, ok := portalBase()
	if !ok {
		return false
	}
	return r.Header.Get("Origin") == u.Scheme+"://"+u.Host
}
func portalRemoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, raw := range strings.Split(os.Getenv("PORTAL_TRUSTED_PROXY_CIDRS"), ",") {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(raw))
			if err == nil && prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	// Ignore forwarded client addresses unless the immediate peer was explicitly
	// trusted. Walk right-to-left to stop at the first untrusted hop.
	forwarded := r.Header.Get("X-Forwarded-For")
	if !trusted(peer) || len(forwarded) > 2048 {
		return peer.String()
	}
	parts := strings.Split(forwarded, ",")
	if len(parts) > 32 {
		return peer.String()
	}
	current := peer
	for i := len(parts) - 1; i >= 0; i-- {
		if !trusted(current) {
			return current.String()
		}
		next, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			return peer.String()
		}
		current = next.Unmap()
	}
	return current.String()
}

// Durable fixed windows bound both email spam and token guessing across restart.
func (s *server) portalRate(ctx context.Context, key string, limit int, window time.Duration) bool {
	now := time.Now().Unix()
	start := now - now%int64(window.Seconds())
	allowed := false
	err := inTx(ctx, s.store.DB(), func(c *sql.Conn) error {
		_, err := c.ExecContext(ctx, `INSERT INTO portal_rate_limits(key,window_start,count) VALUES(?,?,1) ON CONFLICT(key) DO UPDATE SET count=CASE WHEN window_start=excluded.window_start THEN count+1 ELSE 1 END,window_start=excluded.window_start`, portalHash(key), start)
		if err != nil {
			return err
		}
		var count int
		if err := c.QueryRowContext(ctx, `SELECT count FROM portal_rate_limits WHERE key=?`, portalHash(key)).Scan(&count); err != nil {
			return err
		}
		allowed = count <= limit
		return nil
	})
	return err == nil && allowed
}

func verifyTurnstile(ctx context.Context, token, ip string) error {
	base, ok := portalBase()
	if !ok || !turnstileConfigured() || len(token) == 0 || len(token) > 2048 {
		return errors.New("verification unavailable")
	}
	form := url.Values{"secret": {os.Getenv("TURNSTILE_SECRET")}, "response": {token}, "remoteip": {ip}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://challenges.cloudflare.com/turnstile/v0/siteverify", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := portalHTTPClient.Do(req)
	if err != nil {
		return errors.New("verification unavailable")
	}
	defer resp.Body.Close()
	var result struct {
		Success  bool   `json:"success"`
		Hostname string `json:"hostname"`
		Action   string `json:"action"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&result) != nil || !result.Success || result.Hostname != base.Hostname() || result.Action != "signup" {
		return errors.New("verification failed")
	}
	return nil
}

func (s *server) handlePortalRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !portalOriginOK(r) {
		portalError(w, 403, "origin rejected")
		return
	}
	if !emailConfigured() || !turnstileConfigured() {
		portalError(w, 503, "signup and login are not configured")
		return
	}
	var in struct {
		Email          string `json:"email"`
		AUPVersion     string `json:"aup_version"`
		TurnstileToken string `json:"turnstile_token"`
	}
	if portalJSON(r, &in) != nil {
		portalError(w, 400, "invalid request")
		return
	}
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	parsed, err := mail.ParseAddress(in.Email)
	if err != nil || parsed.Address != in.Email || len(in.Email) > 254 || in.AUPVersion != portalAUPVersion {
		portalError(w, 400, "valid email and current AUP acceptance required")
		return
	}
	if !s.portalRate(r.Context(), "login-ip:"+portalRemoteIP(r), 20, 15*time.Minute) || !s.portalRate(r.Context(), "login-email:"+in.Email, 5, time.Hour) {
		portalError(w, 429, "please try again later")
		return
	}
	if verifyTurnstile(r.Context(), in.TurnstileToken, portalRemoteIP(r)) != nil {
		portalError(w, 403, "verification failed")
		return
	}
	token := randomPortalID("")
	now := time.Now()
	base, _ := portalBase()
	base.Path = "/portal/"
	base.Fragment = "/verify?token=" + url.QueryEscape(token)
	err = inTx(r.Context(), s.store.DB(), func(c *sql.Conn) error {
		id := randomPortalID("c_")
		// A request alone proves no ownership of the supplied email address.
		// Consent belongs to the token until the recipient verifies that link.
		_, err := c.ExecContext(r.Context(), `INSERT INTO customers(id,email,created_at) VALUES(?,?,?) ON CONFLICT(email) DO NOTHING`, id, in.Email, portalTime(now))
		if err != nil {
			return err
		}
		if err := c.QueryRowContext(r.Context(), `SELECT id FROM customers WHERE email=?`, in.Email).Scan(&id); err != nil {
			return err
		}
		if _, err := c.ExecContext(r.Context(), `INSERT INTO portal_tokens(hash,customer_id,expires_at,used_at,aup_version,aup_accepted_at) VALUES(?,?,?,'',?,?)`, portalHash(token), id, portalTime(now.Add(15*time.Minute)), portalAUPVersion, portalTime(now)); err != nil {
			return err
		}
		return queueEmailConn(r.Context(), c, randomPortalID("mail_"), id, "magic_link", in.Email, "ลิงก์เข้าสู่ระบบ / Sign in", "เข้าสู่ระบบภายใน 15 นาที / Sign in within 15 minutes:\n"+base.String()+"\nหากไม่ได้ร้องขอ โปรดละเว้นอีเมลนี้ / Ignore if you did not request this.")
	})
	if err != nil {
		portalError(w, 503, "login request unavailable")
		return
	}
	writeJSON(w, 202, map[string]any{"ok": true, "message": "หากอีเมลถูกต้อง ระบบจะส่งลิงก์เข้าสู่ระบบ / Check your email for a sign-in link"})
}

func (s *server) handlePortalVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if !portalOriginOK(r) {
		portalError(w, 403, "origin rejected")
		return
	}
	if !s.portalRate(r.Context(), "verify:"+portalRemoteIP(r), 30, 15*time.Minute) {
		portalError(w, 429, "please try again later")
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if portalJSON(r, &in) != nil || len(in.Token) < 32 || len(in.Token) > 128 {
		portalError(w, 400, "invalid or expired link")
		return
	}
	session := randomPortalID("")
	csrf := portalHash("csrf:" + session)
	now := portalTime(time.Now())
	expires := time.Now().Add(7 * 24 * time.Hour)
	var identity portalIdentity
	err := inTx(r.Context(), s.store.DB(), func(c *sql.Conn) error {
		var consentVersion, consentAt string
		if err := c.QueryRowContext(r.Context(), `SELECT c.id,c.email,t.aup_version,t.aup_accepted_at FROM portal_tokens t JOIN customers c ON c.id=t.customer_id WHERE t.hash=? AND t.used_at='' AND t.expires_at>?`, portalHash(in.Token), now).Scan(&identity.ID, &identity.Email, &consentVersion, &consentAt); err != nil {
			return err
		}
		if consentVersion != portalAUPVersion || consentAt == "" {
			return errors.New("current AUP acceptance required")
		}
		if _, err := c.ExecContext(r.Context(), `UPDATE portal_tokens SET used_at=? WHERE hash=?`, now, portalHash(in.Token)); err != nil {
			return err
		}
		if _, err := c.ExecContext(r.Context(), `UPDATE customers SET verified_at=CASE WHEN verified_at='' THEN ? ELSE verified_at END,last_login_at=?,aup_version=?,aup_accepted_at=? WHERE id=?`, now, now, consentVersion, consentAt, identity.ID); err != nil {
			return err
		}
		_, err := c.ExecContext(r.Context(), `INSERT INTO portal_sessions(hash,customer_id,csrf_hash,expires_at) VALUES(?,?,?,?)`, portalHash(session), identity.ID, portalHash(csrf), portalTime(expires))
		return err
	})
	if err != nil {
		portalError(w, 400, "invalid or expired link")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: portalCookie, Value: session, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, Expires: expires, MaxAge: 7 * 24 * 3600})
	writeJSON(w, 200, map[string]any{"customer": identity, "csrf_token": csrf})
}

func (s *server) requirePortal(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		cookie, err := r.Cookie(portalCookie)
		if err != nil || len(cookie.Value) > 128 {
			portalError(w, 401, "sign in required")
			return
		}
		var identity portalIdentity
		var csrf string
		err = s.store.DB().QueryRowContext(r.Context(), `SELECT c.id,c.email,p.csrf_hash FROM portal_sessions p JOIN customers c ON c.id=p.customer_id WHERE p.hash=? AND p.expires_at>? AND c.verified_at<>''`, portalHash(cookie.Value), portalTime(time.Now())).Scan(&identity.ID, &identity.Email, &csrf)
		if err != nil {
			portalError(w, 401, "sign in required")
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			provided := portalHash(r.Header.Get("X-CSRF-Token"))
			if !portalOriginOK(r) || subtle.ConstantTimeCompare([]byte(provided), []byte(csrf)) != 1 {
				portalError(w, 403, "request verification failed")
				return
			}
		}
		next(w, r.WithContext(context.WithValue(r.Context(), portalIdentityKey{}, identity)))
	}
}
func portalCustomer(r *http.Request) portalIdentity {
	v, _ := r.Context().Value(portalIdentityKey{}).(portalIdentity)
	return v
}
func (s *server) handlePortalMe(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(portalCookie)
	writeJSON(w, 200, map[string]any{"customer": portalCustomer(r), "csrf_token": portalHash("csrf:" + cookie.Value)})
}
func (s *server) handlePortalLogout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(portalCookie)
	if _, err := s.store.DB().ExecContext(r.Context(), `DELETE FROM portal_sessions WHERE hash=?`, portalHash(cookie.Value)); err != nil {
		portalError(w, 503, "logout unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: portalCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	writeJSON(w, 200, map[string]bool{"ok": true})
}

func (s *server) prunePortalAuth(ctx context.Context, now time.Time) error {
	for _, q := range []string{`DELETE FROM portal_tokens WHERE expires_at<?`, `DELETE FROM portal_sessions WHERE expires_at<?`} {
		if _, err := s.store.DB().ExecContext(ctx, q, portalTime(now)); err != nil {
			return err
		}
	}
	_, err := s.store.DB().ExecContext(ctx, `DELETE FROM portal_rate_limits WHERE window_start<?`, now.Add(-24*time.Hour).Unix())
	return err
}
