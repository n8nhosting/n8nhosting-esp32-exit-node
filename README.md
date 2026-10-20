# ESP32-S3 Exit-Node Proxy Platform

Rent out a home/office internet connection as a proxy with a **Thai exit IP**,
using an **ESP32-S3** at home as the exit node and a **VPS** (GOST) as the
customer-facing proxy. The ESP32 sits behind NAT and dials **out** to the VPS,
so no port-forwarding at home is needed.

```
 customer abroad                         VPS (Coolify)                         home
 ───────────────        HTTP CONNECT        ┌───────────────┐    SOCKS5    ┌──────────┐   WiFi STA
  curl -x user:pass  ───────────────────▶   │     gost      │ ───────────▶ │  broker  │ ◀━━━━━━━━━ ESP32-S3
  @vps:8080                                  │ auth+limit+   │              │  :1080   │   (1 TLS link, dials out)
                                             │ quota (plugin)│              │  :7000 ◀━┼━━━━━━━━━━━ mux1 streams
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
| `firmware/` | `esp32_nat_router` fork + **`rproxy`** component: reverse dialer over **one multiplexed TLS link (CA pinned)** with up to 24 concurrent streams and per-stream flow control, **zero-touch provisioning** (broker/token baked at build), MAC identity, **OTA on command**, AP auto-off, minimal UI (no Firewall/VPN/PCAP), "Proxy service: online/connecting/error" on the status page. Prebuilt S3 N16R8 bins in `firmware/prebuilt_s3n16r8/`. | builds clean on ESP-IDF 5.5.6; wire protocol covered by broker tests |
| `broker/` | Go broker: **TLS** ESP32 link (`:7000`, multiplexed `mux1`; still accepts the v1.2 one-DIAL-per-connection pool) ⇄ GOST SOCKS5 upstream (`:1080`); node registry by **MAC** (online, exit IP, traffic, block/allow), **OTA push**, `/status` `/nodes`. | `go test` (17 tests: relay, 1 MB integrity, concurrency, safety filter, TLS pinning, blocking, OTA, persistence; mux: 40 streams on one link, slow-stream isolation, legacy+mux side by side) |
| `admin-api/` | Go: **admin dashboard** (users, devices by MAC, OTA push, stats) + user/billing REST + GOST **auth/limiter/observer** plugins + payment webhook. PBKDF2 password hashing, JSON store, no external deps. | `go test` (7 tests) + headless-browser smoke test |
| `flasher/` | ESP Web Tools static page to flash the board from Chrome/Edge + configure it. | manifest validated |
| `deploy/` | Full-stack `docker-compose.yml` for Coolify (gost + broker + admin-api + flasher). | YAML validated |

### Upgrading devices to the multiplexed link

Since the mux release each ESP32 keeps **one** TLS link to the broker and every
customer connection runs as a stream inside it (frame format in
`broker/mux.go`), instead of one pre-opened TLS connection per customer.
Deploy the new **broker first**: it serves old (pool) and new (mux) devices at
the same time. Then OTA the devices. New firmware refuses to talk to an old
broker (it logs "broker does not support the mux link").

## Deploy on Coolify (one stack)

1. In the **ESP32 Proxy Server** project → **+ New Resource → Public/Private Git
   Repository** → this repo → **Build Pack: Docker Compose**, compose path
   `deploy/docker-compose.yml`, branch `main`.
2. Set Environment Variables (see `deploy/.env.example`): `API_USER`, `API_PASS`,
   `POOL_TOKEN`, `ADMIN_TOKEN`, `PAYMENT_WEBHOOK_SECRET`, `GOST_PLUGIN_SECRET`
   (alphanumeric), and the broker TLS pair `TLS_CERT_B64` / `TLS_KEY_B64`
   (`base64 -w0 broker.crt`, signed by the CA baked into the firmware — see
   `docs/SECURITY.md`). Optionally `PROXY_PORT` (default 8080) and `POOL_PORT`.
3. Deploy. Then in **Domains**, map `gost` **18080** → `gost-api.<domain>`,
   `flasher` **80** → `flash.<domain>`, and `admin-api` **8088** →
   `admin.<domain>` (the dashboard; sign in with `ADMIN_TOKEN`).
4. Point your existing **GOST UI** at the new `gost-api.<domain>` with
   `API_USER`/`API_PASS`. (The standalone `gost-proxy` you had can be removed —
   this stack replaces it and adds auth/limit/quota + the ESP32 chain.)

Ports to open on the VPS firewall: **8080** (customers) and **7000** (your ESP32).

## Flash & configure the ESP32-S3 (N16R8)

Open `flash.<domain>` in Chrome/Edge: flash → join home WiFi (STA). That's it —
the firmware carries the broker address, token and CA, so the device connects
on its own (TLS), shows **Proxy service: online** on its status page, turns its
setup AP off, and appears in the dashboard under **Devices** by MAC. Admins can
block/allow a device or push a firmware update (**Update FW**) from there.

Rebuild with your own secrets: copy `firmware/sdkconfig.prov.example` →
`sdkconfig.prov`, fill in broker host + `POOL_TOKEN`, put your CA at
`firmware/components/rproxy/broker_ca.pem`, run `firmware/build_s3n16r8.sh`,
then archive it with `tools/fw_release.py add firmware/build_s3n16r8 --latest`.
Never commit `sdkconfig.prov`. Every release stays available for rollback
(flasher version picker, dashboard **Update FW**); see
[docs/FIRMWARE_RELEASES.md](docs/FIRMWARE_RELEASES.md).

## Manage users / devices / billing

Use the **dashboard** at `admin.<domain>` (Overview, Users, Devices, Settings).
The same operations are available as REST (Bearer `ADMIN_TOKEN`):

```
POST   /api/users                 {username,password,rate_mbps,quota_gb,expires_at}
GET    /api/users                 list
GET    /api/users/{name}          one (incl. usage_bytes, usage_month)
PUT    /api/users/{name}          update any field
DELETE /api/users/{name}
POST   /api/users/{name}/enable | /disable | /reset-quota | /extend {days}
GET    /api/devices               ESP32 nodes: mac, online, exit IP, traffic, firmware
POST   /api/devices/{mac}/block | /allow | /forget | /ota {url,version} | /ota-cancel
GET    /api/overview              dashboard summary (users + broker)
POST   /webhook/payment           PSP -> activate/extend (X-Webhook-Token or HMAC)
```

Customers connect with **HTTP CONNECT or SOCKS5** on the same port
(`gost` handler `auto`), e.g. `curl -x socks5h://user:pass@VPS:3128 https://…`.

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
