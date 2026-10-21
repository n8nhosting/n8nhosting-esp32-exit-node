package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func (s *server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer "+s.adminToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

// userInput is the create/update payload.
type userInput struct {
	Username  string   `json:"username"`
	Password  string   `json:"password"`
	Enabled   *bool    `json:"enabled"`
	ExpiresAt string   `json:"expires_at"`
	RateMbps  *float64 `json:"rate_mbps"` // nil = absent; 0 = unlimited
	QuotaGB   *float64 `json:"quota_gb"`  // nil = absent; 0 = unlimited
	Note      string   `json:"note"`
	DeviceMAC string   `json:"device_mac"`
	// extend: days to add to expiry (used by /extend)
	Days int `json:"days"`
	// quota_gb_add: GB to add to this month's quota headroom (used by /extend)
}

func (s *server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		list := s.store.List()
		out := make([]PublicUser, 0, len(list))
		for _, u := range list {
			if isReservedUsername(u.Username) {
				continue // a capacity measurement's throwaway user
			}
			out = append(out, s.publicUser(u))
		}
		writeJSON(w, http.StatusOK, out)
	case http.MethodPost:
		var in userInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username required"})
			return
		}
		if !validUsername(in.Username) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid username"})
			return
		}
		if isReservedUsername(in.Username) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "reserved username"})
			return
		}
		generated := ""
		if in.Password == "" {
			in.Password = randToken(8)
			generated = in.Password
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		rate, quota := 0.0, 0.0
		if in.RateMbps != nil {
			rate = *in.RateMbps
		}
		if in.QuotaGB != nil {
			quota = *in.QuotaGB
		}
		u := &User{
			Username:  in.Username,
			PassHash:  hashPassword(in.Password),
			CredEnc:   encryptCred(in.Password),
			Enabled:   enabled,
			ExpiresAt: in.ExpiresAt,
			RateMbps:  rate,
			QuotaGB:   quota,
			Note:      in.Note,
		}
		if err := s.store.Create(u); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		s.emit("user.created", "info", "user", u.Username, "admin", "User created", nil)
		resp := map[string]any{"user": s.publicUser(u)}
		if generated != "" {
			resp["generated_password"] = generated // shown once; not stored in clear
		}
		writeJSON(w, http.StatusCreated, resp)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
	}
}

