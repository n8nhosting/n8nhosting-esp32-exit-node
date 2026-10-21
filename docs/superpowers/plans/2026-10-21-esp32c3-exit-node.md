# ESP32-C3 Exit Node Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship firmware `2.4.22` for the ESP32-C3 Super Mini and the ESP32-S3 N16R8 from one tree, with a broker/dashboard/flasher that know two board types.

**Architecture:** The broker learns three optional HELLO tokens (`win=`, `frame=`, `chip=`) and sizes each mux session from them, so a 400 KB-SRAM board can run small windows while deployed S3 firmware keeps today's 32 KB / 16 KB defaults. The firmware gains compile-time feature gates (OLED, PCAP, mDNS, management web UI), a setup-portal-only web mode tied to the setup AP's lifecycle, per-build rproxy sizes with buffers allocated only while a link exists, and an OTA path that drains streams and drops the link before downloading. The release tool, flasher and dashboard become target-aware (`esp32s3` / `esp32c3`).

**Tech Stack:** ESP-IDF v5.5.2 (C, Kconfig, CMake), Go 1.24 (broker, admin-api, `go test`), vanilla JS dashboard/flasher (ESP Web Tools 10), Python 3 stdlib (`tools/fw_release.py`, `unittest`).

**Spec:** `docs/superpowers/specs/2026-10-21-esp32c3-exit-node-design.md`

## Global Constraints

- Branch `claude/c3-exit-node` from `origin/main` (7ed0329). Firmware is identical to the spike checkout; broker/admin-api/flasher code is **main's**, not the older `claude/project-thread-ceism1` tree.
- Every broker change is backward compatible with nodes on `2.4.21-mux1.2` and `2.4.21-mux1.4`: HELLO without tokens ⇒ window 32768, frame 16384, chip "" (treated as `esp32s3`).
- S3 N16R8 values stay: 40 streams, window 32768, frame 16384, 4 dialers, `sdkconfig.defaults.s3n16r8` otherwise unchanged.
- C3 first-release values: 4 streams, window 8192, frame 4096, 1 dialer, 16 lwIP sockets, OTA slots ≥ 1,792 KB, `-Os`, WiFi/lwIP IRAM optimisation off.
- Both targets are built from `D:\espbuild\<mirror>` (paths with spaces break IDF) with IDF v5.5.2 activated via `tools/activate.py` (see memory `esp-idf-on-this-pc`); never with IDF 6.x.
- Flash the C3 with `--after watchdog_reset`; open its console with DTR/RTS asserted (pyserial defaults). The S3 is COM7, the C3 is COM30.
- New Kconfig gates default to today's behaviour (`y` for features that exist now, `n` for new modes) so other upstream targets build unchanged; exit-node overlays flip them.
- `PROJECT_VER` becomes `2.4.22`; `latest` on the flasher stays `2.4.21-mux1.2` until acceptance (Task 11) passes.
- Do not hand-edit `flasher/firmware/releases/` or `releases.json`; only `tools/fw_release.py` writes them.
- Commit after every task with the attribution line `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`. No pushes/deploys without the operator.

## Review Focus

1. A legacy node (HELLO with no tokens, or an unknown token) must get window 32768 / frame 16384 and relay a 1 MB body exactly as today → Task 1 `TestMuxLegacyHelloDefaults`.
2. A malformed token (`win=abc`, `win=0`, `frame=99999`, `chip=../x`) must fall back to defaults, never panic or produce a zero window that deadlocks the stream → Task 1 table test cases.
3. OTA pushed to a node whose chip does not match the image's target must be refused before anything is queued, and a node that never sent `chip` must still accept an S3 image → Task 2 `TestOTATargetGuard`.
4. OTA arriving while streams are active: the device must RST streams, drop the link, download, and either reboot into the new image or come back on the old one; the broker must end in `ok` or `failed`, never stuck in `sent` with a live link → Task 7 device check + existing `TestOTAStatusTracking`.
5. Uplink lost for longer than the grace period: the setup AP and portal must come back, and after a successful reconnect both must go away again with the HTTP server task gone → Task 5 device check.

---

### Task 1: Broker — HELLO capability tokens and per-session window/frame

**Files:**
- Create: `broker/hello.go`, `broker/hello_test.go`
- Modify: `broker/main.go` (HELLO parse ~L215–245, `serveMux` ~L267–300), `broker/mux.go` (`muxSession`, `newMuxSession`, `open`, `Write`), `broker/registry.go` (`node`, `snapshot`, `touch`)
- Modify tests: `broker/mux_test.go` (`runMuxNode`, `testMuxNode`), `broker/broker_test.go` and `broker/slot_test.go` (`touch` callers)

**Interfaces:**
- Produces: `type helloOpts struct { MaxStreams, Window, Frame int; Chip string }`; `func parseHelloOpts(f []string) helloOpts` (f = `strings.Fields` of the HELLO line); `func newMuxSession(c net.Conn, br *bufio.Reader, n *node, o helloOpts) *muxSession` with fields `window, frame int`; `node.Chip string \`json:"chip,omitempty"\``; `func (r *registry) touch(mac, fw, ip string, uptime uint64, ssid, chip string) *node`.
- Constants: `helloDefaultWindow = 32768`, `helloDefaultFrame = 16384`, window accepted in `[4096, 262144]`, frame in `[1024, 16384]`, chip must match `^[a-z0-9]{1,16}$`.

