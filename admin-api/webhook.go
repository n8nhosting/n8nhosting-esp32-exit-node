package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"
)

// Payment webhook - a generic, PSP-agnostic adapter. A payment gateway (or a
// small bridge you run for PromptPay/Stripe) calls this after a successful
// charge to activate or extend a proxy plan.
//
// Auth: either a shared secret in the X-Webhook-Token header, or an HMAC-SHA256
// of the raw body in X-Webhook-Signature using PAYMENT_WEBHOOK_SECRET.
//
// Body:
//   {
//     "username":  "alice",      // required, [A-Za-z0-9._-]{1,64}; created if missing
//     "password":  "optional",   // set when creating a new user
//     "days":      30,           // extend expiry by N days
//     "quota_gb":  50,           // set monthly quota (optional)
//     "rate_mbps": 10            // set rate cap (optional)
//   }

type payReq struct {
	Username string  `json:"username"`
	Password string  `json:"password"`
	Days     int     `json:"days"`
	QuotaGB  float64 `json:"quota_gb"`
	RateMbps float64 `json:"rate_mbps"`
}

func (s *server) handlePayment(w http.ResponseWriter, r *http.Request) {
	if s.payToken == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "payment webhook not configured"})
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))

	if !s.verifyWebhook(r, body) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad signature"})
		return
	}

	var req payReq
	if err := json.Unmarshal(body, &req); err != nil || req.Username == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username required"})
		return
	}
	if !validUsername(req.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid username"})
		return
	}
	// a paid plan must never land on an uncapped, seatless measurement user
	if isReservedUsername(req.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reserved username"})
		return
	}
	days := req.Days
	if days == 0 {
		days = 30
	}

	// Create the user if new, then extend/activate.
	// Reactivation must not take a seat held by a checkout reservation.
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	if cur, err := s.store.Get(req.Username); err == nil && cur.SubscriptionID != "" {
		writeCommerceError(w, commerceFail(409, "use Sales renewal for subscription payments"))
		return
	}
	if cur, err := s.store.Get(req.Username); err == nil && cur.DeviceMAC != "" && !takesSeat(cur, time.Now()) {
		if code, body := s.admit(cur.Username, cur.DeviceMAC); code != 0 {
			writeJSON(w, code, body)
			return
		}
	}
	if _, err := s.store.Get(req.Username); err == ErrNotFound {
		pw := req.Password
		if pw == "" {
			pw = randToken(8)
		}
		_ = s.store.Create(&User{Username: req.Username, PassHash: hashPassword(pw), CredEnc: encryptCred(pw), Enabled: true,
			QuotaGB: req.QuotaGB, RateMbps: req.RateMbps})
	}
	err := s.store.Update(req.Username, func(u *User) error {
		if u.SubscriptionID != "" {
			return commerceFail(409, "use Sales renewal for subscription payments")
		}
		base := time.Now().UTC()
		if u.ExpiresAt != "" {
			if exp, e := time.Parse(time.RFC3339, u.ExpiresAt); e == nil && exp.After(base) {
				base = exp
			}
		}
		u.ExpiresAt = base.AddDate(0, 0, days).Format(time.RFC3339)
		u.Enabled = true
		if req.QuotaGB > 0 {
			u.QuotaGB = req.QuotaGB
		}
		if req.RateMbps > 0 {
			u.RateMbps = req.RateMbps
		}
		return nil
	})
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	log.Printf("payment: activated user=%q +%dd quota=%.0fGB rate=%.0fMbps", req.Username, days, req.QuotaGB, req.RateMbps)
	s.emit("payment.received", "info", "user", req.Username, "webhook", "Payment activated or extended user", map[string]any{"days": days, "quota_gb": req.QuotaGB, "rate_mbps": req.RateMbps})
	u, _ := s.store.Get(req.Username)
	writeJSON(w, http.StatusOK, s.publicUser(u))
}

func (s *server) verifyWebhook(r *http.Request, body []byte) bool {
	if tok := r.Header.Get("X-Webhook-Token"); tok != "" {
		return subtle.ConstantTimeCompare([]byte(tok), []byte(s.payToken)) == 1
	}
	if sig := r.Header.Get("X-Webhook-Signature"); sig != "" {
		mac := hmac.New(sha256.New, []byte(s.payToken))
		mac.Write(body)
		want := hex.EncodeToString(mac.Sum(nil))
		return subtle.ConstantTimeCompare([]byte(sig), []byte(want)) == 1
	}
	return false
}
