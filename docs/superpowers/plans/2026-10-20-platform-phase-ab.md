# Proxy Seller Platform — Phases A+B Implementation Plan

> Status: Tasks 1–11 implemented on branch feat/platform-ab; Task 12 steps 2–5 pending operator deploy.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make device capacity, user↔device binding and rate limits real: measured per-IP capacity with the 2–3-per-shared-IP / 1-per-dedicated rule, customers routed through the board they are bound to via per-slot ports, device fields (lat/long, operator-entered WiFi password), SQLite storage, and the two known bugs fixed.

**Architecture:** admin-api (Go, SQLite via modernc, embedded static dashboard) owns devices/users/capacity and tells the broker which slot maps to which board; gost gets one service per slot (`:3131`–`:3160`) whose chain authenticates to the broker's SOCKS5 as `slot-N`; the broker routes `slot-N` streams only onto that board's mux link. Phases C–E (plans, Stripe, signup, portal, email) are separate plans and build on the tables and endpoints defined here.

**Tech Stack:** Go 1.24 (`go.mod` floors), `modernc.org/sqlite` (no cgo), gost v3 YAML + sh generator, static HTML/JS dashboard (no framework), docker compose on Coolify.

**Spec:** `docs/superpowers/specs/2026-10-20-proxy-seller-platform-design.md`

## Global Constraints

- Go `1.24.7` floors in both `go.mod` files; no cgo (Coolify builds in `golang:alpine`-style images). Only new dependency: `modernc.org/sqlite`.
- Existing JSON shapes consumed by the dashboard (`PublicUser`, `deviceView`, `/api/overview`) keep every current field; add fields, never rename.
- Errors stay `{"error":"<short code or message>"}` with the status codes the code already uses (400 bad input, 401, 404, 409 conflict, 502 broker unreachable).
- Secrets (`SLOT_SECRET`, WiFi passwords, `CRED_KEY`, tokens) are never logged; WiFi passwords are AES-GCM with `CRED_KEY` exactly like `encryptCred`.
- Slot N ↔ port `3130+N`, `SLOT_COUNT` default `30` (ports `3131`–`3160`); the shared pool stays on gost `:8080` → public `3128`.
- Capacity rule (spec §6.1): `max_shared_users = min(3, floor(capacity_mbps/5))`; dedicated → `1`; unmeasured capacity → no new bindings on a shared IP.
- Dashboard: keep the current look and vanilla JS; new user-visible strings go through `T(key)` (`i18n` map, English values now).
- Firmware is not changed in this plan.

## Review Focus

1. `PUT /api/users/{u}` with a body that omits `rate_mbps`/`quota_gb` must leave them unchanged, while `0` must set unlimited — Task 1.
2. Observer bursts (many `/gost/observer` POSTs per second) must not surface `database is locked` — Task 3.
3. A SOCKS5 client sending `slot-N` with a wrong password, or a slot with no device, must get a clean SOCKS reply (0x04/0x05), never a hang or a fallback to another board — Task 8.
4. Binding a user to a shared device whose capacity was never measured must be refused with 409 — Task 9.
5. A measurement taken while customers are active is a lower bound; the stored result must record the active-stream count at measure time so the operator can tell — Task 10.

---

## File map

| File | Responsibility |
|---|---|
| `admin-api/admin.go` | user REST; pointer semantics for optional numeric fields |
| `admin-api/db.go` (new) | open SQLite, schema migrations (`schema_version`), busy timeout |
| `admin-api/store.go` | `Store` methods reimplemented on SQLite; `User`/`PublicUser` unchanged |
| `admin-api/store_import.go` (new) | one-time import of legacy `users.json` |
| `admin-api/devices_store.go` (new) | `Device` row CRUD, capacity rule helpers |
| `admin-api/devices.go` | merge stored device fields + ip groups into `deviceView`; device PUT/reveal/slot/measure |
| `admin-api/measure.go` (new) | speed measurement through a slot port |
| `admin-api/gost_plugins.go` | slot-aware auth (`?slot=N`), capacity-capped limiter |
| `admin-api/dashboard/index.html` | device edit modal, ports, capacity, `T()` |
| `broker/mux.go`, `broker/main.go`, `broker/registry.go` | OTA on live link; SOCKS5 user/pass; slot routing; `/nodes/{mac}/slot` |
| `deploy/gost/gen-config.sh` (new), `deploy/gost/Dockerfile`, `deploy/gost/gost.yaml` | slot services/authers/chains generated at container start |
| `deploy/docker-compose.yml`, `deploy/.env.example` | slot ports, `SLOT_COUNT`, `SLOT_SECRET`, `SPEEDTEST_URL` |
| `flasher/Dockerfile`, `flasher/nginx.conf` | `speedtest.bin` (8 MiB) |

