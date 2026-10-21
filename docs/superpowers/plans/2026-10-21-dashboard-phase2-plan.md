# Dashboard phase 2 — implementation plan

> Approved platform follow-up (2026-10-22): commerce, Stripe + manual slips,
> customer portal, email outbox, RSSI and bounded firmware OTA are tracked in
> [the platform acceptance report](../../dashboard-review/06-platform-commerce-acceptance.md).
> Its local/production/activation gates supersede the older out-of-scope notes.

> Latest seat-rule revision (2026-10-22): the operator approved independent
> seats per board/MAC, including the same public IP. This supersedes the older
> shared-IP/dedicated-IP admission rules in this plan. See
> [per-board seats](../specs/2026-10-22-per-board-seats-design.md).

> Completed and locally verified 2026-10-22 (Asia/Bangkok).
> Final behavior, validation evidence and screenshot locations:
> [`04-phase2-acceptance.md`](../../dashboard-review/04-phase2-acceptance.md).
> Deployed to production 2026-10-22 as `50c8226`; Coolify deployment
> `vzz99qns9ndckzuhxrzzmgvp` succeeded. Production browser checks and the
> separately approved Board 1 routing restoration are recorded in that handoff.
> Hardware throughput/soak acceptance remains separate.
>
> Follow-up approved 2026-10-22: registered boards without routing receive
> durable automatic slots (editable); complete device details appear together
> without section tabs. See the latest acceptance section and
> [`device-settings-and-flasher-refinement.md`](../specs/2026-10-22-device-settings-and-flasher-refinement.md)
> for the superseding UI and migration v6 contracts.
>
> OTA confirmation and hardware diagnostics follow-up (2026-10-22): see
> [`ota-confirmation-and-diagnostics.md`](../specs/2026-10-22-ota-confirmation-and-diagnostics.md)
> and the latest acceptance section for firmware 2.4.24, actual board results,
> durable broker storage, live traffic counters and remaining soak gates.


> **For agentic workers:** this plan sequences design work that already
> exists in `docs/dashboard-review/` (`01-ux-ia.md`, `02-frontend-design-spec.md`,
> `03-backend-api-plan.md`) into buildable tasks. Read those three files —
> especially `03-backend-api-plan.md` §1 (data inventory) and §7 (its own
> backlog, which this plan follows in order) — before writing code; this
> document does not repeat their line-level Go/CSS, only sequences it and
> notes what changed since they were written. Also read `AGENTS.md` at the
> repo root for build/bench/deploy mechanics and the open capacity-rule
> decision, and `docs/superpowers/specs/2026-10-21-device-health-design.md`
> for the STATS frame this plan builds on.

**Goal:** finish the dashboard backlog from `docs/dashboard-review/README.md`
— items 2 (status/derived fields), 5 (Overview KPIs + attention list), 6
(device drawer), 7 (auto-refresh + mobile pass), 8 (metrics/time series), 9
(events/alerts), 10 (composite health score) — on top of item 1 (Google
Maps, shipped) and the already-shipped device-health round (uptime,
`STATS` frame, Health cell, row menu — not in the original three reports,
shipped 2026-10-21 after they were written).

**Architecture:** unchanged from `03-backend-api-plan.md`: SQLite in
`admin-api` (migrations in `db.go`), a background poller goroutine for
metrics/alerts, REST endpoints under `/api/`, the dashboard is one static
file (`admin-api/dashboard/index.html`) with no build step.

**Tech stack:** Go 1.2x (`broker/`, `admin-api/`), vanilla HTML/CSS/JS
dashboard, `modernc.org/sqlite` (check `admin-api/go.mod` for the exact
driver already in use).

## Current decisions and evidence (2026-10-21, local development)

- The operator approved backend-spec counter-reset behavior: use the
  current counter after a decrease, and establish a baseline with zero
  delta on the first sample. Drawer API queries use the canonical
  `entity=node&id=<mac>` and `subject=<mac>` parameters.