- [ ] **Step 1: Write `broker/hello_test.go`**

```go
func TestParseHelloOpts(t *testing.T) {
	cases := []struct{ line string; want helloOpts }{
		{"HELLO t m fw 1 - mux1 40", helloOpts{40, 32768, 16384, ""}},
		{"HELLO t m fw 1 - mux1 4 win=8192 frame=4096 chip=esp32c3", helloOpts{4, 8192, 4096, "esp32c3"}},
		{"HELLO t m fw 1 - mux1 4 frame=4096 win=8192", helloOpts{4, 8192, 4096, ""}},
		{"HELLO t m fw 1 - mux1 4 win=abc frame=0 chip=../x", helloOpts{4, 32768, 16384, ""}},
		{"HELLO t m fw 1 - mux1 4 win=1 frame=99999", helloOpts{4, 32768, 16384, ""}},
		{"HELLO t m fw 1 - mux1 4 bogus=1 chip=ESP32C3", helloOpts{4, 32768, 16384, "esp32c3"}},
		{"HELLO t m fw 1 - mux1", helloOpts{0, 32768, 16384, ""}},
	}
	for _, c := range cases {
		if got := parseHelloOpts(strings.Fields(c.line)); got != c.want {
			t.Errorf("%q: got %+v want %+v", c.line, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run it** — `cd broker && go test -run TestParseHelloOpts ./...` → FAIL: `undefined: parseHelloOpts`.

- [ ] **Step 3: Implement `broker/hello.go`**: `parseHelloOpts` reads `f[7]` as MaxStreams (Atoi, 0 on error) and every `f[8:]` token of the form `key=value`; unknown keys ignored; out-of-range or unparsable values keep the default; chip lower-cased then validated. Also move the comment block that documents the HELLO line (mux.go header / rproxy.h) to mention the tokens.

- [ ] **Step 4: Thread it through**: in `main.go` replace the `maxStreams` parsing with `o := parseHelloOpts(f)`; `touch(..., ssid, o.Chip)`; `serveMux(c, br, n, o)`; log `mux link up: %s (max %d streams, win %d, frame %d, chip %q)`. In `mux.go`: `newMuxSession(c, br, n, o)` sets `maxStreams` (24 when ≤ 0, as today), `window`, `frame`; `open()` uses `credit: s.window`; `Write()` uses `min(len(p), st.credit, s.frame)`. Leave `muxWindow`/`muxGrantAt` for the node→broker direction untouched. In `registry.go`: add `Chip` to `node` and `snapshot()`; `touch` stores a non-empty chip. Update the three test callers of `touch` with `""`.

- [ ] **Step 5: Extend the test node** in `mux_test.go`: `runMuxNode(..., maxStreams int, opts string, ...)` appends `opts` (e.g. `" win=8192 frame=4096 chip=esp32c3"`) to the HELLO line; `testMuxNode` records `maxFrame int` (largest DATA payload seen) and `maxInflight int` (bytes received on a stream minus credit granted back, peak). Existing callers pass `""`.

- [ ] **Step 6: Write the integration tests** in `mux_test.go`:

```go
func TestMuxSmallWindowNodeNeverOverrun(t *testing.T) {
	// node advertises win=8192 frame=4096; fetch a 1 MiB body through it
	// assert node.maxFrame <= 4096 && node.maxInflight <= 8192
	// assert b.muxes[0].window == 8192 && b.muxes[0].frame == 4096
	// assert b.reg.nodes[mac].Chip == "esp32c3" after HELLO with chip=esp32c3
}
func TestMuxLegacyHelloDefaults(t *testing.T) {
	// node HELLO without tokens; same 1 MiB fetch succeeds
	// assert b.muxes[0].window == 32768 && b.muxes[0].frame == 16384 && node.Chip == ""
}
```
Reuse `spinMuxBroker`, `originServer` (its big-body route is what `TestMuxChainReachabilityAndBigBody` fetches) and `fetchVia`.

- [ ] **Step 7: Run** `go test ./...` in `broker/` → all PASS (existing 34 tests + 3 new).

- [ ] **Step 8: Commit** — `git add broker && git commit -m "broker: HELLO win/frame/chip tokens size each mux session"`.

---

### Task 2: Broker — OTA target guard

**Files:**
- Modify: `broker/main.go` (`/nodes/{mac}/ota` handler ~L663–700), `broker/registry.go`
- Test: `broker/broker_test.go`

**Interfaces:**
- Consumes: `node.Chip` (Task 1).
- Produces: OTA request body `{"url","version","target"}`; `func (r *registry) chipOf(mac string) (chip string, ok bool)` returning `"esp32s3"` for a node with empty Chip; HTTP 409 `{"error":"target mismatch","chip":"<node chip>"}` when `target != ""` and differs.

- [ ] **Step 1: Write `TestOTATargetGuard`** in `broker_test.go`: spin a broker (`spinMuxBroker` or the status mux directly via `b.statusHandler()` — follow `TestOTAStatusTracking`), `touch("aa:…:01", "", "", 0, "", "esp32c3")` and `touch("aa:…:02", "", "", 0, "", "")`; POST `/nodes/aa…01/ota` `{url:"https://x/fw.bin",target:"esp32s3"}` → 409 and `PendingOTA == ""`; same with `target:"esp32c3"` → 200 and `PendingOTA` set; node 02 with `target:"esp32s3"` → 200; node 02 with `target:"esp32c3"` → 409; node 01 with no target → 200 (legacy dashboard).

- [ ] **Step 2: Run** `go test -run TestOTATargetGuard ./...` → FAIL (status 200 where 409 expected).

- [ ] **Step 3: Implement**: add `Target string \`json:"target"\`` to the body struct; `chipOf`; the guard before `setOTA`; keep the existing length checks.

