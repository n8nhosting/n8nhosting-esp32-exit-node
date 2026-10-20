package main

import (
	"encoding/json"
	"net/http"
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
	Username  string  `json:"username"`
	Password  string  `json:"password"`
	Enabled   *bool   `json:"enabled"`
	ExpiresAt string  `json:"expires_at"`
	RateMbps  float64 `json:"rate_mbps"`
	QuotaGB   float64 `json:"quota_gb"`
	Note      string  `json:"note"`
	// extend: days to add to expiry (used by /extend)
	Days int `json:"days"`
	// quota_gb_add: GB to add to this month's quota headroom (used by /extend)
}

func (s *server) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.store.List())
	case http.MethodPost:
		var in userInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Username == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "username required"})
			return
		}
		if in.Password == "" {
			in.Password = randToken(8)
		}
		enabled := true
		if in.Enabled != nil {
			enabled = *in.Enabled
		}
		u := &User{
			Username:  in.Username,
			Password:  in.Password,
			Enabled:   enabled,
			ExpiresAt: in.ExpiresAt,
			RateMbps:  in.RateMbps,
			QuotaGB:   in.QuotaGB,
			Note:      in.Note,
		}
		if err := s.store.Create(u); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, u)
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
		writeJSON(w, http.StatusOK, u)
	case http.MethodPut:
		var in userInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
			return
		}
		err := s.store.Update(name, func(u *User) error {
			if in.Password != "" {
				u.Password = in.Password
			}
			if in.Enabled != nil {
				u.Enabled = *in.Enabled
			}
			if in.ExpiresAt != "" {
				u.ExpiresAt = in.ExpiresAt
			}
			if in.RateMbps != 0 {
				u.RateMbps = in.RateMbps
			}
			if in.QuotaGB != 0 {
				u.QuotaGB = in.QuotaGB
			}
			if in.Note != "" {
				u.Note = in.Note
			}
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		u, _ := s.store.Get(name)
		writeJSON(w, http.StatusOK, u)
	case http.MethodDelete:
		if err := s.store.Delete(name); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
	}
}

func (s *server) userAction(w http.ResponseWriter, r *http.Request, name, action string) {
	var err error
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
	case "extend":
		var in userInput
		json.NewDecoder(r.Body).Decode(&in)
		days := in.Days
		if days == 0 {
			days = 30
		}
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
	writeJSON(w, http.StatusOK, u)
}