- Health weights: availability / dial success / stream headroom / heap
  headroom / CPU headroom = 40 / 30 / 10 / 10 / 10; missing components
  renormalise away. Existing instrument meters remain visible.
- The operator delegated the remaining decisions. Shared capacity keeps
  0 seats without a positive measurement, then uses
  `min(3, max(1, floor(capacity_mbps / 5)))`; dedicated stays at 1.
- Broker additive fields and tests are implemented (commit `9ed2e47`):
  `max_streams` is the accepted session limit (legacy default 24),
  `link_since` starts only after a successful `READY mux1` and clears only
  after the last mux link closes, and a known pinned board gets its own
  `dials_pinned_offline` counter when no usable link remains. These are
  live-only fields and are excluded from persistence.
- Broker validation: `go test ./...` passed (14.253 s); `go vet ./...`
  exited 0. Tests cover legacy HELLO/defaults and `/nodes` serialization,
  failed READY, overlapping reconnects, concurrent snapshots/counters,
  persistence exclusion, and refused pins. Go's telemetry upload-token
  warning on Windows did not fail either command.
- Targeted broker `go test -race` also passed (2.174 s) for accepted HELLO
  capabilities, reconnect/concurrent snapshots, and offline/blocked/full/
  closed/exhausted pin refusals.
- This records local implementation evidence only. No deployment,
  production acceptance, hardware soak, or firmware release is implied.
  The checked items below record delivered behavior and passing validation;
  the final acceptance report supersedes older interface examples in this plan.

**Spec:** `docs/dashboard-review/01-ux-ia.md`,
`docs/dashboard-review/02-frontend-design-spec.md`,
`docs/dashboard-review/03-backend-api-plan.md`,
`docs/superpowers/specs/2026-10-21-device-health-design.md`.

## Global constraints

- Every step keeps `go test ./...` green in both `broker/` and `admin-api/`,
  and `python -I tools/test_fw_release.py` green if a task touches
  `tools/fw_release.py`.
- `admin-api/dashboard_test.go` greps the served HTML for marker strings —
  add markers for new UI, never remove an existing one without checking
  what it guards (several guard against regressions: no `alert(`/`prompt(`,
  MAC/username only via `data-*` attributes, etc.).
- No new JS framework, no build step for the dashboard.
- Each numbered item below should be its own commit (or small commit
  series) and, ideally, its own PR — `03-backend-api-plan.md` §7 calls this
  out explicitly: every item is additive and backward compatible, so the
  dashboard keeps working after each one lands, not just at the end.
- Follow the existing style: handlers return `(status, errBody)` the way
  `admission.go`'s `admit` does; new tables follow `db.go`'s
  `migrations = []string{...}` append-only pattern (current head is the v3
  `maps_url` migration — the next one `CREATE TABLE`s, not `ALTER`s, so it
  does not need a version bump dance beyond appending).
- Dashboard copy, Thai text, focus handling: follow the conventions already
  in `index.html` (`T()`/`i18n`, `esc()`, `role="menu"` for the row popover,
  `#toast` for confirmations, no inline `onclick`).

## Reconciliation: what changed since `03-backend-api-plan.md` was written

