# Agent notes — ESP32 exit-node proxy platform

Operational knowledge that doesn't live anywhere else in this repo: Windows
toolchain quirks, the local bench rig, and the test board. Read this before
building firmware or touching the broker/admin-api against live hardware.

## Repo layout

- `broker/` — Go. The VPS-side mux1 link multiplexer between boards and
  customer traffic (gost). `go test ./...`.
- `admin-api/` — Go. REST API + embedded single-file dashboard at
  `admin-api/dashboard/index.html` (vanilla JS/CSS, no build step — edit in
  place, `dashboard_test.go` greps the served HTML for marker strings).
  `go test ./...`.
- `firmware/` — ESP-IDF 5.5 C. The board side (`components/rproxy/rproxy.c`
  is the mux1 link). Builds per board via sdkconfig overlays, see below.
- `flasher/` — static web flasher (ESP Web Tools) + `firmware/releases/<ver>/`
  archive, managed by `tools/fw_release.py` (never hand-edit
  `flasher/firmware/releases.json`).
- `docs/dashboard-review/` — three design reports (UX/IA, frontend spec,
  backend/API plan) from a 2026-10-21 mockup review, plus a prioritised
  backlog in `README.md`. This is the live design source for dashboard work
  — read it before redesigning anything.
- `docs/superpowers/specs/` and `.../plans/` — design specs and
  implementation plans for completed and in-flight features, dated.

## ESP-IDF build (Windows)

The firmware needs **ESP-IDF v5.5.x**, not whatever version `esp-idf
manager` (EIM) last installed — check `C:\Users\ADMIN\esp-idf\` for a
`v5.5.2` checkout and `C:\Users\ADMIN\.espressif\python_env\` for its venv
before assuming EIM's default toolchain works; a 6.x toolchain cannot build
this firmware (`esp_wireguard_idf_5.5.x` pins 5.5.6, and it's untested
beyond 5.5.x generally).

`export.ps1` breaks if the system `python` isn't the exact version the venv
was built against. Activate instead with:
```powershell
$env:IDF_PATH = "C:\Users\ADMIN\esp-idf\v5.5.2\esp-idf"
$env:IDF_TOOLS_PATH = "C:\Users\ADMIN\.espressif"
$act = & "$env:IDF_TOOLS_PATH\python_env\idf5.5_py3.11_env\Scripts\python.exe" "$env:IDF_PATH\tools\activate.py" --export | Select-Object -Last 1
. $act
```
Must run from PowerShell, not Git Bash/MSYS (`idf_tools.py` refuses it).

Build from a short, space-free mirror — this workspace's path has spaces
and a `+`, which breaks CMake/ninja. `D:\espbuild\build-exitnode.ps1
-Board s3n16r8|c3mini|c3plain [-Clean] [-ExtraDefs <path>]` mirrors
`firmware/` to `D:\espbuild\fw-c3\firmware` (robocopy /MIR, excluding
build dirs) and runs `idf.py build` there with the right sdkconfig overlay
stack per board. If that script is gone, recreate it: mirror, then
`idf.py -B build_<board> "-DSDKCONFIG=build_<board>/sdkconfig"
"-DSDKCONFIG_DEFAULTS=sdkconfig.defaults;sdkconfig.defaults.<chip>;sdkconfig.defaults.exitnode[;sdkconfig.defaults.<board>]"
set-target <esp32s3|esp32c3>` then `idf.py build`.

## Versioning and releases

`firmware/CMakeLists.txt` sets `EXITNODE_RELEASE` and appends `-s3`/`-c3`/
`-c6` from `IDF_TARGET`, so the version a board reports always names its
chip (`2.4.23-c3`) — this is how the broker and OTA picker refuse a
mismatched image. Bump `EXITNODE_RELEASE` for every release (the archiver
refuses to overwrite a target's image under an existing version with a
different build).

Archive a built image: `python -X utf8 tools/fw_release.py add
<build_dir> --status testing --title "..." --notes "..." --commit
<hash>`. Run both boards' builds before archiving, then `python -I
tools/fw_release.py list` and `python -I tools/test_fw_release.py` to
verify. `--status stable` plus `--latest` only once a release has been
soaked on hardware — testing/latest stayed at `2.4.21-mux1.2` through the
2.4.22 and 2.4.23 rounds deliberately.

## Deploy

**Coolify is Manual-deploy, no webhook.** Pushing `main` does *not* go
live — open the Coolify app and click Deploy. The stack
(gost+broker+admin-api+flasher) redeploys together; any board with a live
link reconnects within seconds. A board running mux1 firmware refuses an
older broker's HELLO response, so broker always deploys before any OTA
that needs its new protocol bits.

Live URLs: admin dashboard `https://admin.jbacteam.com` (Bearer
`ADMIN_TOKEN`), flasher `https://flash.jbacteam.com`, broker domain
`proxy.jbacteam.com:7000` (TLS). Coolify's runtime-log panel keeps only
~30 recent lines per container — capture evidence (API responses, board
serial output) while a test runs, not after the fact.

## Bench rig (test without deploying)

A local broker + admin-api pair on this PC lets you test firmware and
dashboard changes against a real board before touching production.

- `D:\espbuild\broker.exe` (rebuild from `broker/`: `go build -o
  D:\espbuild\broker.exe .`). TLS variant env: `POOL_ADDR=:7443
  SOCKS_ADDR=:1081 STATUS_ADDR=:7444 POOL_TOKEN=<D:\espbuild\pool_token.txt>
  TLS_CERT_PEM=<pki_out/broker.crt> TLS_KEY_PEM=<pki_out/broker.key>
  NODES_FILE=D:\espbuild\nodes-tls.json OTA_ALLOW_HTTP=1`. CA/cert pair
  under `pki-KEEP-OFFLINE (1)/pki_out/` (sibling of this workspace) — the
  firmware's pinned CA matches it.
