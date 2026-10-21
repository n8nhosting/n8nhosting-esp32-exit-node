# ESP32-C3 exit node — design

Date: 2026-10-21. Status: draft for review. Author: Claude, from a brainstorming
session with the operator (Earth).

## 1. Intent

Add a second, cheaper exit-node board — the **ESP32-C3 Super Mini** (ESP32-C3FN4:
single-core RISC-V 160 MHz, 400 KB SRAM, **no PSRAM**, 4 MB embedded flash,
USB-Serial/JTAG, ~120–160 THB) — to the existing exit-node product next to the
ESP32-S3 N16R8. A C3 node joins the **same broker, pool and dashboard** with the
**same firmware tree**, built from a per-board sdkconfig overlay, and appears on
the dashboard as a device with a lower capacity.

Why now: the S3 N16R8 costs several times more than a C3 Super Mini. A C3 that
carries a handful of streams reliably lets the operator deploy many more nodes
for the same money.

Phase 2 (not in this design, see §9): the operator plans a separate server that
receives MQTT telemetry and WireGuard tunnels so that a customer who bought a
board also gets a view of other home devices (humidity, air quality, smoke
detectors). This design only keeps those two features compiled in.

## 2. Goals

1. A C3 build of the exit-node firmware that holds its broker link for days,
   carries **N concurrent customer streams** (N fixed by measurement, see §5)
   without heap exhaustion, and survives OTA.
2. One firmware tree: C3 and S3 builds differ only by sdkconfig overlays and
   target-gated code. No forked components.
3. The production S3 N16R8 boards keep working unchanged until the operator
   OTAs them; the shared improvements (feature gates, OTA drain, mux window
   negotiation) ship to S3 in its **next** release, not retro-fitted into
   `2.4.21-mux1.2` (current `latest`).
4. The broker and dashboard understand two board types: per-target firmware
   releases, OTA pushes that cannot send an image for the wrong chip, and a
   device record that shows what the board is.
5. Release tooling (`tools/fw_release.py`, flasher site) produces and serves
   both targets.

## 3. Non-goals

- Matching S3 throughput or stream count. A C3 is a smaller node by design.
- Phase 2 itself: WireGuard as a management tunnel (needs policy routing so the
  exit IP stays the home IP), the MQTT sensor platform, the separate server.
- BLE or SmartConfig provisioning. The BT stack does not fit next to TLS on C3.
- Any new customer-facing UI beyond WiFi setup.
- Upstream (`martin-ger/esp32_nat_router`) feature parity on other targets. New
  Kconfig gates default to the upstream behaviour so other targets build as
  before, but they are not tested here.

## 4. Anti-goals (failure even if it "works")

- A C3 that crashes or reboots under load, or that fails OTA in the field. A
  node that silently drops is worse than no node: the dashboard admission rule
  binds customers to it.
- Any broker change that breaks firmware already deployed (`2.4.21`,
  `2.4.21-mux1.x`). The mux1 protocol stays backward compatible.
- An OTA path that can push an S3 image to a C3 or vice versa.
- Capturing or logging customer traffic on the node (PCAP stays out).
- Leaving the production S3 release train blocked on C3 work.

## 5. Constraints and measurements

All numbers from the throwaway spike builds of 2026-10-21 (firmware code of
`2.4.21-mux1.4`, IDF v5.5.2, 6 streams configured), flashed on the real board
(COM30, MAC `ac:a7:04:ba:ae:c0`), uplink `IokWireless_2.4Ghz`.

| Measurement | S3 config applied to C3 as-is | Config-only RAM diet (spike v2) | **2.4.22 `c3mini` build (this design, measured 2026-10-21)** |
|---|---|---|---|
| App binary | 1,509 KB (4% of 1,536 KB slot free) | 1,351 KB (14% free) | **1,215 KB (40% of the 1,984 KB slot free)** |
| Code pinned in IRAM | 110 KB | 70 KB | 68 KB |
| Heap available at boot | 239 KB | 279 KB | 283 KB |
| Free heap, idle, no uplink | **1.3–1.8 KB** (board rebooted once) | 76 KB | not measured separately (higher than below) |
| Free heap, TLS mux link up, 0 streams | not reachable | **54 KB** (min 45 KB during handshake) | **143 KB** (min 128 KB); 171 KB on a plain-TCP link |

