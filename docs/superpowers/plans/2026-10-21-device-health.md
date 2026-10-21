# Device health + row menu — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:executing-plans (inline). Steps use `- [ ]`.

**Goal:** Correct uptime for every board, live RAM/CPU/flash for boards on 2.4.23, and a `⋯` action menu in both dashboard tables.

**Architecture:** Firmware sends a text `STATS` frame (sid 0, 0x0B) every 30 s; the broker keeps a running uptime from the last report and stores the latest stats on the node; admin-api passes `stats` through; the dashboard renders a Health cell and a shared popover menu.

**Tech stack:** Go 1.2x (broker, admin-api), vanilla HTML/JS dashboard, ESP-IDF 5.5.2 C.

**Spec:** `docs/superpowers/specs/2026-10-21-device-health-design.md`

## Global constraints

- Old firmware must keep working on the new broker; new firmware on the old broker (sid-0 frames other than PING are ignored there).
- `alert(`/`prompt(` stay banned in the dashboard; `confirm()` allowed for destructive actions.
- Internal heap, not default heap, in STATS.
- Version `2.4.23`; latest release stays `2.4.21-mux1.2`.

## Review focus

1. A board that HELLOs with uptime 0 (bootloop) must not show a huge uptime from a previous link.
2. STATS with garbage (`cpu=abc`, missing keys) must not panic and must leave known fields sane.
3. A node restored from disk (no link yet) must report its stored uptime, not grow.
4. Menu open near the viewport bottom must stay on screen; Escape returns focus to the trigger.
5. Users table rows with names containing quotes still work via data-attributes (no inline script).

---

### Task 1: broker — running uptime, link-down bookkeeping, STATS frame

**Files:** modify `broker/registry.go`, `broker/mux.go`, `broker/main.go`, `broker/hello.go` (parseStats); tests `broker/registry_test.go` (new), `broker/hello_test.go`, `broker/mux_test.go`.

**Interfaces (produces):**
- `var timeNow = time.Now` (registry.go)
- `type nodeStats struct{ At string; UptimeSec uint64; HeapFree, HeapMin, HeapTotal, PSRAMFree, PSRAMTotal uint32; CPUPct int; AppBytes, SlotBytes, FlashBytes uint32; Streams int }` JSON keys: `at, uptime_sec, heap_free, heap_min, heap_total, psram_free(omitempty), psram_total(omitempty), cpu_pct, app_bytes, slot_bytes, flash_bytes, streams`
- `func parseStats(s string) (nodeStats, bool)` — false when no known key parsed; `cpu_pct` defaults to -1
- `func (r *registry) stats(n *node, st nodeStats)`; `func (r *registry) linkDown(n *node)`
- `const frameStats = 0x0B`
- `/nodes` JSON: `uptime_sec` running; `stats` object when known.

- [ ] Tests first: `TestUptimeKeepsRunning` (touch uptime 10 at t0; t0+90 → 100; linkDown at t0+120; t0+500 → 130; touch uptime 3 at t0+600 → t0+660 → 63), `TestUptimeStoredNodeDoesNotGrow` (load from file, no touch), `TestParseStats` (full line, partial line, garbage), `TestMuxStatsFrame` (test node sends STATS; `/nodes` shows heap_free and uptime ≥ up; an unknown sid-0 type keeps the link up).
- [ ] Run → fail (missing symbols). Implement. Run `go test ./...` → pass. Commit.

### Task 2: admin-api — stats pass-through

**Files:** modify `admin-api/devices.go` (brokerNode.Stats), test `admin-api/devices_test.go`.
- [ ] Test: fakeBroker node with `Stats` → `GET /api/devices` item has `stats.heap_free`. Fail → implement → pass → commit.

### Task 3: dashboard — Health cell + row menu

**Files:** modify `admin-api/dashboard/index.html`, `admin-api/dashboard_test.go`.
- [ ] Test markers: `<th>Health</th>`, `n.stats`, `id="rowmenu"`, `role="menu"`, `aria-haspopup="menu"`, `fmtAgo(n.last_seen)`; forbidden: `class="danger" data-act="forget"`, `class="danger" data-act="delete"`.
- [ ] Implement `healthCell(n)`, `fmtKB`, `.meter` CSS, `openRowMenu(trigger, items)`, menu keyboard handling; devices + users rows.
- [ ] Verify in Chrome against the local admin-api + local broker (C3 row with stats; menu open/flip/Escape). Commit.

### Task 4: firmware 2.4.23 — STATS sender

**Files:** modify `firmware/components/rproxy/rproxy.c`, `firmware/components/rproxy/CMakeLists.txt` (REQUIRES + `bootloader_support spi_flash`), `firmware/sdkconfig.defaults` (`CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS=y`), `firmware/CMakeLists.txt` (2.4.23).
- [ ] `F_STATS = 0x0B`; `stats_init()` (image/slot/flash sizes, CPU baseline) at mux_task start; `stats_send(link_t*)`; first send after READY, then every 30 s in `mux_serve`.
- [ ] Build C3 (`build-exitnode.ps1 -Board c3mini`) and S3 (`-Board s3n16r8`); flash C3 on COM30 (`--after watchdog_reset`); local TLS broker (rebuilt) shows `stats` for the C3 within 5 s and uptime grows; commit.
- [ ] Archive both images: `tools/fw_release.py add <build> --status testing --title "Board health: RAM/CPU/flash every 30 s; uptime keeps running"`; commit.

### Task 5: docs, memory, merge, push

- [ ] `docs/FIRMWARE_RELEASES.md`, `README.md` (protocol line), `docs/PLATFORM.md` (fleet health row), rproxy.c header comment.
- [ ] Merge branch into main; `go test ./...` both; push `origin main` (authorised by the operator on 2026-10-21 after these tasks).