---

## Phase A — foundation

### Task 1: Optional numeric fields on user update (rate/quota 0 = unlimited)

**Files:**
- Modify: `admin-api/admin.go:21-33` (`userInput`), `admin-api/admin.go:116-135` (PUT branch), `admin-api/admin.go:52-70` (POST branch)
- Test: `admin-api/api_test.go`

**Interfaces:**
- Produces: `userInput.RateMbps *float64`, `userInput.QuotaGB *float64` (nil = absent). POST treats nil as 0.

- [ ] **Step 1: Write the failing test**

```go
func TestUpdateDistinguishesAbsentFromZero(t *testing.T) {
	_, mux := newTestServer(t)
	doJSON(t, mux, "POST", "/api/users", "admintok", map[string]any{"username": "bob", "password": "pw", "rate_mbps": 20, "quota_gb": 5})
	// absent fields: unchanged
	doJSON(t, mux, "PUT", "/api/users/bob", "admintok", map[string]any{"note": "x"})
	_, u := doJSON(t, mux, "GET", "/api/users/bob", "admintok", nil)
	if u["rate_mbps"] != 20.0 || u["quota_gb"] != 5.0 { t.Fatalf("absent must not change: %v", u) }
	// explicit zero: unlimited
	doJSON(t, mux, "PUT", "/api/users/bob", "admintok", map[string]any{"rate_mbps": 0, "quota_gb": 0})
	_, u = doJSON(t, mux, "GET", "/api/users/bob", "admintok", nil)
	if u["rate_mbps"] != 0.0 || u["quota_gb"] != 0.0 { t.Fatalf("zero must reset: %v", u) }
}
```

- [ ] **Step 2: Run it** — `cd admin-api && go test -run TestUpdateDistinguishesAbsentFromZero ./...` → FAIL ("zero must reset").
- [ ] **Step 3: Change `userInput.RateMbps`/`QuotaGB` to `*float64`; PUT sets when non-nil; POST derefs with 0 default.** Keep `Days int` and the rest as is.
- [ ] **Step 4: Run** `go test ./...` in `admin-api` → PASS (all).
- [ ] **Step 5: Commit** `git commit -m "admin-api: allow resetting rate/quota to unlimited (0)"`

### Task 2: Broker pushes a queued OTA to a connected mux link

**Files:**
- Modify: `broker/mux.go` (`muxSession` field + keepalive), `broker/main.go:270-285` (`serveMux`)
- Test: `broker/mux_test.go`

**Interfaces:**
- Produces: `muxSession.pollOTA func() string` (nil-safe); checked on every `muxReapEvery` tick; sends `frameOTA` on sid 0 with the URL.

- [ ] **Step 1: Write the failing test** (`withMuxTimers` from `mux_capacity_test.go` sets reap to 50 ms)

```go
func TestMuxOTADeliveredToConnectedLink(t *testing.T) {
	withMuxTimers(t, time.Hour, time.Hour, time.Hour, 50*time.Millisecond)
	ota := make(chan string, 1)
	b := newBroker(config{token: "tok", dialWait: time.Second}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0"); go b.servePool(poolLn)
	stop := make(chan struct{}); defer func() { close(stop); poolLn.Close() }()
	go runMuxNode(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:60", true, 24, ota, stop)
	waitFor(t, func() bool { return b.muxCount() == 1 })
	if !b.reg.setOTA("aa:bb:cc:dd:ee:60", "https://x/fw.bin") { t.Fatal("setOTA") }
	select {
	case u := <-ota: if u != "https://x/fw.bin" { t.Fatalf("got %q", u) }
	case <-time.After(2 * time.Second): t.Fatal("OTA not delivered while link stayed up")
	}
}
```