The 2.4.22 column has the feature gates, no web server after the uplink is
up, 4 streams / 8 KB window / 4 KB frames / 1 dialer, link buffers only while
a link exists, and the diet config. The old production broker (no token
support) still accepts its HELLO; a broker from this branch logs
`max 4 streams, win 8192, frame 4096, chip "esp32c3"`.

Spike v2 = `-Os` instead of `-O2` globally, WiFi/lwIP IRAM optimisation off,
WiFi pools static-RX 6 / dynamic-RX 16 / dynamic-TX 16, TX/RX BA window 6,
mgmt short buffers 10, no FAT console history, 16 lwIP sockets, DHCP server 4
stations. The TLS link to `proxy.jbacteam.com:7000` (CA pinned) came up on the
first try and AP auto-off worked.

Where the remaining RAM goes at idle (task list + code reading):

- rproxy pre-allocates at boot, before any link: 18 KB rx buffer (16 KB frame +
  header + slack), 16 KB frame buffer, 4 dialer tasks × 4.6 KB stack (each uses
  ~0.6 KB), 8 KB mux task stack → ~60 KB.
- httpd task holds ~20 KB of stack of which ~15.7 KB is never used; the web
  server, captive-portal DNS and mDNS are all up during normal operation.
- WiFi driver in AP+STA mode, lwIP, console (7 KB main stack), event loop.

Other hard constraints:

- **Mux window is not negotiated.** Firmware (`MUX_WINDOW`) and broker
  (`muxWindow`) both hard-code 32,768 bytes of initial credit per stream per
  direction, and the broker hard-codes 16 KB max DATA frames. With 54 KB free, a
  second 32 KB window allocation fails and the stream is RST. Shrinking the
  firmware side alone makes the broker "overrun" the window.
- `HELLO <token> <mac> <fw> <uptime> <ssid_b64> mux1 <max_streams>` is the only
  capability exchange. The broker already enforces `max_streams` per link
  (`serveMux`), defaulting to 24 when absent.
- OTA (`esp_https_ota` with the public CA bundle) opens a **second TLS session
  while the mux link and all streams stay live**; nothing is drained first. The
  S3 v1.2 outage (`mbedtls_ssl_setup -0x7F00`) was this failure mode under load.
- mbedTLS incoming record buffer stays 16 KB (TLS 1.2 peers, Cloudflare for OTA);
  outgoing may be 4 KB on C3.
- 4 MB flash: the stock 4 MB table (`partitions_example.csv`) leaves 576 KB
  unused; OTA slots can grow to 1,824 KB each with the 320 KB storage partition
  kept, or larger if storage is dropped.