- [ ] **Step 4: Run** `go test ./...` → PASS.

- [ ] **Step 5: Commit** — `git commit -am "broker: refuse an OTA whose target does not match the node's chip"`.

---

### Task 3: Admin API + dashboard — chip column and chip-aware OTA picker

**Files:**
- Modify: `admin-api/devices.go` (`brokerNode`), `admin-api/dashboard/index.html` (devices view ~L362–445, `openFw` ~L468–504, `otaAll`), `admin-api/dashboard_test.go`, `admin-api/devices_test.go`

**Interfaces:**
- Consumes: broker `/nodes` JSON `chip`; `releases.json` entries with `targets` (Task 9 format: `targets: { "<target>": { "url","size","sha256","built" } }`; legacy entries without `targets` are S3-only).
- Produces: `/api/devices[].chip` (empty for legacy nodes; the dashboard displays `esp32s3` then); OTA POST body `{url, version, target}`.

- [ ] **Step 1: Tests first.** In `devices_test.go` extend `fakeBroker.nodes` in one existing test (e.g. `TestDeviceListWithoutRow`) with `"chip":"esp32c3"` and assert `/api/devices` returns `chip == "esp32c3"`. In `dashboard_test.go` add to the `want` list: `` `n.chip||'esp32s3'` ``, `` `targets[chip]` ``, `` `target: chip` `` and remove nothing.

- [ ] **Step 2: Run** `cd admin-api && go test ./...` → FAIL on the new assertions.

