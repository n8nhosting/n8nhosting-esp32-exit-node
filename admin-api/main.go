// admin-api - user & billing management for the ESP32 proxy platform, and the
// HTTP plugin backend GOST calls for authentication, rate limiting and traffic
// accounting.
//
// Two groups of endpoints:
//
//	GOST plugins (called by the GOST process, not by humans):
//	  POST /gost/auth      - authenticate a proxy user         -> {ok,id}
//	  POST /gost/limiter   - per-user rate limit               -> {in,out}
//	  POST /gost/observer  - traffic stats for quota accounting -> {ok}
//
//	Admin REST (Bearer ADMIN_TOKEN):
//	  GET    /api/users
//	  POST   /api/users
//	  GET    /api/users/{name}
//	  PUT    /api/users/{name}
//	  DELETE /api/users/{name}
//	  POST   /api/users/{name}/enable | /disable | /reset-quota | /extend
//
//	Payment webhook (shared-secret, generic PSP adapter):
//	  POST /webhook/payment
//
// SPDX-License-Identifier: MIT
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"
)

type server struct {
	store      *Store
	adminToken string
	payToken   string
	usageMode  string // "delta" (default) or "cumulative"
	// usage accounting state for cumulative mode: last seen totals per client
	lastSeen map[string]int64
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)

	dataPath := envOr("DATA_FILE", "/data/users.json")
	store, err := NewStore(dataPath)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	s := &server{
		store:      store,
		adminToken: os.Getenv("ADMIN_TOKEN"),
		payToken:   os.Getenv("PAYMENT_WEBHOOK_SECRET"),
		usageMode:  envOr("USAGE_MODE", "delta"),
		lastSeen:   map[string]int64{},
	}
	if s.adminToken == "" {
		log.Fatal("ADMIN_TOKEN is required")
	}

	mux := http.NewServeMux()

	// GOST plugin endpoints. When GOST_PLUGIN_SECRET is set they live under
	// /gost/<secret>/… so admin-api can be exposed (for the dashboard) without
	// opening the plugin callbacks to the public. gost.yaml must use the same
	// prefix. Empty secret keeps the plain /gost/… paths (internal-only deploys).
	gp := "/gost/"
	if sec := os.Getenv("GOST_PLUGIN_SECRET"); sec != "" {
		gp = "/gost/" + sec + "/"
	}
	mux.HandleFunc(gp+"auth", s.handleAuth)
	mux.HandleFunc(gp+"limiter", s.handleLimiter)
	mux.HandleFunc(gp+"observer", s.handleObserver)

	// admin REST (Bearer ADMIN_TOKEN)
	mux.HandleFunc("/api/overview", s.requireAdmin(s.handleOverview))
	mux.HandleFunc("/api/users", s.requireAdmin(s.handleUsers))
	mux.HandleFunc("/api/users/", s.requireAdmin(s.handleUser))
	mux.HandleFunc("/api/devices", s.requireAdmin(s.handleDevices))
	mux.HandleFunc("/api/devices/", s.requireAdmin(s.handleDeviceAction))

	// payment webhook
	mux.HandleFunc("/webhook/payment", s.handlePayment)
	// health
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Dashboard SPA (static, embedded). Auth happens in-browser with ADMIN_TOKEN.
	mux.Handle("/", dashboardHandler())

	addr := envOr("LISTEN_ADDR", ":8088")

	// Optionally push plugin + service config into GOST on startup.
	if os.Getenv("GOST_API_URL") != "" {
		go func() {
			time.Sleep(2 * time.Second)
			if err := syncGost(); err != nil {
				log.Printf("gost sync: %v (continuing; configure GOST manually)", err)
			} else {
				log.Printf("gost sync: ok")
			}
		}()
	}

	log.Printf("admin-api listening on %s (data=%s, usage=%s)", addr, dataPath, s.usageMode)
	log.Fatal(http.ListenAndServe(addr, logReq(mux)))
}

func logReq(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}