func (s *server) handleUser(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/users/")
	parts := strings.Split(rest, "/")
	name := parts[0]
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username required"})
		return
	}

	if action != "" && r.Method == http.MethodPost {
		s.userAction(w, r, name, action)
		return
	}

	switch r.Method {
	case http.MethodGet:
		u, err := s.store.Get(name)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, s.publicUser(u))
	case http.MethodPut:
		var in userInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
			return
		}
		if in.DeviceMAC != "" {
			// a new binding passes the same capacity rule as /assign
			s.admitMu.Lock()
			defer s.admitMu.Unlock()
			if cur, err := s.store.Get(name); err == nil && cur.DeviceMAC != in.DeviceMAC {
				if code, body := s.admit(name, in.DeviceMAC); code != 0 {
					s.emit("user.binding_refused", "warn", "user", name, "admin", "Device binding refused", map[string]any{"mac": in.DeviceMAC, "reason": body})
					writeJSON(w, code, body)
					return
				}
			}
		}
		before, _ := s.store.Get(name)
		err := s.store.Update(name, func(u *User) error {
			if in.Password != "" {
				u.PassHash = hashPassword(in.Password)
				u.CredEnc = encryptCred(in.Password)
			}
			if in.Enabled != nil {
				u.Enabled = *in.Enabled
			}
			if in.ExpiresAt != "" {
				u.ExpiresAt = in.ExpiresAt
			}
			if in.RateMbps != nil {
				u.RateMbps = *in.RateMbps
			}
			if in.QuotaGB != nil {
				u.QuotaGB = *in.QuotaGB
			}
			if in.Note != "" {
				u.Note = in.Note
			}
			if in.DeviceMAC != "" {
				u.DeviceMAC = in.DeviceMAC
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		u, _ := s.store.Get(name)
		s.emit("user.updated", "info", "user", name, "admin", "User settings updated", nil)
		if in.Password != "" {
			s.emit("user.credentials_rotated", "info", "user", name, "admin", "User credentials rotated", nil)
		}
		if before != nil && before.Enabled != u.Enabled {
			kind := "user.disabled"
			if u.Enabled {
				kind = "user.enabled"
			}
			s.emit(kind, "info", "user", name, "admin", "User access changed", nil)
		}
		if before != nil && before.DeviceMAC != u.DeviceMAC {
			s.emit("user.bound", "info", "user", name, "admin", "User bound to device", map[string]any{"mac": u.DeviceMAC})
		}
		writeJSON(w, http.StatusOK, s.publicUser(u))
	case http.MethodDelete:
		if err := s.store.Delete(name); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		s.emit("user.deleted", "info", "user", name, "admin", "User deleted", nil)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
	}
}

func (s *server) userAction(w http.ResponseWriter, r *http.Request, name, action string) {
	var err error
	var eventData any
	switch action {
	case "enable":
		err = s.store.Update(name, func(u *User) error { u.Enabled = true; return nil })
	case "disable":
		err = s.store.Update(name, func(u *User) error { u.Enabled = false; return nil })
	case "reset-quota":
		err = s.store.Update(name, func(u *User) error {
			u.UsageBytes = 0
			u.UsageMonth = monthKey(time.Now())
			return nil
		})
	case "credentials":
		s.handleCredentials(w, r, name)
		return
	case "assign":
		var in userInput
		json.NewDecoder(r.Body).Decode(&in)
		if in.DeviceMAC == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "device_mac required"})
			return
		}
		s.admitMu.Lock()
		defer s.admitMu.Unlock()
		if code, body := s.admit(name, in.DeviceMAC); code != 0 {
			s.emit("user.binding_refused", "warn", "user", name, "admin", "Device binding refused", map[string]any{"mac": in.DeviceMAC, "reason": body})
			writeJSON(w, code, body)
			return
		}
		err = s.store.Update(name, func(u *User) error { u.DeviceMAC = in.DeviceMAC; return nil })
		eventData = map[string]any{"mac": in.DeviceMAC}
	case "unassign":
		err = s.store.Update(name, func(u *User) error { u.DeviceMAC = ""; return nil })
	case "extend":
		var in userInput
		json.NewDecoder(r.Body).Decode(&in)
		days := in.Days
		if days == 0 {
			days = 30
		}
		eventData = map[string]any{"days": days}
		err = s.store.Update(name, func(u *User) error {
			base := time.Now().UTC()
			if u.ExpiresAt != "" {
				if exp, e := time.Parse(time.RFC3339, u.ExpiresAt); e == nil && exp.After(base) {
					base = exp
				}
			}
			u.ExpiresAt = base.AddDate(0, 0, days).Format(time.RFC3339)
			u.Enabled = true
			return nil
		})
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	u, _ := s.store.Get(name)
	eventKinds := map[string]string{"enable": "user.enabled", "disable": "user.disabled", "reset-quota": "user.quota_reset", "assign": "user.bound", "unassign": "user.unbound", "extend": "user.extended"}
	s.emit(eventKinds[action], "info", "user", name, "admin", "User action: "+action, eventData)
	writeJSON(w, http.StatusOK, s.publicUser(u))
}

// handleCredentials returns the connection string for handing to a customer.
// Admin-only (bearer). Decrypts the stored credential; falls back to a reset if
// the record predates reversible storage. A user bound to a board with a slot
// connects on that slot's port; anyone else on the pool port.
func (s *server) handleCredentials(w http.ResponseWriter, r *http.Request, name string) {
	u, err := s.store.Get(name)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	d, err := s.boundDevice(u)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()})
		return
	}
	host := envOr("PROXY_PUBLIC_HOST", "")
	port := envOr("PROXY_PUBLIC_PORT", "3128")
	if d != nil && d.Slot > 0 {
		port = strconv.Itoa(slotPort(d.Slot))
	}
	pw := ""
	if u.CredEnc != "" {
		pw = decryptCred(u.CredEnc)
	}
	reset := r.URL.Query().Get("reset") == "1"
	if pw == "" || reset {
		pw = randToken(8)
		if err := s.store.Update(name, func(x *User) error { x.PassHash = hashPassword(pw); x.CredEnc = encryptCred(pw); return nil }); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credential update failed"})
			return
		}
		s.emit("user.credentials_rotated", "info", "user", name, "admin", "User credentials rotated", nil)
	}
	conn := ""
	if host != "" {
		conn = u.Username + ":" + pw + "@" + host + ":" + port
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"username":   u.Username,
		"password":   pw,
		"host":       host,
		"port":       port,
		"connection": conn,
		"http_url":   "http://" + conn,
		"socks5_url": "socks5h://" + conn,
		"expires_at": u.ExpiresAt,
		"rate_mbps":  u.RateMbps,
		"quota_gb":   u.QuotaGB,
		"usage_gb":   float64(u.UsageBytes) / (1024 * 1024 * 1024),
	})
}