- [ ] **Step 3: Implement**: `Chip string \`json:"chip"\`` in `brokerNode`. Dashboard: heading `Exit devices`; a `<span class="pill off">${esc(n.chip||'esp32s3')}</span>` under the MAC in the first cell; `openFw(macs)` — for each release build the option label with the targets it has (`[esp32s3, esp32c3]`); on **Go**, for each MAC: `const chip = (window._devs[mac]||{}).chip||'esp32s3'; const tgt = r.targets ? r.targets[chip] : (chip==='esp32s3' ? {url:r.url} : null);` — no `tgt` ⇒ push `mac+': no '+chip+' image in '+r.version` to `failed`; else POST `{url: flashBase()+'/'+tgt.url, version, target: chip}`. Custom URL keeps working (target = the device's chip).

- [ ] **Step 4: Run** `go test ./...` → PASS. Open the dashboard locally (`go run .` with a fake `BROKER_STATUS_URL`, or trust the tests) only if a visual check is cheap.

- [ ] **Step 5: Commit** — `git commit -am "admin-api: show the board chip and pick OTA images per chip"`.

---

### Task 4: Firmware — compile-time gates for OLED, PCAP, mDNS

**Files:**
- Modify: `firmware/main/Kconfig.projbuild`, `firmware/main/CMakeLists.txt`, `firmware/main/esp32_nat_router.c` (L66, L70, L1670–1690, L1697, L1710), `firmware/main/netif_hooks.c` (L23 and every `pcap_should_capture`/`pcap_capture_packet` site: 150–160, 198–237, 515–571, 595–617), `firmware/components/cmd_router/cmd_router.c` (pcap: L45, L1990–2120 and its `register_` call; oled: L52, L105, L469, L3619–3709), `firmware/components/http_server/http_server.c` (pcap: L43, L1435–1442, L1995–2010, L2157–2166, L2249–2251)
- Create: `firmware/sdkconfig.defaults.exitnode`
- Modify: `firmware/build_s3n16r8.sh` (DEFS gains `;sdkconfig.defaults.exitnode` before `sdkconfig.prov`)

**Interfaces:**
- Produces Kconfig symbols (menu "Exit node build" in `Kconfig.projbuild`): `CONFIG_FEATURE_OLED` (bool, default y), `CONFIG_FEATURE_PCAP` (default y), `CONFIG_FEATURE_MDNS` (default y, `depends on !IDF_TARGET_ESP32C5`), `CONFIG_WEB_SETUP_PORTAL_ONLY` (default n, used by Task 5), `CONFIG_LED_GPIO_DEFAULT` (int, default -1), `CONFIG_LED_LOWACTIVE_DEFAULT` (bool, default n) (used by Task 8).
- `sdkconfig.defaults.exitnode` sets `FEATURE_OLED=n`, `FEATURE_PCAP=n`, `FEATURE_MDNS=n`, `WEB_SETUP_PORTAL_ONLY=y`.

- [ ] **Step 1: Baseline** — record `esp_idf_size --archives` for the current S3 build (`firmware/build_s3n16r8/esp32_nat_router.map`): `liboled_display.a`, `libpcap_capture.a`, `libespressif__mdns.a` present; app 1,395,216 B.

- [ ] **Step 2: Kconfig + CMake**: add the menu; in `main/CMakeLists.txt` build `main_priv_requires` without `pcap_capture` / `oled_display` / `espressif__mdns` when the matching `CONFIG_FEATURE_*` is off (`if(CONFIG_FEATURE_PCAP)` works in component CMake since sdkconfig is loaded first). The existing ESP32-C5 mDNS `#if` becomes the gate's `depends on`.

- [ ] **Step 3: Wrap the code**: each include and call site listed above under `#if CONFIG_FEATURE_PCAP` / `#if CONFIG_FEATURE_OLED` / `#if CONFIG_FEATURE_MDNS`. In `http_server.c` the config page's printf arguments stay aligned: when PCAP is off substitute constant strings (`""`, `"#888"`, `"n/a"`, `0`) in the same positions, or wrap the whole PCAP section of the template and its args together — whichever keeps one `#if` per block. `cmd_router.c`: the `pcap` command and both `set_oled*` commands are not registered when off.

- [ ] **Step 4: Build S3 both ways** from the mirror (`D:\espbuild\fw-c3` is the new mirror of this branch; `robocopy` the `firmware/` tree there excluding `build_*`): (a) with `DEFS` as before → image byte-identical in size class to Step 1 (gates default y); (b) with `sdkconfig.defaults.exitnode` → `esp_idf_size --archives` lists none of the three archives and the app is ≥ 40 KB smaller. Build the C3 the same way (`set-target esp32c3`, `sdkconfig.defaults;sdkconfig.defaults.esp32c3;sdkconfig.defaults.exitnode;sdkconfig.prov`) → links.

- [ ] **Step 5: Commit** — `git add firmware && git commit -m "firmware: Kconfig gates for OLED, PCAP and mDNS; exit-node overlay turns them off"`.

---

### Task 5: Firmware — setup-portal-only web UI tied to the setup AP

**Files:**
- Modify: `firmware/components/http_server/http_server.c` (`start_webserver` L3785–3868, `stop_webserver` L3870, `dns_server_task` L3880–3943, `web_server_start_captive_dns` L3945), `firmware/components/http_server/include/http_server.h`, `firmware/components/rproxy/rproxy.c` (`ap_policy_task` L899–917), `firmware/components/rproxy/CMakeLists.txt` (REQUIRES + `http_server`), `firmware/main/esp32_nat_router.c` (web server start L1657–1667 — unchanged behaviour, only comment)

**Interfaces:**
- Consumes: `CONFIG_WEB_SETUP_PORTAL_ONLY` (Task 4).
- Produces in `http_server.h`: `void web_server_set_setup_mode(bool on);` (on: start the server on the port saved at the last `start_webserver` call, default 80, plus captive DNS when no STA SSID is set; off: stop the server and the DNS task; idempotent) and `bool web_server_is_running(void);`.
- Behaviour: in portal-only builds `start_webserver` registers only `setupp` (GET `/setup`), a second `httpd_uri_t setup_rootp` (GET `/`, same `setup_get_handler`), `scanp`, `favicon_uri`, and the 404 captive redirect; `config.stack_size = 8192`, `max_uri_handlers = 6`. All other handlers (`configp`, `mappingsp`, `firewallp`, `vpnp`, `config_exportp`, `config_importp`, `vpn_importp`, `ota_uploadp`, `indexp`, `indexp_post`) are compiled out with the gate.

- [ ] **Step 1: Make the DNS task stoppable**: keep `static int s_dns_sock = -1;` and `static TaskHandle_t s_dns_task;` — `web_server_start_captive_dns` is a no-op when the task exists; `dns_server_task` stores the socket, and on `recvfrom < 0 && s_dns_sock < 0` (socket closed by the stopper) frees and exits. `static void web_server_stop_captive_dns(void)` closes the socket (`shutdown`+`close`, set to -1) and waits (bounded 500 ms) for the task to clear `s_dns_task`.

- [ ] **Step 2: Lifecycle**: `static httpd_handle_t s_server; static uint16_t s_port = 80;` set in `start_webserver`; implement `web_server_set_setup_mode` and `web_server_is_running` (always compiled; in non-portal builds `set_setup_mode` still works but nothing calls it).

- [ ] **Step 3: Portal-only registration** under `#if CONFIG_WEB_SETUP_PORTAL_ONLY` in `start_webserver` (an `#else` keeps the current list). The setup page already posts to `/setup` and links `/scan`, `/` and `favicon.png`, so no template change.

- [ ] **Step 4: Hook the AP policy** in `rproxy.c` `ap_policy_task`: next to `ap_set_enabled(false)` add `#if CONFIG_WEB_SETUP_PORTAL_ONLY web_server_set_setup_mode(false); #endif`, and next to `ap_set_enabled(true)` the `true` call. Add `http_server` to rproxy's `REQUIRES`.

- [ ] **Step 5: Build** S3 (exit-node overlay) and C3 → both link; `esp_idf_size --archives` for `libhttp_server.a` flash drops by > 15 KB on the exit-node build.

- [ ] **Step 6: Device check (C3, COM30, from the Task 4 mirror build + this task)**: flash (`--after watchdog_reset`), console `tasks` right after boot shows `httpd` and `dns_srv`; ~15 s after `got ip` the log shows `uplink up: turning setup AP off` and `tasks` shows neither; `heap` is ≥ 15 KB higher than spike v2's 54 KB figure at link-up. Then `set_sta WrongSSID x` + `restart`; after 90 s the log shows the AP coming back and `tasks` shows `httpd` again; `set_sta IokWireless_2.4Ghz <pw>` + `restart` returns to the first state. From a phone on `ESP32_NAT_Router`, `http://192.168.4.1/` shows the Getting Started form and `/config` returns 404.

- [ ] **Step 7: Commit** — `git commit -am "firmware: setup-portal-only web UI that lives and dies with the setup AP"`.

---

### Task 6: Firmware — rproxy sizes from Kconfig, HELLO tokens, buffers only while linked

**Files:**
- Modify: `firmware/components/rproxy/Kconfig`, `firmware/components/rproxy/include/rproxy.h` (L46 `RPROXY_RELAY_BUF`), `firmware/components/rproxy/rproxy.c` (L384–390 constants, `start_pool` L920–938, `mux_task` L727–806, HELLO L763–766, `rproxy_get_status`/`cmd_rproxy` status print), `firmware/sdkconfig.defaults.s3n16r8` (explicit `CONFIG_RPROXY_MUX_WINDOW=32768`, `CONFIG_RPROXY_MUX_FRAME=16384`, `CONFIG_RPROXY_DIALERS=4`)

**Interfaces:**
- Produces Kconfig: `RPROXY_MUX_WINDOW` (int, range 4096 65536, default 32768), `RPROXY_MUX_FRAME` (int, range 1024 16384, default 16384), `RPROXY_DIALERS` (int, range 1 4, default 4), `RPROXY_DIALER_STACK` (int, default 4608).
- Macros: `MUX_WINDOW = CONFIG_RPROXY_MUX_WINDOW`, `MUX_MAX_PAYLOAD = CONFIG_RPROXY_MUX_FRAME`, `MUX_GRANT_AT = MUX_WINDOW / 4`, `MUX_RXCAP = MUX_HDR + MUX_MAX_PAYLOAD + 2048`, `RPROXY_RELAY_BUF = CONFIG_RPROXY_MUX_FRAME - 8` (header), `MUX_DIALERS = CONFIG_RPROXY_DIALERS`.
- HELLO line: `"HELLO %s %s %s %lu %s mux1 %d win=%d frame=%d chip=%s\n"` with `CONFIG_IDF_TARGET` as chip (matches Task 1 parsing and validation).
- `rproxy_status_t` gains `uint32_t window, frame;` printed by `rproxy status` as `  mux:      window 8192 B, frame 4096 B, 1 dialer(s)`.

- [ ] **Step 1: Kconfig + macros** as above; `s_res_q` depth `RPROXY_MAX_STREAMS + MUX_DIALERS`; dialer tasks created with `CONFIG_RPROXY_DIALER_STACK`.

- [ ] **Step 2: Lazy buffers**: remove the `s_fb` allocation from `start_pool` and the `rx` allocation from the top of `mux_task`; inside the loop, after `link_connect` succeeds and before HELLO, allocate `rx` (`MUX_RXCAP`) and `s_fb` (`MUX_HDR + MUX_MAX_PAYLOAD`) with the existing PSRAM-then-internal fallback; on failure log `mux: no memory for link buffers`, `link_close`, `backoff_delay`, `continue`; free both right after `link_close` on the way down (`s_fb = NULL`). `st_pump` only runs inside `mux_serve`, so `s_fb` is always valid there.

- [ ] **Step 3: HELLO + status** as in Interfaces; the `mux link up` log line adds `win %d frame %d`.

- [ ] **Step 4: Build** S3 and C3 → link. S3 image behaviour unchanged (same numbers).

- [ ] **Step 5: Device check against a local broker (no deploy needed)**: on the PC run `cd broker && set POOL_TOKEN=<token from firmware/sdkconfig.prov> && set NODES_FILE=%TEMP%\nodes.json && go run .` (listens `:7000`, status `:7001`); on the C3 console `rproxy server <PC LAN IP> 7000`, `rproxy tls off`, `restart`. Expected: broker log `mux link up: ac:a7:04:ba:ae:c0 (max 4 streams, win 8192, frame 4096, chip "esp32c3")`; `curl http://localhost:7001/nodes` shows `"chip":"esp32c3"`; `heap` on the C3 with the link up is ≥ 25 KB higher than spike v2's 54 KB. Restore `rproxy server proxy.jbacteam.com 7000`, `rproxy tls on` afterwards (the production broker ignores the tokens until Task 1 is deployed — the link still comes up).

- [ ] **Step 6: Commit** — `git commit -am "rproxy: window/frame/dialers from Kconfig, advertised in HELLO; link buffers allocated per connection"`.

---

### Task 7: Firmware — OTA drains streams and drops the link first

**Files:**
- Modify: `firmware/components/rproxy/rproxy.c` (`handle_frame` F_OTA branch L580–585, `mux_serve` loop, `mux_task` after `mux_serve`, `start_ota`/`ota_task` L862–896, `handle_open` busy path)

**Interfaces:**
- State: `static char s_ota_url[256]; static volatile bool s_ota_wanted;` (set by F_OTA), existing `s_ota_busy`.
- Behaviour (all targets, builder's call: always drop the link rather than threshold on heap — one path to test): F_OTA stores the URL and sets `s_ota_wanted`; `mux_serve` on seeing it logs `OTA: draining %d streams`, answers any further OPEN with `OPEN_ERR busy`, RSTs and frees every stream (`mux_reset_streams`), then returns; `mux_task` closes the link, frees the link buffers, calls `start_ota(s_ota_url)` and loops `vTaskDelay(500 ms)` while `s_ota_busy` before trying to reconnect; `ota_task` clears `s_ota_wanted` on failure. `start_ota` is no longer called from `handle_frame`.

- [ ] **Step 1: Implement** as described. The `OTA requested by broker` log stays; add `OTA: link closed, %u B internal heap free` before `start_ota`.

- [ ] **Step 2: Build** S3 and C3.

- [ ] **Step 3: Device check (C3 against the local broker from Task 6 Step 5)**: with the link up, `curl -X POST localhost:7001/nodes/ac:a7:04:ba:ae:c0/ota -d '{"url":"https://flash.jbacteam.com/firmware/releases/2.4.21-mux1.4/esp32_nat_router.bin","target":"esp32c3"}'` → 409 (guard, Task 2). Repeat with `"target":"esp32s3"` on a node whose chip is `esp32c3` → 409. Then push a C3 image URL (serve the Task 6 build from the PC: `python -m http.server 8080` in the build dir; URL `http://<pc>:8080/esp32_nat_router.bin` — `esp_https_ota` accepts http) → console shows `OTA: draining`, `mux link down`, `OTA: downloading`, reboot, link back up; broker `/nodes` shows `ota_status: ok` once the firmware string differs (bump `PROJECT_VER` to `2.4.22-dev` for this test image so it differs). Then push a deliberately unreachable URL → `OTA: failed`, link comes back within 30 s, no reboot.

- [ ] **Step 4: Commit** — `git commit -am "rproxy: drain streams and drop the link before an OTA download"`.

---

### Task 8: Firmware — C3 Super Mini overlay, partition table, LED defaults, build script, version

**Files:**
- Create: `firmware/sdkconfig.defaults.c3mini`, `firmware/partitions_c3_4mb.csv`, `firmware/build_c3mini.sh`
- Modify: `firmware/main/esp32_nat_router.c` (LED load L1360–1364), `firmware/CMakeLists.txt` (`PROJECT_VER "2.4.22"`), `firmware/README.md` (build section), `firmware/.gitignore` if build dirs need it

**Interfaces:**
- `partitions_c3_4mb.csv`: `nvs 0x9000 24K; otadata 0xf000 8K; phy_init 0x11000 4K; ota_0 0x20000 1984K; ota_1 , 1984K` (no `storage`: history FS is off in exit-node builds; 0x20000 + 2×1984K = 4,063,232 B ≤ 4 MiB). Both slots ≥ 1,792 KB as the spec requires.
- `sdkconfig.defaults.c3mini` (applied after `sdkconfig.defaults`, `sdkconfig.defaults.esp32c3`, `sdkconfig.defaults.exitnode`): spike v2 values (`COMPILER_OPTIMIZATION_SIZE=y`/`PERF=n`, `ESP_WIFI_IRAM_OPT=n`, `ESP_WIFI_RX_IRAM_OPT=n`, `LWIP_IRAM_OPTIMIZATION=n`, `ESP_WIFI_STATIC_RX_BUFFER_NUM=6`, `ESP_WIFI_DYNAMIC_RX_BUFFER_NUM=16`, `ESP_WIFI_DYNAMIC_TX_BUFFER_NUM=16`, `ESP_WIFI_TX_BA_WIN=6`, `ESP_WIFI_RX_BA_WIN=6`, `ESP_WIFI_MGMT_SBUF_NUM=10`, `STORE_HISTORY=n`, `LWIP_DHCPS_MAX_STATION_NUM=4`) plus `RPROXY_MAX_STREAMS=4`, `RPROXY_MUX_WINDOW=8192`, `RPROXY_MUX_FRAME=4096`, `RPROXY_DIALERS=1`, `RPROXY_DIALER_STACK=3584`, `LWIP_MAX_SOCKETS=16`, `LWIP_MAX_ACTIVE_TCP=24`, `MBEDTLS_SSL_OUT_CONTENT_LEN=4096`, `PARTITION_TABLE_CUSTOM_FILENAME="partitions_c3_4mb.csv"`, `LED_GPIO_DEFAULT=8`, `LED_LOWACTIVE_DEFAULT=y`.
- `build_c3mini.sh`: mirror of `build_s3n16r8.sh` with `DEFS="sdkconfig.defaults;sdkconfig.defaults.esp32c3;sdkconfig.defaults.exitnode;sdkconfig.defaults.c3mini"` (+ prov), `-B build_c3mini`, `set-target esp32c3`, and a printed reminder to flash with `--after watchdog_reset`.
- LED: when `get_config_param_int("led_gpio")` finds nothing, `led_gpio = CONFIG_LED_GPIO_DEFAULT; led_lowactive = CONFIG_LED_LOWACTIVE_DEFAULT;`.

- [ ] **Step 1: Write the three files and the LED default; set `PROJECT_VER` to `2.4.22`.** Add `build_c3mini` to `.gitignore` next to the S3 build dir pattern if one exists.

- [ ] **Step 2: Build both** (`build_s3n16r8.sh`, `build_c3mini.sh` from the mirror). Expected: C3 app ≤ 1,400 KB with ≥ 25% of its 1,984 KB slot free; S3 app within ±20 KB of 1,350 KB (gates applied) in its 3 MB slot. Record `esp_idf_size` summaries for both in the commit message.

- [ ] **Step 3: Device check (C3)**: flash all four images from `build_c3mini` (`--after watchdog_reset`); boot log shows `heap_init` regions totalling ≥ 300 KB, `LED status on GPIO 8 (low-active)`, blue LED behaviour visible; `heap` idle ≥ 110 KB; against the local broker (Task 6 Step 5) link up with `win 8192 frame 4096`, `heap` ≥ 70 KB with the link up. Record the numbers in `docs/superpowers/specs/2026-10-21-esp32c3-exit-node-design.md` §5 as a third column.

- [ ] **Step 4: Device check (S3, COM7)**: USB-flash `build_s3n16r8` (close any console holding COM7; the port re-enumerates on reset): links to the production broker (tokens ignored by the old broker, link still up), 40 streams advertised, `tasks` shows no `httpd` after uplink, web UI absent at the STA IP, `rproxy status` shows `window 32768 B, frame 16384 B, 4 dialer(s)`.

- [ ] **Step 5: Commit** — `git add firmware && git commit -m "firmware 2.4.22: ESP32-C3 Super Mini build (overlay, 4 MB table, LED defaults)"`.

---

### Task 9: Release tool — one version, many targets

**Files:**
- Modify: `tools/fw_release.py`
- Create: `tools/test_fw_release.py` (stdlib `unittest`, run with `python -m unittest tools.test_fw_release` from the repo root or `python -I tools/test_fw_release.py`)

**Interfaces:**
- Target detection: `detect_target(build_dir) -> str` reads `project_description.json` `"target"`; `CHIP_FAMILY = {"esp32s3": "ESP32-S3", "esp32c3": "ESP32-C3"}` (unknown target ⇒ exit with a message).
- Layout: `releases/<version>/<target>/{bootloader.bin, partition-table.bin, ota_data_initial.bin, esp32_nat_router.bin}`; legacy folders with the bins at `releases/<version>/` are read as `esp32s3` and never moved. `releases/<version>/manifest.json` has one `builds[]` entry per target present (legacy paths stay root-relative).
- `releases.json` entry: existing keys unchanged (`url`, `size`, `sha256`, `built` describe the `esp32s3` image when present, else the first target) plus `"targets": {"<target>": {"url","size","sha256","built"}}`.
- `latest <version>`: copies `esp32s3` bins to `flasher/firmware/*.bin` (as today) and `esp32c3` bins to `flasher/firmware/esp32c3/*.bin`; `flasher/manifest.json` lists both builds with `firmware/` and `firmware/esp32c3/` paths; refuses a version that has no `esp32s3` image.
- `add` refuses a second image for the same version+target with a different sha256 unless `--force` (as today, per target).
- `list` prints the targets column.

- [ ] **Step 1: Tests** in `tools/test_fw_release.py`: a `setUp` that creates a temp tree and patches `fw_release.FW_DIR/REL_DIR/INDEX/TOP_MANIFEST/FLASHER_HTML`; helper `make_build(dir, target, version)` writing `project_description.json` and four fake bins, the app with the ESP-IDF header (magic `0xABCD5432` at offset 32, version at 48, project at 80, time at 112, date at 128, zero-padded). Cases: `test_add_s3_then_c3_same_version` (layout, manifest has 2 builds with `ESP32-S3`/`ESP32-C3`, index entry `targets` has both, `url` points at the s3 image); `test_legacy_folder_indexed_as_s3` (bins at the version root → `targets == {"esp32s3": ...}`, files untouched); `test_latest_copies_both_targets` (root bins + `esp32c3/` + top manifest with 2 builds); `test_latest_refuses_without_s3`; `test_add_refuses_different_image_same_target`.

- [ ] **Step 2: Run** → FAIL (`detect_target` undefined, layout mismatch).

- [ ] **Step 3: Implement** in `fw_release.py`; keep `app_desc`, `sha256`, `save_json` as they are; `release_manifest(version, prefix, targets)` builds the per-target `builds[]`; `entry_for(version, old)` scans the folder for targets; `cmd_add` uses `detect_target`; `set_latest` copies per target.

- [ ] **Step 4: Run** the unit tests → PASS; then `python -I tools/fw_release.py index` on the real tree → the six existing releases keep their entries (`git diff flasher/firmware/releases.json` shows only added `targets` keys).

- [ ] **Step 5: Commit** — `git add tools flasher/firmware/releases.json && git commit -m "fw_release: one version, per-target images (esp32s3, esp32c3)"`.

---

### Task 10: Flasher page and docs for two boards

**Files:**
- Modify: `flasher/index.html` (header L85–86, step 1 text L90–91, `fwline` L103, release list rendering ~L156–185, steps 2–3 L106–139), `docs/FIRMWARE_RELEASES.md`, `README.md` (board list / build commands), `docs/PLATFORM.md` (chip in device records)

**Interfaces:**
- Consumes: `releases.json` `targets` (Task 9); ESP Web Tools picks the `builds[]` entry whose `chipFamily` matches the connected chip, so the picker needs no target logic — only text.

- [ ] **Step 1: Flasher text**: title `ESP32 Exit Node Flasher`; subtitle names both boards (`ESP32-S3 N16R8` and `ESP32-C3 Super Mini`); step 1 adds that the C3 needs no button combo (native USB) and that the browser chooses the right image for the detected chip; `fwline` lists the latest release's targets from `releases.json`; each row in the version list shows its targets as pills. Steps 2–3 rewritten for the portal-only UI: after flashing, connect to `ESP32_NAT_Router`, open `http://192.168.4.1/`, enter the home WiFi, the AP disappears once online; status is read on the dashboard (and the board LED), not on a board web page. Keep all text Thai where it is Thai today.

- [ ] **Step 2: Docs**: `FIRMWARE_RELEASES.md` — layout with `<target>/` folders, `add` once per build dir (`build_s3n16r8`, `build_c3mini`), `latest` copying both, the C3 flash note (`--after watchdog_reset`). `README.md` — the C3 Super Mini as a supported board with its limits (4 streams, window 8 KB). `PLATFORM.md` — `chip` on device records and the OTA guard.

- [ ] **Step 3: Check**: `python -m http.server` in `flasher/` and open the page → version list renders with target pills; `go test ./...` in `admin-api/` still passes (dashboard test unaffected).

- [ ] **Step 4: Commit** — `git commit -am "flasher+docs: two boards, portal-only setup steps, per-target releases"`.

---

### Task 11: Acceptance (spec §8) and release archiving

**Files:**
- Modify: `docs/superpowers/specs/2026-10-21-esp32c3-exit-node-design.md` (append `## 11. Acceptance results`), `flasher/firmware/` via `tools/fw_release.py add … --status testing` for both build dirs

**Prerequisites (operator):** deploy broker + admin-api from this branch on Coolify (manual Deploy), confirm the dashboard's device list shows `chip` for the COM7 board after its next HELLO (empty ⇒ shown as esp32s3), and confirm the C3 MAC is unbound/dedicated.

- [ ] **Step 1: Archive** both images: `python -I tools/fw_release.py add firmware/build_s3n16r8 --status testing --title "…" --notes "…" --commit $(git rev-parse --short HEAD)` and the same for `firmware/build_c3mini` (same version `2.4.22` ⇒ one release with two targets). Commit `flasher/`.

- [ ] **Step 2: C3 soak** (COM30, production broker, operator presses Measure on the dashboard every ~2 min for 30 min while a console poller logs `heap` + `rproxy status` every 30 s to `D:\espbuild\c3-soak.log`): pass = no reboot, no `overran window`, free heap ≥ 20 KB at every sample with 4 streams active, throughput recorded.

- [ ] **Step 3: C3 OTA under load**: dashboard Update FW → a rebuilt `2.4.22` (bump to `2.4.22-rc2` for the test image) while Measure runs → `ok` on the dashboard; then push an image built to fail the probation (wrong broker host in prov) → `rolled back` within 5 minutes and the board is back on 2.4.22.

- [ ] **Step 4: Portal round trip** on the C3: pull the home AP (or `set_sta` to a wrong SSID), wait > 90 s, join `ESP32_NAT_Router` with a phone, set the right SSID at `http://192.168.4.1/`, confirm the AP and `httpd` go away after reconnect.

- [ ] **Step 5: S3 regression** (COM7, 2.4.22): link, 40 streams, speed within 10% of 9 down / 12 up Mbps, OTA under load (drain) to an rc image and back, `2.4.21-mux1.2` board (or the fakenode in `tools/fakenode`) still links to the new broker.

- [ ] **Step 6: Record** every number in §11 of the spec, mark pass/fail per §8 line, and commit. Promotion to `latest`, field OTAs and the PR are the operator's calls (spec §6.8).