`03-backend-api-plan.md` §1's data inventory and §7 item 10 assumed board
heap/CPU did not exist on the wire ("Free heap / RSSI: not sent... Needs a
firmware + broker change... a periodic STATS frame on sid 0 gives live
values. Phase E / firmware backlog."). **That frame now exists**: firmware
2.4.23+ sends a `STATS` frame (sid 0, `0x0B`) every 30 s; the broker stores
it as `node.Stats *nodeStats` (`broker/registry.go`) and admin-api passes it
through as `brokerNode.Stats` / `deviceView.Stats` (`admin-api/devices.go`).
Fields: `uptime_sec`, `heap_free`, `heap_min`, `heap_total`, `psram_free`,
`psram_total`, `cpu_pct` (-1 = unknown), `app_bytes`, `slot_bytes`,
`flash_bytes`, `streams`. No RSSI yet (never implemented — still a gap if
Wi-Fi signal strength turns out to matter for alerts).

Effect on the remaining backlog:

- **§5 "Health gauge" (now item 10 below) no longer needs to wait for the
  poller (§2/item 8 below) to show something live.** Build the composite
  score in two layers from day one: an *instant* layer from
  `deviceView.Stats` (heap headroom, CPU) and `online`, blended with a
  *trailing* layer from `metrics_node` (24 h availability, dial success)
  once item 8 ships. Before item 8 ships, the trailing terms are simply
  absent and their weight renormalises away — same pattern
  `03-backend-api-plan.md` §5 already describes for "before the sampler
  ships", just with one more instant input than it assumed.
- **§7 item 10's firmware half ("heap/rssi reporting") is half done.**
  Only RSSI is still open; do not re-implement heap reporting.
- The Health **cell** already on the Devices table (uptime + RAM/CPU/flash
  meters, `index.html`'s `healthCell()`) is a *per-board instrument panel*,
  distinct from the *composite score* this plan's item 10 adds (a single
  0-100 number with a reason list, used for the status pill and an
  Overview "attention" list). Keep both — they answer different questions
  ("is this board sick right now" vs "should I click into it").

## Review focus

(the five inputs/conditions most likely to bite a person using this
software that no single item's own tests obviously cover)

1. A board that has never reported `STATS` (firmware < 2.4.23) must get a
   sane composite health score and status, not a crash or a 0 from a nil
   pointer — every item touching `deviceView.Stats` needs a nil-safe path.
2. Monthly/retention jobs (rollup, event/metrics pruning) must not run
   twice concurrently if the poller's own tick overlaps a slow DB write —
   use a single poller goroutine with a ticker, not overlapping timers.
3. A broker restart resets in-memory byte counters (`bytes_up`/`bytes_down`
   are RAM-only, `persistedNode` excludes them per `03-backend-api-plan.md`
   §1) — the metrics sampler must treat a counter *decrease* as a reset,
   not negative throughput.
4. The alerts evaluator must be idempotent against its own poller tick
   cadence — an alert already "raised" must not re-emit an `alert.raised`
   event every tick while the condition persists; only on the transition.
5. Mobile/narrow-viewport pass (item 7) must not break the row menu's
   viewport-edge clamping already shipped for `#rowmenu` in `index.html` —
   test the drawer and menu together at a phone width, not in isolation.

---

### Task 1: derived status, `connected_now`, `throughput_bps`

(`03-backend-api-plan.md` §7 item 2 — unchanged by the reconciliation above)

**Files:** modify `admin-api/devices.go` (`deviceStatus()` per §5's exact
switch, `deviceView.Status`/`.Tags`/`.NextExpiry`), `admin-api/store.go`
(`PublicUser.Status`), `admin-api/main.go` (`/api/overview` additions),
`admin-api/devices_test.go`, `admin-api/api_test.go`.

**Interfaces (produces):** `deviceView.status string`, `.tags []string`
(json omitempty), `.next_expiry string` (RFC3339 or ""); `PublicUser.status
string` (`active|disabled|expired|over_quota`); `overview.users.connected_now
int`; `overview.broker.throughput_bps {up,down float64}` (two samples of
`/status` cumulative bytes, taken a fixed interval apart — in-memory only,
no persistence needed yet).

- [x] Write failing tests for `deviceStatus()`'s precedence order (blocked
      > updating > offline(+ota_stalled tag) > rolled_back > unmeasured >
      online(+ota_queued/slot_mismatch tags)) exactly as
      `03-backend-api-plan.md` §5 specifies, including a case with
      `deviceView.Stats == nil` (must not affect status at all — status is
      independent of the health score).
- [x] Implement; run `go test ./...`; commit.
- [x] Dashboard: replace `index.html`'s client-side online/offline
      derivation (`strings.Contains` search for where it currently builds
      the status pill from `n.online`/`n.blocked`) with `n.status`/`n.tags`
      from the server. Add `dashboard_test.go` markers for `n.status` and
      `n.tags`. Commit.

### Task 2: time-series poller + `metrics_*` tables (migration v4)

(`03-backend-api-plan.md` §2 + §7 item 3 — read §2 in full, it has the
exact schema, rollup/retention logic and cost estimate already worked out)

**Files:** new `admin-api/metrics.go` (poller, flush, rollup), `admin-api/db.go`
(migration v4 — `CREATE TABLE metrics_node`/`metrics_user` per §2's schema),
`admin-api/main.go` (start the poller goroutine), tests `admin-api/metrics_test.go`.

**Interfaces (produces):** an internal sampler that reads `/nodes` and the
observer's per-client accumulator every poll interval (§2 "Where it runs"),
writes 5-minute buckets, rolls up to hourly after retention age, and
exposes a cached snapshot for Task 3's endpoint to read without hitting
SQLite on every dashboard poll.

- [x] Tests first, per §2's own worked examples: a counter reset between
      polls contributes the current value, never a negative delta; a
      first sample contributes zero while establishing its baseline
      (Review focus #3); rollup
      merges 5-min buckets into hourly correctly; retention deletes rows
      older than its window; two overlapping ticks never run concurrently
      (Review focus #2 — test with a slow fake DB).
- [x] Implement against §2's schema and cost assumptions exactly (10
      boards, 50 users — if the real fleet is bigger by the time this
      ships, recheck the cost table in §2 before shipping, it is sized for
      that assumption).
- [x] `go test ./...`; commit.

### Task 3: `GET /api/metrics`

(`03-backend-api-plan.md` §2 "Read endpoint" + §7 item 4)

**Files:** modify `admin-api/main.go` (route), new handler in `admin-api/metrics.go`,
tests in `admin-api/metrics_test.go`.

**Interfaces (produces):** the exact response shape in §2's "Read endpoint"
subsection: fleet/node/user series, `nodes`/`users` totals, `summary` with
`last`/`prev`/`delta_pct`.

- [x] Test the endpoint against Task 2's fixtures (seeded buckets); test
      the `delta_pct` math at the summary boundary (0 previous value must
      not divide by zero).
- [x] Implement; `go test ./...`; commit.
- [x] Dashboard: KPI sparklines + delta pills on Overview (`02-frontend-design-spec.md`
      §2.8, §3.1), 30-day area chart (§3.2). Add `dashboard_test.go`
      markers for the new KPI card IDs. Verify in Chrome against the bench
      admin-api (`AGENTS.md` bench rig) with at least one board's worth of
      seeded `metrics_node` rows before calling this done — an empty chart
      that silently renders nothing is not a passing check. Commit.

### Task 4: events (migration v5) + `GET /api/events`

(`03-backend-api-plan.md` §3 + §7 item 5)

**Files:** new `admin-api/events.go` (`emit()`, emitters), `admin-api/db.go`
(migration v5 per §3's schema), modify the admin handlers/webhook/passthrough
actions §3 "Sources and emitters" lists to call `emit()`, `admin-api/main.go`
(route), tests `admin-api/events_test.go`.

**Interfaces (produces):** `emit(kind, actor, target, detail)` from any
handler; `GET /api/events` with keyset pagination per §3's "Endpoint"
subsection; poller-driven transition events (online/offline, OTA status
changes) with last-event dedupe so a flapping board does not spam the feed
(Review focus #4 applies here too — dedupe on the transition, not the tick).

- [x] Tests: emitters fire exactly once per real transition (not per
      poller tick while a state persists); keyset pagination returns
      stable pages under concurrent inserts; retention prunes old events.
- [x] Implement; `go test ./...`; commit.
- [x] Dashboard: Activity feed (`02-frontend-design-spec.md` §2.9). Add
      `dashboard_test.go` markers. Commit.

### Task 5: composite `deviceView.health`

(`03-backend-api-plan.md` §5's formula + §7 item 6, **revised per the
Reconciliation section above** — do not implement §5's formula verbatim,
extend it)

**Files:** modify `admin-api/devices.go` (`buildDeviceViews`, add `Health`
to `deviceView`), tests `admin-api/devices_test.go`.

**Interfaces (produces):** `deviceView.health {score int, availability_24h
*float64, dial_success_24h *float64, stream_load *float64, heap_headroom
*float64, cpu_load *float64, reasons []string}`. `heap_headroom` =
`Stats.HeapFree / Stats.HeapTotal` when `Stats != nil`, else absent.
`cpu_load` = `Stats.CPUPct/100.0` when `Stats != nil && CPUPct >= 0`, else
absent (the firmware sends -1 for "unknown", e.g. right after a long link
outage — see `rproxy.c`'s `cpu_load_pct()` baseline reset, do not treat -1
as 0% load).

- [x] Tests: weights renormalise correctly across every combination of
      present/absent components (no `Stats`, `Stats` present but `cpu_pct
      == -1`, pre-Task-2 with no `metrics_node` rows yet, all present);
      `blocked`/`offline`/`updating` caps per §5; score is deterministic
      and documented (a human must be able to read `reasons` and believe
      the number).
- [x] Implement; `go test ./...`; commit.
- [x] Dashboard: 10-segment gauge (`02-frontend-design-spec.md` §2.5) on
      the status pill / device row, tooltip from `reasons`. This does
      **not** replace the existing per-board Health cell (uptime/RAM/CPU/
      flash meters) — both appear; see Reconciliation note. Add
      `dashboard_test.go` markers. Commit.

### Task 6: `GET /api/alerts`

(`03-backend-api-plan.md` §6 + §7 item 7)

**Files:** new `admin-api/alerts.go` (evaluator run from the poller tick),
modify `admin-api/main.go` (route, overview `counts`), `admin-api/metrics.go`
(call the evaluator each tick), tests `admin-api/alerts_test.go`.

**Interfaces (produces):** `GET /api/alerts` per §6's exact shape; evaluator
emits `alert.raised`/`alert.cleared` events (via Task 4's `emit()`) only on
the transition edge (Review focus #4); `overview.alerts.counts
{critical,warning}`.

- [x] Tests: an alert condition held across N consecutive ticks emits
      exactly one `raised` event, not N; clearing emits exactly one
      `cleared`; counts reflect current state, not event history.
- [x] Implement; `go test ./...`; commit.
- [x] Dashboard: alert tiles / attention list on Overview
      (`01-ux-ia.md` backlog item 5's "attention list" replacing a bell
      icon — build it here, it needs this endpoint). `dashboard_test.go`
      markers. Commit. **This is the natural point to also close
      `01-ux-ia.md` backlog item 5 (Overview KPI cards + attention list)
      if Task 3 has already landed the KPI cards** — both need real data
      this task and Task 3 provide; do not build the Overview page twice.

### Task 7: device drawer

(`01-ux-ia.md` §3.2 + backlog item 3; no backend blocker — uses endpoints
already shipped plus Tasks 1/3/4/5 once they land)

**Files:** modify `admin-api/dashboard/index.html` only (route
`#/devices/<mac>`, drawer component per `01-ux-ia.md`'s IA section),
`admin-api/dashboard_test.go`.

**Interfaces (consumes):** `GET /api/devices` (existing), Task 3's
`GET /api/metrics?entity=node&id=<mac>&range=24h` for a per-device sparkline/history panel,
Task 4's `GET /api/events?subject=<mac>&limit=50` for a per-device activity log, Task
5's `deviceView.health.reasons` for an explain panel.

- [x] Add the drawer (keyboard-accessible, `role="dialog"` like the
      existing `#dmodal`, closes on Escape, returns focus to the triggering
      row — follow the pattern `#rowmenu`/`#cmodal`/`#amodal` already use).
      `dashboard_test.go` markers (`id="ddrawer"` or similar, `role="dialog"`).
- [x] Verify in Chrome against the bench admin-api with a seeded board;
      screenshot before calling it done. Commit.

### Task 8: auto-refresh (patch, not re-render) + mobile pass

(`01-ux-ia.md` backlog items 6 and 10; `02-frontend-design-spec.md` §5)

**Files:** modify `admin-api/dashboard/index.html` only,
`admin-api/dashboard_test.go`.

**Interfaces:** none new — this is a client-side refactor of
`viewDevices()`/`viewUsers()` to diff-patch existing `<tr>`s on a poll
interval instead of replacing `innerHTML` (so an open row menu or a focused
input is not destroyed under the user), plus the responsive pass
`02-frontend-design-spec.md` §5 specifies.

- [x] Verify the row menu (`#rowmenu`), device editor (`#dmodal`) and the
      new drawer (Task 7) all survive a background auto-refresh tick while
      open — this is Review focus #5's point, test it explicitly, not just
      visually.
- [x] Test at a phone viewport width in Chrome DevTools (`resize_page`
      375×812 or similar) — table must not force horizontal page scroll;
      `02-frontend-design-spec.md` §5 has the exact breakpoint rules.
      Screenshot before/after. Commit.

### Task 9: broker additive fields + docs/memory

(`03-backend-api-plan.md` §7 item 9, reduced — `mux_links` and heap are
already shipped; only `max_streams`, `link_since`, and per-node
`dials_pinned_offline` remain)

**Files:** modify `broker/registry.go` (`node.MaxStreams int`,
`node.LinkSince string`), `broker/main.go` (set from `helloOpts.MaxStreams`
and on `READY mux1`), `broker/registry_test.go`; modify `admin-api/devices.go`
(`brokerNode.MaxStreams`, `.LinkSince`) to consume when present (nil-safe
for older brokers); `docs/FIRMWARE_RELEASES.md`, `docs/PLATFORM.md`,
`AGENTS.md` (update the open-decision section if Task 5's health score or
operator input resolves it — see below).

- [x] Broker tests: `max_streams`/`link_since` round-trip through `/nodes`;
      a node from an older HELLO (no `win=`/`frame=`/`chip=` tokens, see
      `broker/hello_test.go`'s legacy-defaults cases) still works.
- [x] Implement; `go test ./...` both modules; update docs; commit.
- [x] Verify the selected C3 capacity rule in `admin-api/devices.go`'s
      `maxUsers()`, with tests: unmeasured = 0, positive measurement = at
      least 1, ceiling = 3, dedicated = 1. The former open decision is
      resolved above and in `AGENTS.md`; completion still requires
      implementation and admin-api test evidence.

## Self-review notes for whoever picks this up

- Items 1 (Maps) and the device-health round (uptime/STATS/Health cell/row
  menu) are **done and pushed** (`origin/main`, see `git log`). Do not
  redo them. The former open C3 decision is now recorded in `AGENTS.md`'s
  "Dashboard phase 2 decisions" section. Task 9 still requires verifying
  the selected rule and its admin-api tests before claiming completion.
- Tasks are ordered by dependency (2→3, 4 independent of 2/3, 5 depends on
  1 + partially 2, 6 depends on 2+4, 7 depends on 3+4+5, 8 depends on 7,
  9 is independent and can run anytime) but each is self-contained enough
  to ship alone — follow `03-backend-api-plan.md` §7's framing: additive,
  backward compatible, the dashboard works after every single task.
