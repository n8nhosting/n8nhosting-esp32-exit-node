# Dashboard Phase 2 — implementation and local acceptance

Verified 2026-10-22 (Asia/Bangkok). Source baseline: `507bc77`.

Implementation checkpoints: `9ed2e47` (broker), `767b486` (admin API),
`13f902b` (health component/threshold coverage), `eaf6afa` (dashboard and
repeatable browser acceptance). These commits are local; no push or deploy
was performed.

## Delivered

- Canonical device status/tags, next expiry, user status counts, recent connected
  users and live broker throughput. Existing overview fields retain their meaning.
- One serialized sampler, SQLite migrations v4/v5, five-minute metrics, hourly
  rollups, 90-day retention, metrics filters/totals and complete-window summaries.
  A reset contributes the current counter; the first sample contributes zero.
  Failed flushes retain observer bytes. Maintenance rolls completed source hours
  before hour-aligned pruning, including after a long admin-API outage.
- Durable activity events and transition-based alerts with restart deduplication,
  secret-free audit data and keyset pagination. Failed broker polls preserve
  existing node alerts instead of reporting every board offline.
- Explainable health score (40/30/10/10/10) with missing/stale/invalid resource
  exclusion, live availability fallback and true nullable historical components.
- Broker `max_streams`, `link_since`, per-node pinned-refusal counters, safe
  overlapping-link handling and compatibility with legacy HELLO defaults.
- Measured shared boards get `min(3,max(1,floor(Mbps/5)))` seats. Unmeasured
  shared boards get zero; dedicated boards get one. Overview counts availability
  once per exit IP and preserves dedicated exclusivity.
- Seven-column device table, retained RAM/CPU/flash instruments, responsive
  drawer, inventory KPIs, real-data charts, attention/activity, user search,
  usage/expiry treatment and 7/30/90-day renewal choices.
- Ten-second keyed refresh preserves focus, open menus, filter and scroll.
  Hidden tabs and editing dialogs pause polling. Stale data stays visible on
  errors, and unauthorized responses clear authentication and stop polling.

## Interface decisions

- `/api/metrics?entity=node&id=<mac>&range=24h`
- `/api/events?subject=<mac>&limit=50`
- `broker.throughput_bps` is **bytes/second**; UI Mbps is bytes/s × 8 / 1e6.
- Alerts use `critical|warn|info`; overview exposes `alerts.counts`.
- `lat:null,lon:null` clears position; omitted values keep it. Existing numeric
  single-coordinate edits remain supported. UI also clears the original map URL.
- Polling defaults to 60 seconds; persistence buckets are fixed at five minutes.
  Five-minute rows retain 48 hours; hourly rows retain 90 days by default.
- Resource samples older than 120 seconds are excluded from the composite score.
  CPU -1 and invalid resource measurements remain unknown, rather than healthy.

## Validation evidence

- `admin-api`: full `go test ./...` and `go vet ./...` passed. Full
  `go test -race ./...` passed (22.740 seconds), including the future-OTA-time
  regression, status/health/capacity boundaries, serialization, flush recovery,
  reset handling, history gaps, retention and pagination/alert deduplication.
- `broker`: `go test ./...` passed (14.036 seconds), `go vet ./...` passed;
  targeted race checks passed for link metadata and concurrent snapshots.
- Browser acceptance passed on Chromium 156.0.8078.4 at 1440×900 and 375×812.
  This runs the **real admin API against isolated, migrated and seeded SQLite**;
  broker and geo responses are deterministic fixtures. It does not use live
  production credentials or represent a hardware throughput test.
- Browser checks exercise nonzero history, null gaps, zero/single-point charts,
  chart keyboard inspection, Thai names/search, actual refresh ticks, retained
  row/menu/focus, unsaved edits, drawer focus trapping, paired coordinate deletion,
  hash Back/Forward/reload, mobile menu clamping, no horizontal page overflow,
  reduced motion, failure/retry, 401 logout and polling cancellation. No uncaught
  browser exceptions were observed.
- Impeccable detector returned `[]` after resolving the legacy toast border.
  Placeholder contrast is explicit; chart axis text retains readable size on
  narrow viewports. Screenshots were inspected on desktop and mobile. Token
  contrast checks cover ten foreground/background pairs; all pass 4.5:1
  (lowest 4.51:1, placeholder 5.04:1). Health tests also cover all 24 valid
  combinations of optional components and 13 resource threshold cases.

## Reproducing browser checks

From `admin-api`, with writable Go temporary/cache locations:

```powershell
$env:GOTMPDIR=$env:TEMP
$env:GOCACHE=Join-Path $env:TEMP 'codex-exitnode-go-cache'
$env:GOTOOLCHAIN='local'
go build -o ../output/playwright/admin-fixture.exe .
```

From the repository root:

```powershell
node tools/dashboard_smoke.cjs
```

The harness finds an installed cached `playwright-core` and Chromium, or accepts
`PLAYWRIGHT_CORE_PATH` and `DASHBOARD_CHROME`. A restricted Windows sandbox may
require approval to launch the local browser. The fixture binds loopback only,
uses a temporary database and shuts down its test admin process afterwards.