- [ ] **Step 2: Run** `cd broker && go test -run TestMuxOTADeliveredToConnectedLink ./...` → FAIL (timeout).
- [ ] **Step 3: Implement:** in `serveMux` set `s.pollOTA = func() string { return b.reg.takeOTA(n) }`; in `keepalive()`'s `<-reap.C` case call `s.reapIdle()` then `if s.pollOTA != nil { if url := s.pollOTA(); url != "" { log.Printf("ota: pushing %s to %s (mux, live)", url, s.node.MAC); s.writeFrame(frameOTA, 0, []byte(url)) } }`.
- [ ] **Step 4: Run** `go test -race ./...` in `broker` → PASS.
- [ ] **Step 5: Commit** `git commit -m "broker: deliver queued OTA to a live mux link"`

### Task 3: SQLite store with legacy import

**Files:**
- Create: `admin-api/db.go`, `admin-api/store_import.go`
- Modify: `admin-api/store.go` (all `Store` methods; keep `User`, `PublicUser`, hashing/crypto helpers), `admin-api/go.mod`
- Test: `admin-api/store_test.go` (new), existing `api_test.go` must stay green

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func openDB(path string) (*sql.DB, error)` — `file:<path>?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)`, `SetMaxOpenConns(1)`, runs migrations from `var migrations = []string{...}` tracked in table `schema_version(version INTEGER)`.
  - `func NewStore(path string) (*Store, error)` keeps its signature: if `path` ends in `.json`, the DB is `filepath.Join(filepath.Dir(path), "platform.db")` and `importLegacyJSON(db, path)` runs once when `users` is empty and the file exists (file is then renamed `users.json.imported`). Otherwise `path` is the DB path.
  - `Store` methods keep names/signatures: `List() []*User`, `Get(string) (*User, error)`, `Create(*User) error`, `Update(string, func(*User) error) error`, `Delete(string) error`, `AddUsage(string, int64, time.Time)`, `Authorize(string, string, time.Time) (bool, string)`, `RateBytesPerSec(string) int64`, plus `DB() *sql.DB` for the device store.
  - Schema v1: `users(username TEXT PRIMARY KEY, pass_hash, cred_enc, enabled INTEGER, expires_at, rate_mbps REAL, quota_gb REAL, usage_bytes INTEGER, usage_month, created_at, note, device_mac)`.

- [ ] **Step 1: Write failing tests**

```go
func TestStoreImportsLegacyJSONOnce(t *testing.T) {
	dir := t.TempDir(); js := filepath.Join(dir, "users.json")
	os.WriteFile(js, []byte(`[{"username":"earth","password":"plain","enabled":true,"rate_mbps":100}]`), 0o600)
	st, err := NewStore(js); if err != nil { t.Fatal(err) }
	u, err := st.Get("earth"); if err != nil || !verifyPassword(u.PassHash, "plain") { t.Fatalf("import: %v %v", u, err) }
	if _, err := os.Stat(js + ".imported"); err != nil { t.Fatal("json should be renamed after import") }
	st2, _ := NewStore(js); if len(st2.List()) != 1 { t.Fatal("second open must not duplicate") }
}
func TestStoreConcurrentUsageWrites(t *testing.T) {
	st, _ := NewStore(filepath.Join(t.TempDir(), "p.db"))
	st.Create(&User{Username: "c", PassHash: hashPassword("x"), Enabled: true})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ { wg.Add(1); go func() { defer wg.Done(); st.AddUsage("c", 10, time.Now()) }() }
	wg.Wait()
	if u, _ := st.Get("c"); u.UsageBytes != 2000 { t.Fatalf("usage %d", u.UsageBytes) }
}
```

- [ ] **Step 2: Run** `go test -run 'TestStore' ./...` → FAIL (compile: `openDB`/behaviour).
- [ ] **Step 3: `go get modernc.org/sqlite@latest`; implement `db.go`, `store_import.go`, rewrite `Store` methods over SQL** (one `*sql.DB`, no in-memory map; `Update` runs `BEGIN IMMEDIATE`, read-modify-write, `COMMIT`; `rollMonth` logic unchanged, applied inside `Authorize`/`AddUsage`).
- [ ] **Step 4: Run** `go test -race ./...` in `admin-api` → PASS (all existing tests, which use `NewStore(<tmp>/users.json)`).
- [ ] **Step 5: Commit** `git commit -m "admin-api: SQLite store (modernc) with one-time users.json import"`

### Task 4: Devices table, device PUT/reveal, IP groups

**Files:**
- Create: `admin-api/devices_store.go`
- Modify: `admin-api/devices.go` (`deviceView`, `handleDevices`, `handleDeviceAction`), `admin-api/main.go` (routes unchanged; `server` gains `devices *DeviceStore`), `admin-api/db.go` (migration v2), `admin-api/api_test.go` (`newTestServer` wires `devices` + a fake broker)
- Test: `admin-api/devices_test.go` (new)

**Interfaces:**
- Produces:
  - `type Device struct { MAC, Name, Kind string; Slot int; CapacityMbps float64; CapacityMeasuredAt string; CapacityActiveStreams int; Lat, Lon *float64; WifiPasswordEnc, Notes string }` (`Kind` ∈ `shared|dedicated`, default `shared`).
  - `type DeviceStore struct{ db *sql.DB }` with `Get(mac) (*Device, error)`, `Upsert(mac string, fn func(*Device) error) error`, `List() []*Device`, `BySlot(slot int) (*Device, error)`.
  - `deviceView` gains: `Name, Kind, Notes string; Slot int; Port int; CapacityMbps float64; CapacityMeasuredAt string; CapacityActiveStreams int; Lat, Lon *float64; WifiPasswordSet bool; IPGroup string; IPGroupSize int; MaxUsers int; ActiveUsers int` (`IPGroup` = `RemoteIP`; `MaxUsers` per Global Constraints; `ActiveUsers` = enabled, unexpired users bound to any device in the group).
  - `PUT /api/devices/{mac}` body `{name,kind,lat,lon,notes,wifi_password}` (all optional; `lat`/`lon` validated −90..90 / −180..180; `wifi_password: ""` clears) → 200 `deviceView`.
  - `POST /api/devices/{mac}/reveal-wifi` → `{"wifi_password":"…"}`; logs `audit reveal-wifi mac=… ` (no password in the log).
  - Migration v2: `devices(mac TEXT PRIMARY KEY, name, kind, slot INTEGER UNIQUE, capacity_mbps REAL, capacity_measured_at, capacity_active_streams INTEGER, lat REAL, lon REAL, wifi_password_enc, notes)`.
  - Test harness: keep `newTestServer(t) (*server, *http.ServeMux)` unchanged; add `newTestServerWithBroker(t) (*server, *http.ServeMux, *fakeBroker)` where `fakeBroker` is an `httptest.Server` serving `GET /nodes` from its mutable `nodes []brokerNode` and recording `POST /nodes/{mac}/<action>` bodies in `calls []string`; it sets `BROKER_STATUS_URL` to its URL for the test (`t.Setenv`). `newTestServer` wires `s.devices` too.

- [ ] **Step 1: Write failing tests**

```go
func TestDevicePutAndReveal(t *testing.T) {
	_, mux, fb := newTestServerWithBroker(t)
	fb.nodes = []brokerNode{{MAC: "aa:bb:cc:dd:ee:01", RemoteIP: "27.1.1.1", Online: true}}
	rr, d := doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"name": "Home A", "kind": "dedicated", "lat": 13.75, "lon": 100.5, "wifi_password": "s3cret"})
	if rr.Code != 200 || d["name"] != "Home A" || d["kind"] != "dedicated" || d["wifi_password_set"] != true || d["lat"] != 13.75 { t.Fatalf("%d %v", rr.Code, d) }
	_, list := doJSONList(t, mux, "GET", "/api/devices", "admintok") // helper returning []map[string]any
	if _, has := list[0]["wifi_password"]; has { t.Fatal("password must never be in the list") }
	_, rv := doJSON(t, mux, "POST", "/api/devices/aa:bb:cc:dd:ee:01/reveal-wifi", "admintok", nil)
	if rv["wifi_password"] != "s3cret" { t.Fatalf("reveal: %v", rv) }
	rr, _ = doJSON(t, mux, "PUT", "/api/devices/aa:bb:cc:dd:ee:01", "admintok", map[string]any{"lat": 123})
	if rr.Code != 400 { t.Fatal("lat out of range must be 400") }
}
func TestDeviceIPGroupAndMaxUsers(t *testing.T) {
	// two devices on the same RemoteIP: ip_group_size 2; shared with capacity 12 → max_users 2; unmeasured → max_users 0
}
```

- [ ] **Step 2: Run** `go test -run TestDevice ./...` → FAIL.
- [ ] **Step 3: Implement `devices_store.go`, migration v2, PUT/reveal handlers, group/max computation in `handleDevices`.** `MaxUsers`: dedicated → 1; shared → `min(3, int(capacity/5))`, 0 when unmeasured.
- [ ] **Step 4: Run** `go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `git commit -m "admin-api: device records (name, kind, lat/lon, WiFi password), IP groups, capacity rule"`