- Build environment: IDF v5.5.2 on this PC (see memory `esp-idf-on-this-pc`);
  build from a short path under `D:\espbuild\`.
- Board quirks on Windows: esptool's default `hard_reset` leaves the C3 in ROM
  download mode — flash with `--after watchdog_reset`; the console needs the port
  opened with DTR/RTS asserted.
- The C3 Super Mini has a plain blue LED on GPIO8 (active-low), no WS2812.
  The firmware already supports this at runtime (`set_led_gpio`,
  `set_led_lowactive`); no driver work.

## 6. Decisions (as decided)

### 6.1 Feature set (operator's decisions, 2026-10-21)

| Feature | Decision | Mechanism |
|---|---|---|
| OLED display | **out** of exit-node builds (C3 and S3) | new Kconfig gate, default `y` for upstream, `n` in exit-node overlays |
| PCAP capture | **out** (C3 and S3) | same |
| mDNS responder | out (C3 and S3) — Claude's call, needed for RAM; nothing uses it on an exit node | same |
| MQTT Home Assistant | **kept compiled, runtime-off** (Phase 2) | existing `CONFIG_MQTT_HOMEASSISTANT` stays `y` |
| WireGuard | **kept compiled, runtime-off** (Phase 2) | no change |
| Management web UI (config, firewall, mappings, scan, VPN pages and their API handlers) | **out** (operator: "cut the management web UI entirely") | gate; see 6.2 |
| LED status | on, via runtime GPIO settings | provisioning defaults, no code |

Gates are compile-time so the upstream code stays intact for other targets; the
exit-node overlays (`sdkconfig.defaults.s3n16r8`, new `sdkconfig.defaults.c3mini`
or a shared `sdkconfig.defaults.exitnode`) turn them off.

### 6.2 WiFi provisioning without the web UI (Claude's call — confirm)

Customers still need a way to type their home WiFi credentials, and to change
them later without a PC. Decision: keep a **minimal setup portal** — one page
with SSID / password (and a status line), served by `esp_http_server` behind the
captive-portal DNS **only while the setup AP is up**. Once the STA uplink has an
IP (the moment AP auto-off fires) the HTTP server and captive DNS are stopped
and their memory returned; when the AP comes back after the uplink grace period,
they start again. During normal operation the node runs **no HTTP server at
all**. The console (`set_sta`) and the dashboard remain the operator's tools.

Applies to both C3 and S3 for a single customer experience (Claude's call —
confirm; S3 does not need the RAM).

### 6.3 Mux protocol: advertise window and frame size

Extend HELLO with optional `key=value` tokens after `mux1 <max_streams>`:

```
HELLO <token> <mac> <fw> <uptime> <ssid_b64> mux1 <max> win=<bytes> frame=<bytes> chip=<name>
```

- `win`: initial per-stream credit the **device** grants (broker → device
  direction). Broker uses it as that session's window; absent → 32768 (today's
  behaviour, so every deployed firmware keeps working). The device → broker
  direction stays 32 KB (broker RAM is not the constraint).
- `frame`: largest DATA payload the device accepts; absent → 16384.
- `chip`: `esp32c3` / `esp32s3`; used by the dashboard and OTA guard (6.6).
- Broker ignores unknown keys. Firmware constants `MUX_WINDOW`,
  `MUX_MAX_PAYLOAD`, `RPROXY_RELAY_BUF`, `MUX_RXCAP` become per-build Kconfig
  values (`CONFIG_RPROXY_MUX_WINDOW`, `CONFIG_RPROXY_MUX_FRAME`), S3 keeping
  32768 / 16384 and C3 starting at **8192 / 4096**.
- `MUX_GRANT_AT` scales with the window (grant at window/4).

### 6.4 rproxy memory on small boards

- Allocate the rx buffer and frame buffer when the link task **starts a
  connection**, free them when the link drops; nothing pre-allocated at boot.
- Dialer count becomes `CONFIG_RPROXY_DIALERS` (S3: 4, C3: 1). Dialer stack
  may shrink (measured use ~0.6 KB of 4.6 KB) — builder's call with margin.
- Stream count `CONFIG_RPROXY_MAX_STREAMS` for the first C3 release: **4**.
  Raise to 6 only when the acceptance soak (§8) shows ≥ 20 KB free heap at
  full load and OTA still succeeds.
- `CONFIG_LWIP_MAX_SOCKETS` on C3: streams + link + console/DNS margin (16 is
  enough for 4–6 streams once the web server is gone).

### 6.5 OTA drain (all targets)

Before `esp_https_ota` runs: stop accepting OPENs (answer with the existing
reject path), RST every live stream, free their windows, wait for the sockets
to close (bounded), and only then start the download. If free internal heap is
still below a threshold (builder to measure; the handshake needs ~45 KB plus
the CA bundle), drop the mux link for the duration. The broker does **not**
re-queue: `registry.takeOTA` clears the pending URL and marks the OTA "sent"
the moment it is delivered (and `main` also delivers a queued OTA to a live
link). So the device keeps the URL in RAM across the link drop and must not
reconnect (the HELLO TLS handshake would compete for the same heap) until
`esp_https_ota` has finished or failed; on failure it reconnects and the
broker marks the OTA "failed" when the old firmware version shows up again.
Status reporting to the broker is otherwise unchanged.

### 6.6 Two targets in the release pipeline

- `tools/fw_release.py` and `flasher/firmware/releases/` key releases by
  **version and target** (layout is the builder's call; `releases.json` must
  carry the target). The flasher page offers the image matching the chip
  esptool-js detects.
- The broker's OTA push (`setOTA`) refuses, or the admin API refuses, a URL
  whose target does not match the node's reported `chip`. Nodes that predate
  the `chip` token are treated as `esp32s3` (all deployed boards are S3).
- The dashboard device record shows the chip; the operator still sets
  `capacity` by hand as today (the admission rule is unchanged).

### 6.7 C3 build configuration

`sdkconfig.defaults.c3mini` (name is the builder's call), applied after
`sdkconfig.defaults` and `sdkconfig.defaults.esp32c3`, plus the shared
exit-node overlay and `sdkconfig.prov`:

- `-Os`; WiFi and lwIP IRAM optimisation off; WiFi pools as in spike v2 (tune
  with measurements, never above spike v2 values without a reason).
- No FAT/console history, no mDNS, no OLED, no PCAP, no management UI.
- CPU 160 MHz, USB-Serial/JTAG console, `MBEDTLS_SSL_OUT_CONTENT_LEN=4096`,
  incoming 16384.
- Custom 4 MB partition table with **OTA slots ≥ 1.75 MB each**; with the
  history filesystem gone the 320 KB `storage` partition can go too (builder's
  call; check nothing else mounts it).
- Rollback bootloader stays enabled (as on S3).
- Provisioning defaults for the C3 Super Mini LED (GPIO8, low-active) in the
  prov overlay or a C3-specific Kconfig default.

S3 N16R8 keeps `sdkconfig.defaults.s3n16r8` values (40 streams, 32 KB window,
16 KB frames, 64 KB TCP windows) and only inherits the feature gates, the OTA
drain and the HELLO tokens.

### 6.8 Code base, versions and rollout order

- **Base branch: `origin/main`** (7ed0329 at the time of writing). The
  checkout the spike was done in (`claude/project-thread-ceism1`, 7353d99) is
  an ancestor of `main`; `firmware/` is identical on both, but `main` carries
  33 further commits in `broker/`, `admin-api/` (SQLite store, device records
  with name / kind=shared|dedicated / slot / capacity, admission rule, Measure
  endpoint, slot-pinned routing with `SLOT_SECRET`, OTA delivered to a live
  link), `deploy/` and `flasher/`. All work lands on a branch from `main`;
  `main` is what Coolify runs.
- **Version:** the next firmware release is `2.4.22` for both targets. It
  supersedes `2.4.21-mux1.4` (status testing, never promoted); `latest` stays
  `2.4.21-mux1.2` until 2.4.22 passes acceptance. The S3 overlay for 2.4.22
  starts from the current `sdkconfig.defaults.s3n16r8` (mux1.4 settings: 16 KB
  frames, 16 KB outgoing TLS records, rproxy `-O2`). Backward-compatibility
  checks cover nodes on `2.4.21-mux1.2` and `2.4.21-mux1.4`.
- **Board inventory on the desk:** one S3 N16R8 (`e8:3d:c1:f2:86:bc`, COM7,
  slot 1) and the C3 Super Mini (`ac:a7:04:ba:ae:c0`, COM30). Any further S3
  boards are in the field and only reachable by OTA (operator to confirm how
  many and which are customer-bound). Before the soak the operator checks that
  the C3's MAC is not bound to a slot/customer on the dashboard (set it
  dedicated and unbound) so the admission rule cannot route paying users to it.

Rollout:

1. Broker + admin API (backward compatible: new HELLO tokens optional, OTA
   target guard, chip shown). Deploy on Coolify first, verify the COM7 S3 (on
   mux1.4) still links and the dashboard still works.
2. C3 firmware 2.4.22 as a **testing** release on the flasher; USB-flash the
   COM30 board; run the acceptance soak (§8).
3. S3 2.4.22 as **testing**: USB-flash the COM7 board, run its acceptance
   (§8) including an OTA to a rebuilt 2.4.22 image under 40-stream load (the
   drain path). Only then OTA field boards one at a time, watching the
   dashboard OTA status (auto-rollback is the safety net), and promote to
   `latest` last.

## 7. Left to the builder

- Exact Kconfig symbol names and overlay file names; whether the exit-node
  gates live in one shared overlay or per board.
- Partition CSV layout within the slot-size constraint.
- Task stack sizes (measure with `tasks` high-water marks), socket count,
  WiFi pool values within the spike v2 ceiling.
- Setup-portal page content and look (one form, plain HTML, no JS framework).
- How the HTTP server start/stop is wired to the existing AP policy task.
- Release directory layout and `releases.json` schema change; esptool-js chip
  detection details on the flasher page.
- Version string for the next release (`PROJECT_VER`), e.g. `2.4.22`.
- Soak test tooling (the dashboard Measure endpoint exists; a loop around it
  plus console `heap` polling is enough).

## 8. Acceptance

C3 (COM30 board, home uplink):

- Build fits with ≥ 10% of its OTA slot free.
- Free internal heap ≥ 20 KB with the link up and **all N streams moving data**
  for 30 minutes (dashboard Measure in a loop), no reboot, no `overran window`.
- OTA from the flasher succeeds under that load (drain path), and rollback
  still works (flash a build that cannot reach the broker; it must revert).
- Uplink loss for > grace period brings the AP and setup portal back; a new
  SSID/password entered there connects, portal and AP go away again.
- Throughput per node recorded (download/upload through the customer proxy);
  the operator decides the go/no-go floor (open question).

S3 N16R8 (COM7 board) with the same tree: links, 40 streams, speed within 10%
of `2.4.21-mux1.4` figures (9 down / 12 up Mbps), OTA drain works, old
`2.4.21-mux1.2` firmware still links to the new broker.

## 9. Phase 2 note (out of scope, recorded for later)

Keeping MQTT and WireGuard compiled costs ~39 KB flash and ~1.3 KB static RAM.
Running them is a separate design: WireGuard in this firmware is a **VPN
uplink** (all router traffic exits via the WG server), which would move the
customer's exit IP away from the home IP; a management tunnel needs policy
routing. On C3, rproxy + WireGuard + MQTT concurrently will not fit; Phase 2
on C3 may need to trade streams for it or be S3-only.

## 10. Open questions for the operator

Approved to proceed on 2026-10-21 ("ทำเลย"); the calls below stand unless the
operator says otherwise.

1. Minimum sellable capacity per C3 node (streams / Mbps). If the soak shows
   4 streams at 3–5 Mbps, is that a product?
2. 6.2 applied to **both** boards (S3 loses its management UI too; the flasher
   page's Thai steps 2–3, which send customers to the status page at
   192.168.4.1 / esp32-nat-router.local, get rewritten).
3. mDNS removed from exit-node builds.
4. How many S3 boards are in the field beyond the COM7 one, and which are
   customer-bound (decides the OTA canary order in 6.8 step 3)?
5. Is the dashboard's slot-2 device the C3 (`ac:a7:04:ba:ae:c0`)? If so,
   unbind it before the soak.

## 11. Acceptance results (2026-10-21, branch `claude/c3-exit-node`)

Setup: C3 Super Mini on COM30 (home uplink `IokWireless_2.4Ghz`, RSSI −57…−72 dBm),
a broker built from this branch running on the PC (192.168.1.114) with the
pinned-CA TLS certificate from `pki_out/`, customer traffic generated on the PC
through that broker's SOCKS5 (customer → broker → C3 → origin), the console
polled every 30 s. The production broker was not yet deployed, so "dashboard
Measure" was replaced by four parallel 25 MB HTTPS downloads from
speed.cloudflare.com (same path as a customer).

| §8 line | Result |
|---|---|
| C3 build fits with ≥ 10% slot free | **pass** — 1,215,632 B in a 1,984 KB slot (40% free) |
| ≥ 20 KB free heap with all 4 streams moving data for 30 min, no reboot, no `overran window` | **pass** — first config, 30 min: 60/60 samples link up, 58 with 4/4 streams, heap 68.8–143 KB (no downward trend after the first minute: 77 → 72 KB), 0 overrun on device and broker, 0 reboots; 377 MB relayed. **Shipped config, 10 min (final soak):** 20/20 link up, 18 with 4/4 streams, sampled heap 43–139 KB, console minimum **26.1 KB**, 0 overrun, 0 reboots, ~250 MB relayed at ~2.7 Mbps aggregate. The 30-minute run on the shipped config is the operator's (Task 11 step 2). |
| Throughput per node | first soak **1.76 Mbps aggregate (≈0.44 Mbps per stream)**; after the fixes in §11.1: **single stream 1.73 Mbps, four streams 2.7 Mbps aggregate** (final soak below) |
| OTA under load succeeds (drain path), rollback still works | **pass (drain + OTA)**: twice through the local broker — `OTA requested` → `draining` → `mux link down` → `link closed, 180 KB free` → download 8 s → reboot → probation → `confirmed`, broker `ota_status: ok`, slots alternate ota_1/ota_0. Unreachable URL → `OTA: failed`, link back in 1 s, no reboot. **Rollback (image that cannot reach the broker) not exercised** — operator, Task 11 step 3. |
| Uplink loss > grace brings AP + portal back; new SSID connects; both go away again | **pass** (Task 5): `uplink down 90s: turning setup AP back on`, httpd task present; after `set_sta` to the real SSID: `got ip` → `stopping the setup portal`, httpd gone. The portal page itself was not opened from a phone — operator. |
| S3 N16R8 regression (link, 40 streams, speed, OTA drain, old firmware vs new broker) | **not run** — the S3 board was not plugged in during this session (only COM30 enumerated). Image 2.4.22 esp32s3 (1,264,208 B) is archived; operator runs step 5 on COM7. Old-firmware compatibility is covered by the broker tests (`TestMuxLegacyHelloDefaults`) and by the production broker accepting the 2.4.22 HELLO. |

Other evidence: the pre-token production broker accepts the 2.4.22 HELLO
(link up in 3.4 s, TLS); a broker from this branch logs `max 4 streams, win
8192, frame 4096, chip "esp32c3"`; OTA target guard returns 409 for an S3
image pushed to the C3.

### 11.1 Why the first soak was slow, and what changed

The first 30-minute soak relayed 377 MB without a hiccup but at 0.44 Mbps per
stream, although the board's own ping to the origin was 7–13 ms and RSSI was
−59 dBm. Measured step by step (single 10 MB download / four 5 MB downloads
through the local TLS broker, heap floor from the console):

| Change (cumulative) | single | 4 streams | heap floor |
|---|---|---|---|
| as archived first (TCP wnd 5,760 B) | 0.33 Mbps | 1.76 Mbps | 68.8 KB |
| TCP wnd 16 KB | — | 1.22 Mbps | 67.7 KB |
| + node→broker send credit back to 32 KB (`MUX_SEND_CREDIT`; the Kconfig window had also been used as the send credit, a deviation from §6.3) | 0.51 | 1.45 | — |
| + `TCP_NODELAY` on the link socket (a 16-byte credit sliver was held by Nagle for the peer's delayed ACK) | 0.22 | 1.2 | 29 KB |
| + receive pools (WiFi static RX 8, dynamic RX 24, RX BA window 16, lwIP TCP recvmbox 32, tcpip mbox 48) | 0.84 | 2.4 | 9.5 KB |
| + **WiFi power save off** (`esp_wifi_set_ps(WIFI_PS_NONE)` when rproxy starts; PC→board ping fell from 87 ms to 7 ms) | 1.39 | 2.5 | 10.5 KB |
| TCP wnd back to 8 KB, link send buffer 16 KB (**shipped**) | **1.73** | **2.72** | **26.3 KB** |

The dominant cause was WiFi modem power save: the station only listened every
DTIM, so every inbound ACK and WINDOW grant from the broker arrived ~100 ms
late and the one link's in-flight window per RTT capped the whole board. The
16 KB receive windows cost ~40 KB of heap at full load and were not needed
once latency was 7 ms. The remaining ceiling (~2.7 Mbps aggregate, ~1.7 Mbps
single) is most likely the single 160 MHz core doing TLS for the link plus
four TCP streams; it is the number the operator prices a C3 node on.
Power save off also applies to the S3 (same code path); its 9–12 Mbps figure
should be re-measured and may rise.
