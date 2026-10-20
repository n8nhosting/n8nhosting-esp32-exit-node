# ESP32-S3 Exit-Node Proxy Platform

Rent out a home/office internet connection as a proxy with a **Thai exit IP**,
using an **ESP32-S3** at home as the exit node and a **VPS** (GOST) as the
customer-facing proxy. The ESP32 sits behind NAT and dials **out** to the VPS,
so no port-forwarding at home is needed.

```
 customer abroad                         VPS (Coolify)                         home
 ───────────────        HTTP CONNECT        ┌───────────────┐    SOCKS5    ┌──────────┐   WiFi STA
  curl -x user:pass  ───────────────────▶   │     gost      │ ───────────▶ │  broker  │ ◀━━━━━━━━━ ESP32-S3
  @vps:8080                                  │ auth+limit+   │              │  :1080   │   (pool, dials out)
                                             │ quota (plugin)│              │  :7000 ◀━┼━━━━━━━━━━━ HELLO/DIAL
                                             └──────┬────────┘              └──────────┘               │
                                                    │ plugins                                          ▼
                                             ┌──────▼────────┐                                  home router → Thai IP
                                             │   admin-api   │  users / billing / webhook
                                             └───────────────┘
```

The exit is the home WiFi: the ESP32 resolves DNS and `connect()`s out through
its STA uplink, so traffic leaves with the home connection's Thai IP.

## Components

| Dir | What it is | Tested |
|-----|-----------|--------|
| `firmware/` | `esp32_nat_router` fork + **`rproxy`** component (reverse dialer). Prebuilt S3 N16R8 bins in `firmware/prebuilt_s3n16r8/`. | builds clean on ESP-IDF 5.5.6; protocol covered by broker tests |
| `broker/` | Go broker: ESP32 pool (`:7000`) ⇄ GOST SOCKS5 upstream (`:1080`) + `/status`. | `go test` (6 tests: relay, 1 MB integrity, concurrency, safety filter) |
| `admin-api/` | Go: user/billing CRUD + GOST **auth/limiter/observer** plugins + payment webhook. JSON store, no external deps. | `go test` (7 tests) |
| `flasher/` | ESP Web Tools static page to flash the board from Chrome/Edge + configure it. | manifest validated |
| `deploy/` | Full-stack `docker-compose.yml` for Coolify (gost + broker + admin-api + flasher). | YAML validated |

## Deploy on Coolify (one stack)

1. In the **ESP32 Proxy Server** project → **+ New Resource → Public/Private Git
   Repository** → this repo → **Build Pack: Docker Compose**, compose path
   `deploy/docker-compose.yml`, branch `main`.
2. Set Environment Variables (see `deploy/.env.example`): `API_USER`, `API_PASS`,
   `POOL_TOKEN`, `ADMIN_TOKEN`, `PAYMENT_WEBHOOK_SECRET` (use **alphanumeric**
   secrets). Optionally `PROXY_PORT` (customer proxy, default 8080) and
   `POOL_PORT` (ESP32 uplink, default 7000).
3. Deploy. Then in **Domains**, map the `gost` service port **18080** to e.g.
   `gost-api.<domain>` and the `flasher` service port **80** to `flash.<domain>`.
   Keep `admin-api` internal (or map 8088 to `admin.<domain>` — it is bearer-token
   gated).
4. Point your existing **GOST UI** at the new `gost-api.<domain>` with
   `API_USER`/`API_PASS`. (The standalone `gost-proxy` you had can be removed —
   this stack replaces it and adds auth/limit/quota + the ESP32 chain.)

Ports to open on the VPS firewall: **8080** (customers) and **7000** (your ESP32).

## Flash & configure the ESP32-S3 (N16R8)

Open `flash.<domain>` in Chrome/Edge and follow the 4 steps: flash → join home
WiFi (STA) → paste the generated `rproxy` console commands (broker host, port,
`POOL_TOKEN`, pool size) → `rproxy enable`. `rproxy status` should show workers
connected and `uplink: STA has IP`. Prebuilt bins are also in
`firmware/prebuilt_s3n16r8/`; rebuild with `firmware/build_s3n16r8.sh`.

## Manage users / billing

Admin REST (Bearer `ADMIN_TOKEN`) on the admin-api:

```
POST   /api/users                 {username,password,rate_mbps,quota_gb,expires_at}
GET    /api/users                 list
GET    /api/users/{name}          one (incl. usage_bytes, usage_month)
PUT    /api/users/{name}          update any field
DELETE /api/users/{name}
POST   /api/users/{name}/enable | /disable | /reset-quota | /extend {days}
POST   /webhook/payment           PSP -> activate/extend (X-Webhook-Token or HMAC)
```

GOST calls `/gost/auth`, `/gost/limiter`, `/gost/observer` itself (wired by
`deploy/gost/gost.yaml`). Auth denies disabled/expired/over-quota users; the
limiter enforces `rate_mbps`; the observer accumulates monthly usage.

See `docs/SECURITY.md` and `docs/LEGAL.md` before taking paying customers.

## Status / what is tested here

Everything except live hardware and a live GOST instance was tested in CI-style
runs: the firmware **compiles** for S3 N16R8 (app 1.38 MB in a 3 MB slot); the
broker + the exact ESP32 wire protocol + SOCKS5 + relay + safety filter pass an
in-process integration suite; the admin-api plugin/billing logic passes unit
tests. You still need to **flash a real board and verify on hardware**, and
confirm the GOST plugin wiring against your running GOST (the observer byte
semantics may need `USAGE_MODE` calibration — see `docs/USAGE.md`).

Firmware is MIT (fork of martin-ger/esp32_nat_router); platform code is MIT.