### Task 5: Dashboard — device edit modal, binding visibility, `T()`

**Files:**
- Modify: `admin-api/dashboard/index.html` (users table, devices table, new `#dmodal`, `i18n`/`T`)
- Test: `admin-api/dashboard_test.go` (new) — smoke: served `/` contains `id="dmodal"`, `data-i18n` keys resolve (no `T(` left raw) and the users table header includes `Device`.

- [ ] **Step 1: Write the smoke test** (`dashboardHandler()` served through `httptest`, assert the three substrings).
- [ ] **Step 2: Run** → FAIL (`dmodal` missing).
- [ ] **Step 3: Implement:** `const i18n = {...}; const T = k => i18n[k] ?? k;` for new strings; devices table gets columns Name, Kind, Location (lat,lon → `https://www.openstreetmap.org/?mlat=..&mlon=..#map=16/..` link, falls back to geo city), WiFi (`SSID` + `••••` + **Reveal** button → `reveal-wifi` → shows for 10 s), Capacity (`— unmeasured` in warn pill when 0), Users `n/max`; **Edit** opens `#dmodal` (name, kind select, lat, lon, wifi password, notes) → `PUT`. Users table: Device column shows `name (mac)` and `:port` when `slot` > 0 (port from `/api/devices`); keep `assign…`.
- [ ] **Step 4: Run** `go test ./...` → PASS; open the page locally (`go run . ` with `ADMIN_TOKEN=x DATA_FILE=/tmp/u.json BROKER_STATUS_URL=<fake>`) and click through once.
- [ ] **Step 5: Commit** `git commit -m "dashboard: device editor (name, kind, lat/lon, WiFi password), binding and capacity columns"`