- `D:\espbuild\admin-api.exe` (rebuild from `admin-api/`). Env:
  `ADMIN_TOKEN=test1234 LISTEN_ADDR=:8099
  BROKER_STATUS_URL=http://localhost:7444
  PROXY_PUBLIC_HOST=proxy.jbacteam.com DATA_FILE=D:\espbuild\admin-users.json`.
- A board is pointed at the bench broker over its serial console:
  `rproxy server 192.168.1.114 7000; rproxy tls off; restart` (plain) or
  with TLS, the board's own `rproxy server/tls` commands against the
  `:7443` TLS listener. **Always point it back before finishing a
  session**: `rproxy server proxy.jbacteam.com 7000; rproxy tls on;
  restart`, or a hardware board silently stays off production.
- Throughput/load test through the bench broker's SOCKS5: `curl
  --socks5-hostname 127.0.0.1:1081 https://speed.cloudflare.com/__down?bytes=N`.
  The node refuses RFC1918 dial targets, so a LAN-hosted origin won't work
  for a soak.
- Read live state from the broker's status API (`curl
  http://localhost:7444/nodes`), not the serial console — see below.

## ESP32-C3 Super Mini test board (COM30)

MAC `ac:a7:04:ba:ae:c0`, no PSRAM, 4 MB flash. Home WiFi credentials are
in its NVS only (not in this repo).

- **Flashing:** `esptool --chip esp32c3 --port COM30 --baud 921600
  --before default_reset --after watchdog_reset write_flash ...`. The
  `--after` flag matters: the default `hard_reset` leaves this board
  sitting in the ROM bootloader (console silent, app never runs) —
  `watchdog_reset` is what actually boots the new image.
- **Console:** open COM30 with default pyserial settings (DTR/RTS
  asserted, like a real terminal) — `rts=False, dtr=False` reads zero
  bytes on this board's USB-Serial/JTAG. `D:\espbuild\c3probe.py COM30
  "<cmd>" wait:N` sends a command and captures output.
- **A console probe reboots the board.** Opening/closing the COM30 handle
  toggles DTR/RTS, which resets this chip — confirmed by uptime dropping
  from 115 s to 3 s right after a probe. **Never** use the console to
  check uptime, health, or mid-soak state; read `/nodes` on the broker
  instead (firmware 2.4.23+ reports a STATS sample every 30 s; see
  `broker/registry.go`'s `nodeStats`).
- **Factory reset** (either board): hold **BOOT** (GPIO0 on S3, GPIO9 on
  C3) at least 5 s while running — NVS erased, setup AP returns. Holding
  RST only reboots. A board also returns to its setup AP on its own if it
  has no uplink for 90 s (e.g. moved to a different network).

## Known throughput/capacity facts (don't re-derive)

- WiFi modem power save must stay off on an exit node
  (`esp_wifi_set_ps(WIFI_PS_NONE)` in `rproxy.c`'s `start_pool()`) — with
  it on, inbound ACK/credit latency is ~100 ms (DTIM interval) and caps a
  single mux link hard. This was the single biggest throughput bug found
  on 2026-10-21; if a board relays slowly, check this and the link's
  `TCP_NODELAY` setting before anything else.
- C3 Super Mini (firmware 2.4.22/2.4.23, 4 streams, 8 KB window): idle is
  about 1% CPU, ~140 KB of 292 KB internal heap free. Under 2 parallel
  streams at ~1.3 Mbps each (2.6 Mbps aggregate): CPU **56%**, heap free
  drops to ~94 KB (low-water 65 KB). The 4-stream cap is CPU/heap-bound on
  this chip, not just window-bound — watch the STATS `cpu_pct`/`heap_min`
  fields before raising it further.
- S3 N16R8 (8 MB PSRAM, 40 streams, 32 KB window): about 9-12 Mbps per
  board on mux1.4-era firmware.

## Dashboard phase 2 decisions (2026-10-21)

The operator delegated remaining design choices. The chosen shared-board
admission rule is **0 seats without a positive capacity measurement**, then
`min(3, max(1, floor(capacity_mbps / 5)))`. A measured C3 at 2.6 Mbps gets
1 seat; the existing ceiling of 3 remains. Dedicated boards retain 1 seat.
This is an admission allowance, not a promise of 5 Mbps on a C3.

**2026-10-22 superseding decision:** seats and admission are per board/MAC,
including boards sharing one public IP. `active_users` is board-local;
dedicated reserves one board, not the whole IP. Keep IP-group metadata for
information only. See `docs/superpowers/specs/2026-10-22-per-board-seats-design.md`.

Metrics follow the backend spec: a decreased cumulative counter contributes
its current value after reset; a first sample only establishes a baseline.
Drawer queries use `entity=node&id=<mac>` for `/api/metrics` and
`subject=<mac>` for `/api/events`, never `node=` or `target=` aliases.
Health component weights are availability/dial success/stream headroom/
heap headroom/CPU headroom = **40/30/10/10/10**, with absent components
excluded and the remaining weights renormalised.

Broker `/nodes` now adds live-only `max_streams` (accepted mux limit,
including the legacy default 24), `link_since` (latest successful
`READY mux1`, cleared when the last mux link closes), and per-node
`dials_pinned_offline`. A previous link closing after a reconnect must not
clear the new link's timestamp. Unknown/unassigned slot refusals affect
only the broker-wide counter; known pinned boards get their own count
when no usable link remains (including blocked/full/exhausted links).
These fields and counters are never persisted to the node file.

Local tests/builds are development evidence. Coolify deployment, production
checks, and hardware soak are separate gates; do not infer any of them from
the phase 2 implementation or fixture screenshots.
