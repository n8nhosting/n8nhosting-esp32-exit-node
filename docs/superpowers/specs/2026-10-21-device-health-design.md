# Device health on the dashboard — design

Date: 2026-10-21. Follows the ESP32-C3 exit-node work (2.4.22) and the
dashboard review (`docs/dashboard-review/`).

## Intent

The operator asked for three things after seeing the Devices table:

1. **Uptime shows `0m` for every board.** Root cause (read, not guessed):
   the firmware reports uptime once, in HELLO, a few seconds after boot
   (`rproxy.c` builds HELLO with `esp_timer_get_time()/1e6`); the broker
   stores that number in `touch()` and nothing ever updates it. The
   dashboard formats anything under a minute as `0m`.
2. **Show CPU / RAM / ROM (flash) of each board** so a sick board is visible
   before it dies (the v1.2 pool died of internal-RAM exhaustion with no
   signal on the dashboard).
3. **Redesign the row action buttons** (Edit · Update FW · Block · Forget):
   four equal-weight buttons per row with the destructive one next to the
   everyday one, the exact anti-pattern the review flagged
   (`01-ux-ia.md` §Devices, backlog item 2).

## Goals

- Uptime is right for **every deployed firmware** (2.4.21-mux1.x boards on
  production included) without an OTA: the broker keeps it running.
- Boards on new firmware report health every 30 s; the dashboard shows it in
  one *Health* cell (no new column): uptime, RAM, CPU, flash.
- Old firmware keeps working on the new broker, and new firmware keeps
  working on the old broker (the broker already ignores unknown sid-0
  frames; verified in `mux.go dispatch()`).
- Row actions: one quiet visible action plus a `⋯` menu; destructive
  actions sit at the bottom of the menu, separated and red; keyboard and
  screen-reader usable.

## Non-goals

- Historical charts, alerts, thresholds in the backend (later: review item 9).
- Persisting health samples (live only; a broker restart shows "—" until the
  next sample, at most 30 s).
- The row-click drawer / 7-column table from the review (item 2's bigger
  half) — only the action menu part lands here.
- Changing the capacity/admission rule (separate decision, still open).

## Design

### Protocol: `STATS` frame (node → broker, sid 0, type `0x0B`)

Payload is text, space-separated `key=value` tokens, same style as the
optional HELLO tokens, so it can grow without a format change:

| key | value | source on the board |
|---|---|---|
| `up` | seconds since boot | `esp_timer_get_time()/1e6` |
| `heap` / `heapmin` / `heaptot` | internal heap free / low-water / total, bytes | `heap_caps_*(MALLOC_CAP_INTERNAL)` |
| `psram` / `psramtot` | PSRAM free / total, bytes; absent without PSRAM | `heap_caps_*(MALLOC_CAP_SPIRAM)` |
| `cpu` | load % since the previous sample, averaged over cores; `-1` = unknown | FreeRTOS run-time stats: 100 − idle share (`ulTaskGetIdleRunTimeCounterForCore`) |
| `app` / `slot` / `flash` | running image bytes / its OTA partition bytes / flash chip bytes | `esp_image_get_metadata`, `esp_ota_get_running_partition`, `esp_flash_get_size` |
| `streams` | open mux streams | `s_active` |

Sent once right after `READY mux1`, then every 30 s from the link task.
Unknown keys are ignored; a bad value keeps the field unset. Internal heap
is reported (not the default heap) because on the S3 the 8 MB PSRAM would
hide internal-RAM starvation, which is the failure that matters.

`CONFIG_FREERTOS_GENERATE_RUN_TIME_STATS=y` goes into the shared
`sdkconfig.defaults` (both chips). Counter is the default esp_timer µs
`uint32`, which wraps every 71 min; deltas are taken as `uint32` so a 30 s
sampling period is safe.

### Broker

- `node` gains `uptimeAt` (when `UptimeSec` was last reported), `offAt`
  (when its last link closed) and `Stats *nodeStats` (`json:"stats"`,
  omitted when nil, never persisted).
- `uptime_sec` in `/nodes` = reported uptime + time elapsed since it was
  reported, frozen at `offAt` once the link is down. A node restored from
  disk after a broker restart shows its stored value until it HELLOs.
- A link closing sets `offAt` and `last_seen` (today `last_seen` is the
  HELLO time, so "last seen" was wrong for a board that ran for days).
- `STATS` updates `UptimeSec`/`uptimeAt` and replaces `Stats`.
- Time is read through a package variable so tests control it.

### admin-api

Pass-through: `brokerNode.Stats *nodeStats` with the same JSON shape; the
device view inherits it. No new endpoint.

### Dashboard

- The *Uptime* column becomes **Health**: line 1 uptime (online) or
  "offline · last seen N ago"; with stats: `RAM` meter (free of total,
  amber below 15 % or 24 KB), `CPU` meter (amber at ≥ 85 %), `Flash app /
  slot · chip` line; without stats: "health needs firmware 2.4.23".
- Row actions, both tables: visible `Edit` (devices) / `Connection` + `Edit`
  (users) plus a `⋯` button (`aria-haspopup="menu"`, labelled with the row
  name) opening one shared `#rowmenu` (`role="menu"`, `role="menuitem"`
  buttons, fixed-positioned so the table's `overflow:hidden` cannot clip it,
  flips above the trigger near the bottom edge). Arrow keys move, Escape
  closes and returns focus, click outside closes. Devices menu: Update FW ·
  Block/Allow · ── · Forget. Users menu: Enable/Disable · +30 days · ── ·
  Delete. `confirm()` stays on Forget/Delete.
- Thai-safe: no fixed heights; sizes ≥ 13 px for labels.

### Firmware release

`EXITNODE_RELEASE 2.4.23` → `2.4.23-s3` / `2.4.23-c3`, archived as
*testing* in `flasher/firmware/releases/2.4.23/`; latest unchanged.

## Acceptance

- Broker tests: uptime keeps running while linked, freezes when the link
  drops, resets on a new HELLO; STATS parsed into `/nodes`; an unknown sid-0
  frame is ignored.
- admin-api test: `/api/devices` carries `stats`.
- Dashboard test: markers for the Health cell and the menu; no bare red
  destructive button in a row.
- Bench: the C3 on COM30 against the local TLS broker shows RAM/CPU/flash
  on the local dashboard; S3 image builds.