---

## Phase B — capacity & pinning

### Task 6: Speedtest object on the flasher

**Files:**
- Modify: `flasher/Dockerfile` (`RUN head -c 8388608 /dev/urandom > /usr/share/nginx/html/speedtest.bin`), `flasher/nginx.conf` (`location = /speedtest.bin { default_type application/octet-stream; add_header Cache-Control "no-cache"; }`)
- Verification: after deploy `curl -s -o /dev/null -w '%{size_download}' https://flash.jbacteam.com/speedtest.bin` → `8388608`.

- [ ] **Step 1: Make the two edits.** (Config-only task: no unit test; the deploy check above is the gate.)
- [ ] **Step 2: Commit** `git commit -m "flasher: 8 MiB speedtest.bin for device capacity measurement"`

### Task 7: gost slot services generated at start

**Files:**
- Create: `deploy/gost/gen-config.sh`
- Modify: `deploy/gost/Dockerfile` (CMD runs the generator before the existing awk render), `deploy/gost/gost.yaml` (unchanged base; generator appends), `deploy/docker-compose.yml` (`gost.ports` += `"3131-3160:3131-3160"`, env `SLOT_COUNT`, `SLOT_SECRET`, `PLUGIN_SECRET`), `deploy/.env.example` (`SLOT_COUNT=30`, `SLOT_SECRET=change-me-alphanumeric`, `SPEEDTEST_URL=https://flash.jbacteam.com/speedtest.bin`)
- Test: `broker/gostconfig_test.go` (new; runs `sh ../deploy/gost/gen-config.sh` with `SLOT_COUNT=2`, reads stdout)

**Interfaces:**
- Produces: for `N` in `1..SLOT_COUNT`, appended YAML: service `slot-N` on `:3130+N`, `handler.type auto`, `handler.auther auther-slot-N`, `handler.limiter limiter-0`, `handler.chain chain-slot-N`, `observer observer-0`, `metadata.enablestats true`; auther `auther-slot-N` plugin http `http://admin-api:8088/gost/${PLUGIN_SECRET}/auth?slot=N`; chain `chain-slot-N` → node `broker:1080` socks5 with `auth: {username: slot-N, password: ${SLOT_SECRET}}`.
- Generator contract: `SLOT_COUNT=<n> PLUGIN_SECRET=<s> SLOT_SECRET=<p> sh gen-config.sh base.yaml > out.yaml`.

- [ ] **Step 1: Write the failing test**

