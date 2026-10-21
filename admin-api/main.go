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
//	  POST   /api/devices/{mac}/measure - capacity through the board's slot
//	         port: SPEEDTEST_URL (default https://flash.jbacteam.com/speedtest.bin)
//	         via gost at GOST_INTERNAL_HOST (default gost) port 3130+slot
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
	"sync"
	"time"
)

type server struct {
	store      *Store
	devices    *DeviceStore
	adminToken string
	payToken   string
	usageMode  string // "delta" (default) or "cumulative"
	// usage accounting state for cumulative mode: last seen totals per client,
	// guarded by lastSeenMu (gost calls the observer concurrently)
	lastSeen   map[string]int64
	lastSeenMu sync.Mutex
	geo        *geoCache
	maps       *mapsResolver // Google Maps share link -> coordinates (maps.go)
	// admitMu holds a capacity check (admit) and the binding it allows
	// together, so two assigns cannot both take the last seat
	admitMu sync.Mutex
	// Serializes complete operator/automatic routing changes across SQLite and
	// broker HTTP, so a stale startup snapshot cannot overwrite an edit.
	routingMu sync.Mutex
	// measureMu guards measuring, the MACs with a capacity measurement
	// running (measure.go); a nil map is an empty set
	measureMu sync.Mutex
	measuring map[string]bool
	telemetry telemetryState
	activity  activityState
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
		devices:    NewDeviceStore(store.DB()),
		adminToken: os.Getenv("ADMIN_TOKEN"),
		payToken:   os.Getenv("PAYMENT_WEBHOOK_SECRET"),
		usageMode:  envOr("USAGE_MODE", "delta"),
		lastSeen:   map[string]int64{},
		geo:        newGeoCache(),
		maps:       newMapsResolver(),
	}
	if s.adminToken == "" {
		log.Fatal("ADMIN_TOKEN is required")
	}
	initCredKey(os.Getenv("CRED_KEY"), s.adminToken)

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
	mux.HandleFunc("/api/metrics", s.requireAdmin(s.handleMetrics))
	mux.HandleFunc("/api/events", s.requireAdmin(s.handleEvents))
	mux.HandleFunc("/api/alerts", s.requireAdmin(s.handleAlerts))
	mux.HandleFunc("/api/users", s.requireAdmin(s.handleUsers))
	mux.HandleFunc("/api/users/", s.requireAdmin(s.handleUser))
	mux.HandleFunc("/api/devices", s.requireAdmin(s.handleDevices))
	mux.HandleFunc("/api/slots", s.requireAdmin(s.handleSlots))
	mux.HandleFunc("/api/devices/", s.requireAdmin(s.handleDeviceAction))
	mux.HandleFunc("/api/geo/resolve", s.requireAdmin(s.handleGeoResolve))
	registerCommerceRoutes(mux, s)
	registerPortalRoutes(mux, s)

	// payment webhook
	mux.HandleFunc("/webhook/payment", s.handlePayment)
	// health
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })

	// Dashboard SPA (static, embedded). Auth happens in-browser with ADMIN_TOKEN.
	mux.Handle("/", dashboardHandler())

	addr := envOr("LISTEN_ADDR", ":8088")
	s.emit("system.started", "info", "system", "", "system", "Admin API started", nil)
	go s.runTelemetry()

	// The existing telemetry sampler also discovers/reserves/reconciles slots
	// on every successful broker poll, including its immediate startup sample.

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