Generated local artifacts (gitignored) are in `output/playwright/`:

- `acceptance.json`: passed scenarios and browser identity.
- `contrast.json`: foreground/background token contrast results.
- `overview-desktop.png`, `devices-desktop.png`, `drawer-desktop.png`.
- `overview-mobile.png`, `devices-mobile.png`, `drawer-mobile.png`, `users-mobile.png`.
- `fixture-admin.log`, `fixture-launch.log` and text snapshots for diagnosis.

## Delivery status

Local implementation and acceptance are complete. The subsequent user-authorized
production deployment is recorded below. Hardware throughput/soak acceptance,
RSSI firmware, billing, orders and external notifications remain separate work.
The existing firmware release index and latest/stable selections have not changed.

## ISP and C3 clarification follow-up (2026-10-22)

- Added embedded, locally served AIS, True, 3BB, dtac and NT logos in the
  device Location cell and drawer. Full legal ISP names remain in tooltips;
  unknown names stay visible as escaped text. Asset provenance is recorded in
  `admin-api/dashboard/isp/SOURCES.md`.
- Capacity now says **Speed not measured** / **Measure speed** to explain
  that it is an internet-throughput test, separate from board RAM/CPU reports.
- Firmware before 2.4.23 gets an explicit upgrade explanation and Update FW
  action. Current firmware with no STATS gets a waiting/report-path message,
  rather than an incorrect instruction to upgrade an already current board.
- Extended browser acceptance checks for all five loaded local logos, AWN
  aliasing, unknown-provider escaping, similar-name non-matching and old/current
  firmware health hints. Existing desktop/mobile acceptance continues to pass.
- No board was flashed, measured, or reconfigured by this UI follow-up.

## Production deployment (2026-10-22, Asia/Bangkok)

- User explicitly authorized deployment. Pushed `main` from `507bc77` to
  `50c82260e548df8f3a889729a454ee5f7e2182db`, then triggered Coolify manually.
- Coolify deployment `vzz99qns9ndckzuhxrzzmgvp` reports **Success / Finished**;
  its import log confirms the exact commit above. Started 02:04:45 +07;
  new application started 02:06:28 +07 (duration 1m43s).
- All four services (admin-api, broker, gost, flasher) started. The existing
  `j2qrpgae6rhrcuxiferirwn0_admin-data` and `_broker-data` volumes remain mounted
  at `/data`. No additional volume backup was made: automatic approval review
  rejected the Backup button because the backup destination was unknown.
- Authenticated production browser checks at `https://admin.jbacteam.com`
  confirm the new seven-column Devices UI, all three connected boards, three
  existing active users, live C3 RAM/CPU, locally loaded AIS SVGs and working
  device history/activity responses. History correctly starts empty; this does
  not establish accumulated production traffic history or a throughput soak.
- After the next 60-second sample, Overview shows **3/3 online**, **3/3 users
  ready**, **1 occupied seat / 0 available**, and broker TLS uptime. The initial
  sample preceded board reconnect and briefly showed 0/0 in Overview while the
  live Devices endpoint already showed three boards.
- Startup logs showed saved slot pushes arriving before broker registration.
  Board 1's slot mismatch also existed before deployment. After separate explicit
  user approval, used **Re-push routing to broker** to send its existing shared,
  slot 1 / port 3131 settings. The success toast appeared and the mismatch and
  re-push button disappeared from refreshed device details. No C3 slots were
  assigned, no capacity tests were run, and no firmware OTA was initiated.
- The sampler subsequently emitted the routing alert's **cleared** event;
  Overview has no critical/warn alerts and retains two capacity info alerts.
- At 02:10 +07 the first closed 5-minute bucket became visible in the real
  production chart. Keyboard-opened Table view shows prior days as missing
  (`—`) and the observed day as `0 B`, matching no relayed traffic. This verifies
  a production metrics flush/read without claiming nonzero throughput or soak.
- C3s `1c:db:d4:c5:7e:b0` and `ac:a7:04:ba:ae:c0` both have no slot and no
  measurement. CPU 1% is independent of capacity. All three boards share one
  exit IP; Board 1's saved 5.3 Mbps measurement permits one shared group seat,
  currently occupied. A positive C3 measurement does not automatically add
  capacity beyond this IP group's maximum eligible board limit.
- Browser console reported no errors/warnings. Production mobile Devices checked
  at 375x812 has no horizontal page overflow; viewport override was reset.
  Direct unauthenticated Python HTTP checks received 403; browser verification
  above succeeded. No raw authenticated API response or customer proxy traffic
  test was collected, and no independent production schema-version query was run.
- Screenshots are local, gitignored evidence in `output/playwright/`:
  `production-deployment-2026-10-22.jpg`, `production-overview-2026-10-22.jpg`,
  `production-devices-2026-10-22.jpg`, and
  `production-devices-mobile-2026-10-22.jpg`.