```go
func TestGostSlotConfigGenerates(t *testing.T) {
	cmd := exec.Command("sh", "../deploy/gost/gen-config.sh", "../deploy/gost/gost.yaml")
	cmd.Env = append(os.Environ(), "SLOT_COUNT=2", "PLUGIN_SECRET=ps", "SLOT_SECRET=ss")
	out, err := cmd.Output(); if err != nil { t.Fatal(err) }
	for _, want := range []string{"name: slot-1", `addr: ":3131"`, "name: slot-2", `addr: ":3132"`, "auth?slot=2", "username: slot-2", "password: ss", "name: chain-slot-1"} {
		if !strings.Contains(string(out), want) { t.Fatalf("missing %q in\n%s", want, out) }
	}
	if strings.Count(string(out), "name: service-0") != 1 { t.Fatal("base config must be kept once") }
}
```

- [ ] **Step 2: Run** `cd broker && go test -run TestGostSlotConfigGenerates ./...` → FAIL (script missing).
- [ ] **Step 3: Write `gen-config.sh`** (POSIX sh: cat base, then a `while` loop emitting the three blocks per slot); update Dockerfile CMD to `sh /etc/gost/gen-config.sh /etc/gost/gost.yaml > /tmp/gost.full.yaml && awk … /tmp/gost.full.yaml > /tmp/gost.yaml && exec /bin/gost -C /tmp/gost.yaml`; compose + env example edits.
- [ ] **Step 4: Run** the test → PASS; `sh deploy/gost/gen-config.sh deploy/gost/gost.yaml | head -80` reads sane.
- [ ] **Step 5: Commit** `git commit -m "gost: per-slot services 3131-3160 with slot-scoped auther and broker slot auth"`

### Task 8: Broker — SOCKS5 user/pass and slot routing

**Files:**
- Modify: `broker/main.go` (`handleSocks` negotiation; `dialViaMux`/`pickMux`/`evictIdleStream` gain a `pin *node` filter; `/nodes/{mac}/slot`; `/status` `slots_assigned`, `dials_pinned_offline`), `broker/registry.go` (`node.Slot int` persisted; `setSlot(mac, slot) bool`; `bySlot(slot) *node`), `broker/mux_test.go` or new `broker/slot_test.go`
- Test: `broker/slot_test.go`

**Interfaces:**
- Consumes: `SLOT_SECRET` env (config field `slotSecret`); when empty, user/pass auth is not offered.
- Produces:
  - SOCKS5 greeting: accept method `0x02` when the client offers it and `slotSecret != ""`, else `0x00`. RFC 1929 sub-negotiation: username `slot-N`, password must equal `slotSecret` → reply `0x00`, else `0x01` and close.
  - `slot-N` → `b.reg.bySlot(N)`; nil or offline (`!online()`) or no mux link → `socksReply(repHostFail)` + `dialsPinnedOffline++`; never falls back to the pool or another board. Dial restricted to that node's mux session(s).
  - `POST /nodes/{mac}/slot {"slot": N}` (0 clears; conflict with another node → 409); `/nodes` rows include `"slot"`.
  - Test helper: `socks5ConnectAuth(proxy, user, pass, host, port)`.

- [ ] **Step 1: Write failing tests**

```go
func TestSlotRoutesOnlyToPinnedDevice(t *testing.T)   // nodes A,B online; setSlot(B,1); slot-1 dial → B.DialsOK==1, A.DialsOK==0
func TestSlotOfflineIsRefusedNotRerouted(t *testing.T) // reg.touch("aa:bb:cc:dd:ee:99", …) once (seen before, no link now); setSlot(…,2); slot-2 dial → rep 0x04 within 1 s; pool node A untouched; status dials_pinned_offline==1
func TestSlotBadSecretRejected(t *testing.T)            // wrong password → sub-negotiation status 0x01, conn closed
func TestPoolStillNoAuth(t *testing.T)                  // existing socks5Connect (no auth) keeps working on the same listener
```

- [ ] **Step 2: Run** `go test -run 'TestSlot|TestPoolStillNoAuth' ./...` → FAIL.
- [ ] **Step 3: Implement** (`handleSocks` reads the method list instead of discarding it; `dialViaMux(host, port, tried, pin *node)`; `pickMux` skips sessions whose `node != pin` when `pin != nil`; `evictIdleStream` same filter; registry persistence adds `Slot`).
- [ ] **Step 4: Run** `go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `git commit -m "broker: SOCKS5 slot auth and per-device pinned routing"`

### Task 9: admin-api — slot assignment, admission control, capacity-capped limiter, slot-scoped auth

**Files:**
- Modify: `admin-api/devices.go` (`PUT` accepts `slot`; `POST …/slot` to broker), `admin-api/admin.go` (`assign` action enforces the rule; `handleCredentials` port), `admin-api/gost_plugins.go` (`handleAuth` reads `?slot=`; `handleLimiter` caps by device capacity), `admin-api/store.go` (`RateBytesPerSec` → `(s *Store) RateBytesPerSecCapped(username string, capMbps float64)` or the limiter composes it)
- Test: `admin-api/api_test.go`

**Interfaces:**
- Consumes: Task 4 `DeviceStore`, Task 8 `POST /nodes/{mac}/slot`.
- Produces:
  - `PUT /api/devices/{mac}` `{"slot": N}`: `1 ≤ N ≤ SLOT_COUNT` (env, default 30), unique across devices (409 `slot taken`), then broker `POST /nodes/{mac}/slot` (502 if it fails; DB not updated). `slot: 0` clears both.
  - `POST /api/users/{u}/assign {"device_mac"}`: 404 unknown device; 409 `{"error":"capacity","reason":"dedicated device already has a user" | "shared IP full (n/max)" | "capacity not measured"}`; same-IP-group counting per Task 4.
  - `/gost/auth?slot=N`: ok only if the user's bound device has `Slot == N`; without `?slot` (pool port) ok only if the user is **unbound**; deny reason `wrong port`.
  - `/gost/limiter`: `bps = min(userRate, deviceCapacity)` when the user is bound and capacity > 0; user rate 0 → capacity.
  - `/credentials`: `port` = `3130+slot` when bound to a slotted device, else `PROXY_PUBLIC_PORT`.

- [ ] **Step 1: Write failing tests**

```go
func TestAssignRespectsCapacityRule(t *testing.T)      // shared cap 12 → 2 users ok, 3rd 409 "shared IP full (2/2)"; dedicated → 2nd 409; unmeasured shared → 409 "capacity not measured"
func TestAuthScopedToSlotPort(t *testing.T)            // bound user: /gost/auth?slot=3 ok, ?slot=4 deny, no slot deny; unbound user: no slot ok, ?slot=3 deny
func TestLimiterCappedByDeviceCapacity(t *testing.T)   // user 100 Mbps on cap 12 → in/out == 12_000_000/8; user 0 → same; unbound user 20 → 20 Mbps
func TestDeviceSlotAssignmentForwardsToBroker(t *testing.T) // PUT slot 3 → fake broker saw POST /nodes/<mac>/slot {"slot":3}; duplicate → 409; port in /credentials == 3133
```

- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement** the four behaviours; `SLOT_COUNT` via `atoiDefault(envOr("SLOT_COUNT","30"),30)`.
- [ ] **Step 4: Run** `go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `git commit -m "admin-api: slot assignment, admission by IP capacity, slot-scoped auth, capacity-capped limiter"`

### Task 10: Measure device capacity through its slot

**Files:**
- Create: `admin-api/measure.go`
- Modify: `admin-api/devices.go` (`POST /api/devices/{mac}/measure`), `admin-api/main.go` (env `SPEEDTEST_URL`, `GOST_INTERNAL_HOST` default `gost`)
- Test: `admin-api/measure_test.go`

**Interfaces:**
- Produces:
  - `func measureViaProxy(ctx context.Context, proxyURL, targetURL string, maxBytes int64) (mbps float64, bytes int64, err error)` — `http.Transport{Proxy: http.ProxyURL(proxyURL)}`, reads up to `maxBytes` (8 MiB) or 45 s, `mbps = bytes*8/secs/1e6`.
  - `POST /api/devices/{mac}/measure`: 409 `no slot` when unslotted; 409 `measurement in progress` (per-MAC mutex); creates user `_measure-<6hex>` bound to the device bypassing admission (flag `internal`), runs `measureViaProxy("http://_measure-…:<pw>@<GOST_INTERNAL_HOST>:<3130+slot>", SPEEDTEST_URL, 8<<20)`, deletes the user (also on error), stores `capacity_mbps` (rounded to 0.1), `capacity_measured_at` (RFC3339), `capacity_active_streams` (from broker `/nodes` `active_dials` read just before), returns `{"capacity_mbps":…, "bytes":…, "seconds":…, "active_streams":…}`.
  - Measurement users never appear in `/api/users` (filtered by prefix) and never count toward capacity.

- [ ] **Step 1: Write failing tests**

```go
func TestMeasureViaProxyComputesMbps(t *testing.T)  // httptest "proxy" serving 1 MiB at ~2 MiB/s → mbps within 50% of 16
func TestMeasureEndpointStoresCapacityAndCleansUp(t *testing.T) // fake broker nodes active_dials=3; after POST: device capacity>0, capacity_active_streams==3, no _measure user left
func TestMeasureRequiresSlot(t *testing.T)          // unslotted → 409 "no slot"
```

- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `go test -race ./...` → PASS.
- [ ] **Step 5: Commit** `git commit -m "admin-api: measure device capacity through its slot port"`

### Task 11: Dashboard — slots, ports, capacity, Measure

**Files:**
- Modify: `admin-api/dashboard/index.html`
- Test: `admin-api/dashboard_test.go` (extend smoke: `Measure` button markup and `:31` port rendering helper present)

- [ ] **Step 1: Extend the smoke test** (assert substrings `measureDevice(` and `slotPort(`).
- [ ] **Step 2: Run** → FAIL.
- [ ] **Step 3: Implement:** device edit modal gains **Slot** (number, blank = none) and shows **Port** `3130+slot`; table gains Capacity cell with **Measure** button (`POST …/measure`, disabled while running, shows `12.3 Mbps · 3 streams active · 2 min ago`), IP group badge `IP 27.x (2 boards)`, Users `n/max` turning red at max; users table Device column shows `name · :port`; `copyCreds` already uses the returned `port`. Settings text: list the slot port range.
- [ ] **Step 4: Run** `go test ./...` → PASS; manual click-through locally.
- [ ] **Step 5: Commit** `git commit -m "dashboard: slots/ports, capacity measurement, IP group and n/max users"`

### Task 12: Deploy, migrate the two legacy users, verify

**Files:**
- Modify: `README.md` (slot ports, capacity rule, measurement), `docs/PLATFORM.md` (update "declined": WiFi password *entered*, lat/long *entered*), `HANDOFF-mux1-rollout.md` (one line pointing at the plan)

- [ ] **Step 1: Docs edits; commit** `git commit -m "docs: slots, capacity rule, platform map updates"`.
- [ ] **Step 2: Operator sets Coolify env:** `SLOT_SECRET`, `SLOT_COUNT=30`, `SPEEDTEST_URL`; push; Deploy.
- [ ] **Step 3: Verify from outside:** `curl -s -o /dev/null -w '%{size_download}' https://flash.jbacteam.com/speedtest.bin` → `8388608`; `nc -z 210.246.215.196 3131` → open; `/api/overview` broker has `slots_assigned`.
- [ ] **Step 4: In the console:** device `e8:3d:c1:f2:86:bc` → Edit → name, kind `shared`, slot `1` → Measure → capacity recorded; assign `earth` and `testuser` (now 2/2 or 2/3 depending on capacity) → their `/credentials` show port `3131`; tell both users the new port.
- [ ] **Step 5: Acceptance:** `curl -x http://<user>:<pass>@proxy.jbacteam.com:3131 https://ifconfig.me` → `27.130.95.10`; same creds on `:3128` → 407 (`wrong port`); unknown slot `:3140` → 407/0x04 not a hang; `/api/overview` after 10 min: `dials_no_node` unchanged.

---

## Self-review notes

- Spec coverage: §2 goals 1–4 → Tasks 4, 8–11; §6.2 items 1, 2, 3, 4, 5, 12, 13, 14, 17 → Tasks 7–8, 10, 3, 4, 4–5, 1–2, 4, 9/12, 7. Items 6–11, 15, 16, 18 (portal, plans, Stripe, email, signup) are Phases C–E, out of this plan. §6.2.16 language map → Task 5 (`T()`).
- Types: `Device`, `DeviceStore`, `deviceView` fields, `dialViaMux(…, pin *node)`, `measureViaProxy` signatures are used consistently across Tasks 4, 8, 9, 10, 11.
- Review Focus 1–5 are pinned by Tasks 1, 3, 8, 9, 10 tests respectively.
- Proportion: 12 tasks for a 288-line spec slice; bodies are given only where tests cannot determine them (generator contract, measurement formula, SOCKS negotiation values).
