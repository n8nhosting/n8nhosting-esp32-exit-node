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

## Device Settings / Assign / flasher follow-up (2026-10-22)

- Device Settings exposes `capacity_mbps` (finite numeric 0–100000 Mbps,
  omission keeps, zero resets unmeasured). Manual overrides clear automated
  measurement metadata and return `capacity_source: manual`; ordinary tests
  return `measured`, zero returns `unmeasured`. Existing users are not removed
  when capacity is lowered. Seat admission and exit-IP grouping remain unchanged.
- Unnamed boards display `Board-XXXX` from the last four uppercase MAC hex
  digits, including in alerts. Existing operator names and stored records are
  retained. This is a display fallback, not a bulk database rewrite.
- All ten device detail sections remain in the 640px/full-mobile drawer, with
  keyboard-accessible tabs and no visible scrollbar. Overflow remains reachable
  by touch, wheel and keyboard in short/zoomed windows or long notes/history.
  Activity pagination exposes all 50 fetched events instead of truncating at 12.
- Device editor tabs retain General, Location, WiFi and Notes fields. Assign is
  a primary Devices action; its board-first dialog fixes the clicked MAC and
  supports slot-only setup before measurement, or optional user assignment.
  Partial user-assignment failures explicitly retain the saved slot. Closing
  during a delayed slot save does not submit the cancelled user assignment or
  mutate a newer modal. Controls are locked while each save is pending.
- Keyed refresh patches each unfocused cell independently, preserving the
  focused action/menu while immediately displaying saved names and capacity.
- Flasher matches admin tokens, typography and controls. All eight release
  entries, setup text, manifest selection, query links and ESP Web Tools scripts
  remain intact; no firmware images or release selections were changed.

Local verification:

- Both Go modules: `go test ./...` and `go vet ./...` pass. New backend tests
  cover manual override validation/persistence/reset, concurrent measurement,
  same-IP admission, default names and safe events; targeted race checks pass.
- `tools/dashboard_settings_smoke.cjs`: seven functional groups pass against
  real local Go handlers and isolated SQLite, with fake broker/geo plus explicit
  simulated 409 and 50-event responses. Checks cover desktop 1440/1024 and mobile
  375x812, keyboard tabs/focus, 10-second refresh, save/cancel races and overflow.
- Flasher: 1440x900, 800x900 and 375x812 pass with the real custom element loaded;
  seven contrast pairs pass 4.5:1, release tests pass 10/10, no page exceptions.
- Impeccable detector returns `[]` for both changed UI files. Evidence is in
  gitignored `output/playwright/settings-acceptance.json`, `settings-*.png`,
  `flasher-acceptance.json` and `flasher-*-viewport.png`.

Production rollout of this follow-up:

- Coolify deployment `xcv9cfxrtzabxa8odqyz4fd9` imported exact commit
  `0c8083e53b84a1806dcea4fd3b2934df8bbe4dc2` and finished successfully on
  2026-10-22. Log start 02:32:47 +07; all four new services started by
  02:34:07 +07 and the build container shut down at 02:34:08 +07.
- Authenticated production browser confirms `Board-7EB0`, `Board-AEC0`, retained
  custom `Board 1`, three primary Assign buttons, ten device tabs, and General
  Settings with the existing 5.3 Mbps value in the editable speed field. Opened
  Assign for Board-7EB0, confirmed its fixed MAC, slot selection and three users,
  then cancelled. No production capacity values, C3 slots or user assignments
  were changed as a test, and no hardware flash/OTA was initiated by this work.
- The existing startup slot-push race recurred; under the user's explicit prior
  authorization, re-pushed Board 1's saved shared slot 1 / port 3131. Refreshed
  Routing has no mismatch/re-push action. This does not fix the startup race;
  subsequent deployments should check saved routing after boards register.
- Overview confirms 3/3 online, 3/3 users ready, 1 occupied / 0 available group
  seats, no critical/warn alerts, and retained pre-deploy metric/activity history.
  The existing high CPU report on Board-AEC0 was present before this deployment;
  backend/dashboard changes did not flash or configure its firmware.
- Production flasher matches the theme, loads all eight releases / nine picker
  options, and has no horizontal overflow at its checked 1440px and 375x812
  viewports. Admin production desktop checked at the normal 1920px viewport;
  exact 1440/1024/800/375 admin coverage is from the isolated local tests above.
  Browser consoles reported no errors/warnings on either production page.
- Viewport overrides were reset. Production screenshots are gitignored local
  artifacts: `production-{devices,settings,assign,flasher,deployment}-refinement.jpg`
  and `production-flasher-mobile-refinement.jpg` in `output/playwright/`.

## Users assignment button correction (2026-10-22)

The earlier Assign refinement covered Devices only; Users still had small
inline assign/change links touching the endpoint text. Users now groups the
board/pool name and port above a separate primary **Assign board** or **Change
board** button, with a 12px gap and minimum 44px height. Both buttons retain the
existing user-first picker and backend assignment behavior.

Local verification: Dashboard Go marker test passes; browser regression passes,
including a real isolated unassigned account, both button labels, measured
spacing/height at 1440px and 375px, no page overflow and opening the correct
user's existing picker. Impeccable detector returns `[]`. Local screenshots are
`output/playwright/users-desktop.png` and `users-mobile.png`.

Production: Coolify deployment `2nf5dznhfdwdgapldawixvhn` imported
`c209fa1236e6ea59f74a242dbb4cdc8c9cfbc9d6`; log start 02:39:55 +07, all new
services started by 02:41:16 +07 on 2026-10-22. Browser verification found two
Assign board buttons and one Change board button, each 44px high with a 12px
gap from endpoint information. Opened earth's picker and cancelled without
changing a customer binding. Production 375x812 has no horizontal overflow;
viewport was reset and browser console was clean. Screenshots:
`production-users-assignment.jpg` and `production-users-assignment-mobile.jpg`.
Re-pushed the previously authorized shared slot 1 for Board 1 after the known
startup registration race; refreshed Routing no longer has a mismatch action.
Only Board 1 and Board-AEC0 were registered at this check; Board-7EB0 was absent
from the broker list, so this rollout does not claim three-board hardware
acceptance. No board firmware or C3 routing was changed.

## Automatic slots and complete device details (2026-10-22)

This revision supersedes the tabbed detail sheet described above. All ten
sections now appear together in three aligned desktop columns; Notes shares
the Traffic/Activity column to balance the available height. The sheet expands
up to 1440px. At 1440x900 the checked normal content, including four Activity
entries, fits entirely inside the frame. Phones use a single column. Scrollbars
are hidden; long notes, expanded history tables, short windows and phones retain
wheel/touch/keyboard scrolling rather than losing data. Settings retains its
existing editor tabs. Devices and the detail header explicitly show Slot/Port.

The user explicitly approved real automatic reservation for existing unassigned
and newly registered boards. Backend commit `5de2813` introduces migration v6,
durable reservations, the authenticated read-only `/api/slots?mac=<target>`
picker contract and reconciliation on the existing sampler. Positive slots and
dedicated policy remain intact. Offline/absent reservations count as occupied;
SQL UPSERT cannot delete another board on a slot conflict. Explicitly saving
slot 0 opts out. Port remains 3130 + slot; no customer is bound automatically.
The editor explains the derived port and opt-out behavior. Delayed Settings and
Maps responses cannot overwrite a later device editor.

Local verification: admin-api full `go test ./...` (4.881s) and `go vet ./...`
pass; broker full tests/vet pass. Focused backend race checks pass (3.161s).
New tests cover migration, existing metadata preservation, concurrent allocation,
slot exhaustion, offline reservations, dedicated adoption, failure/restart/retry,
explicit clear, manual edit versus poll, no repeated reservation events and no
SQLite transaction held over broker HTTP. Dashboard marker checks pass and
Impeccable detector returns `[]`.

Combined browser verification: regression passes 11 groups; focused acceptance
passes 9 groups against the rebuilt Go backend and isolated SQLite. Coverage
includes every detail section fitting at 1440x900, 375x812 navigation with hidden
scrollbars, all 50 Activity entries, keyboard/focus and refresh preservation,
SQL-only offline slot 12 excluded from the real picker, and delayed Settings /
Assign cancellation races. No uncaught browser errors. Evidence is in
`output/playwright/settings-acceptance.json` and
`settings-{drawer-desktop,drawer-mobile,drawer-mobile-bottom,devices-desktop}.png`.

Production: initial deployment `4ikmzplifmlyfbovwmw1c4wn` imported
`0f04a51f17984fdf5f4306efa659129786471f9c` and finished successfully with all
services started by 03:01:54 +07. After the first successful sampler following
board reconnects, the live dashboard confirmed Board 1 retained slot 1 / 3131,
Board-7EB0 received slot 3 / 3133, and Board-AEC0 received slot 4 / 3134. Saved
offline slot 2 remained unavailable. Board 1's mismatch cleared automatically;
no manual re-push was necessary. Settings showed the real slot as editable and
the derived port. Assign retained current slot 3 and omitted occupied 1/2/4;
the verification dialog was cancelled without changing a customer binding.

All ten production sections fit inside the checked 1440x900 frame; 375x812
has no horizontal overflow, retains all sections and supports keyboard scrolling
with `scrollbar-width:none`. Screenshots are
`production-device-details-complete.jpg` and
`production-device-details-mobile-complete.jpg` under `output/playwright/`.
Live manual capacity and firmware values present before this deployment were
retained: Board-AEC0 1 Mbps manual / 2.4.22-c3,
Board-7EB0 unmeasured / 2.4.23-c3, Board 1 5.3 Mbps / 2.4.21-mux1.4. This work
did not initiate hardware flash/OTA or assign customers automatically.

Final copy correction: deployment `ery4rhcuvftl5epmvvdzanjz` imported
`08220c97431b074402c57b606fc63b9b616ce592`, started all services by 03:05:20 +07
and finished successfully. Assign help now describes saving the slot separately
from assigning a customer, including slots already reserved automatically.
Reloaded production confirms reservations 1/3/4 persisted through restart,
current slot 3 remains selected, and occupied 1/2/4 are absent from its picker.
Production was rechecked at exact 1440x900 and 375x812; console has no captured
errors/warnings. Viewport overrides are reset after verification.

## Independent seats per board (2026-10-22)

The operator explicitly selected independent seats per board even when public
IPs are identical. This supersedes earlier same-IP shared ceilings and
dedicated-IP ownership in this handoff. Backend commit `7b30608` counts
`active_users` only on the target MAC, admits against that board's own limit,
and sums free seats independently across routable online boards. Dedicated
reserves one board for one account; IP-group fields remain informational.
Shared still requires a positive measured/manual capacity for any seat.

Devices and Capacity now show board occupancy and capacity using the same unit;
an unused unmeasured C3 displays 0/0, and an unused 1 Mbps C3 displays 0/1.
Both assignment pickers use local limits and no longer exempt a transfer merely
because its source shares the target's IP. The user-first picker also shows
partially occupied boards with room. Settings, Overview and assignment help
describe the new board ownership model.

Local backend verification: full admin-api tests pass (5.479s), vet passes,
targeted race tests pass (9.120s), and the focused unmeasured-neighbor check
passes (1.459s). Broker tests/vet pass. No database migration, user reassignment,
capacity override, firmware operation or history rewrite is required.

Combined browser verification passes 11 regression groups and 10 focused
groups against the real rebuilt Go API with isolated SQLite and fake broker.
The same-IP scenario proves both pickers can assign an empty measured C3 while
Board 1 is full; unmeasured C3 remains 0/0; full C3 rejects with 409; moving an
existing Board 1 user cannot bypass the C3 limit; and a 1/2 C3 appears in the
default picker. Local evidence includes `settings-acceptance.json` and
`settings-same-ip-c3-{available-desktop,desktop,partial-desktop}.png` under
`output/playwright/`. Impeccable detector returns `[]`; focused code review
found no remaining cross-IP admission conditions.

Deployment was paused while Board 1 showed `updating` to 2.4.23-s3 since
03:09 +07, while still running 2.4.21-mux1.4. The operator confirmed this was
a stale status and they were no longer waiting for OTA, so deployment resumed.
Both C3 boards already had 1 Mbps manual capacity before this change. No
production capacity values or customer bindings were modified during
verification.

Production deployment `fhuwrkeregrwrbbxmfs5x3zg` imported
`46f2af15b41a8b7f4292d5858c504c6336baaef0`; log start 03:22:25 +07, all services
started by 03:23:47 +07 on 2026-10-22, and deployment finished successfully.
The reloaded dashboard confirms Board 1 has 1/1 seats and each C3 has 0/1,
retaining slots/ports 1/3131, 3/3133 and 4/3134. After sampler reconciliation
there are no routing-mismatch notices. Overview shows 1 occupied and 2 available
online seats across 3/3 online boards, with the new per-board help text.

For the existing active, unbound `esp32c3` account, the default user-first
picker shows both C3 boards eligible with 0/1 seats and an enabled Assign button.
Board-AEC0's board-first picker also shows 0/1 board seats and enables Assign
for that account. Both dialogs were cancelled; no production assignment was
submitted. Actual POST assignment success and full-target rejection were
verified with the real Go handlers in the isolated local tests above.

Production desktop verification used 1440x900; 375x812 has no horizontal
overflow. Console has no captured errors/warnings; viewport override was reset.
Screenshots under `output/playwright/`:
`production-per-board-seats-picker.jpg`, `production-per-board-seats-devices.jpg`
and `production-per-board-seats-mobile.jpg`. No firmware update was initiated.

## OTA confirmation and real C3 checks (2026-10-22, in progress)

The operator approved fixing OTA tracking and testing actual C3 transfers,
parallel connections and reconnection. This section supersedes the earlier
statement that no firmware action was initiated. Customer bindings are retained.
See [OTA and diagnostics contract](../superpowers/specs/2026-10-22-ota-confirmation-and-diagnostics.md).

At 03:50:37 +07 the production broker sent the archived testing image
`2.4.23/esp32c3/esp32_nat_router.bin` to Board-AEC0 (`ac:a7:04:ba:ae:c0`,
slot 4 / 3134). Its mux link closed immediately and returned at 03:50:55;
HELLO reported `2.4.23-c3` and OTA became `ok`. This is a confirmed C3 OTA
and 18-second reconnect, not merely a successful HTTP enqueue. The latest/stable
release pointer remains unchanged. Screenshot: `ota-aec0-success.jpg`.

Normal **Measure speed** ran through the real production gost slot and board,
using the built-in temporary seatless measurement account:

| Board | Port | Actual object | Stored result | Completion (+07) |
|---|---:|---:|---:|---|
| AEC0 | 3134 | 8,388,608 bytes | 2.4 Mbps | 03:54:07 |
| 7EB0 | 3133 | 8 MiB object | 1.6 Mbps | approximately 03:55 |

Both remain at 1/1 board seats. These real measurements replace their previous
1 Mbps manual capacities; no customer was reassigned. AEC0's relayed counters
increased by 8 MiB; RAM/CPU reports now appear. Screenshots and sanitized broker
chronology are under `output/playwright/ota-*`.

Open observations: Board 1 still runs `2.4.21-mux1.4`; its previous S3 OTA was
sent at 03:33:37 but has not confirmed the target. The old dashboard incorrectly
keeps its firmware pill at “updating” after 15 minutes. AEC0 reports 100% CPU
even in a later zero-stream sample, while 7EB0 returns to 1% after load; this
is recorded telemetry, not yet an established firmware fault. Separately,
7EB0's uptime reset around 03:53:05–03:53:07 before its measurement. The agent
did not initiate that reset or open serial; the cause is unconfirmed.

OTA reliability changes and bounded diagnostics are being validated locally;
they are not yet claimed deployed by this section. Parallel and longer-duration
hardware acceptance remains open until actual results below are recorded.

Local verification completed before deployment: broker full tests/vet plus
targeted OTA/storage/race cases; admin full tests (5.878s)/vet plus targeted
diagnostic and OTA race cases. Failure fixtures prove no command is sent when
the durable dispatch marker cannot be saved, including mkdir/write/rename
failures. New diagnostics cover strict bounded options, actual parallel bytes,
wall-clock aggregate rate, partial failure, cancellation, temporary-account
cleanup and unchanged capacity for `persist:false`.

One existing speed test initially failed during simultaneous ESP-IDF builds:
its fixed “16 Mbps” expectation assumed scheduler sleeps were exact. Commit
`0699ae9` checks returned Mbps against observed transfer timing and byte counts
instead; no production formula changed. The corrected full suite passes.

Browser regression passes 11 groups; settings/per-board-seat regression passes
10 groups. The additional connection/OTA helper passes 5 groups, covering
truthful status/reason copy, post-OTA Health focus refresh, bounded request
payload, incomplete-stream results, 524 recovery, refresh pausing, Stop/late
responses, mobile layout, Escape and navigation cancellation. Diagnostic
responses in this local helper are explicitly simulated; they are not hardware
throughput evidence. Impeccable detector returns `[]`. Local screenshots and
`connection-acceptance.json` are under `output/playwright/`.

Initial production deployment `ezzouvfz4hxitljhemvthnhd` imported
`4302ca7ebeb82a45072c6da131064668a4dc31db`, started at 04:06:53 +07 and finished
with all four services started by 04:08:20. Production reconnect exposed an
existing storage defect: `/data` inside the broker volume was mode 0755,
UID/GID 0/0, and `/data/nodes.json` did not exist. The broker runs as UID10001;
the old image never prepared a writable data directory, and its old save path
ignored errors. Broker-only OTA metadata was absent after this redeploy, and
the verified permissions explain why writes by this runtime could not succeed.
Admin SQLite retained slots 1/3/4, names, capacities and customer assignments.

Commit `de24873` prepares `/data` owned by `app` before switching to the nonroot
runtime, matching the admin image. Existing-volume ownership repair and a second
deployment are required before claiming OTA/restart persistence on production.
The repair targets only this application's verified broker volume, with no
volume deletion, content reset or broad permission changes.

The runtime repair verified Docker mount
`j2qrpgae6rhrcuxiferirwn0_broker-data` at `/data`, then changed only that
directory's owner to UID/GID10001. Mode remains0755. The next genuine OTA
request created `/data/nodes.json` mode0600 owned10001, containing slots1/3/4,
AEC0 target24-c3/ok and S3 target24-s3/sent. No existing file was erased.

After firmware builds passed for both targets, source commit `7d9d75a` and
testing archive `c12fdf3` preserved all53 old binaries/manifests. Chip versions,
hashes and unchanged partitions are verified in
`output/playwright/firmware-2.4.24-validation.json`. Firmware archive tests10/10
pass. Latest remains `2.4.21-mux1.2`.

AEC0 canary: command written04:11:26 +07; origin logged an actual ESP32 HTTP
Client GET with200/1,217,232bytes at04:11:28; link returned04:11:45 reporting
`2.4.24-c3`/OTAok. Reconnect took19seconds. Its previously persistent idle100%
CPU became1% on successive30/60-second reports, with141KiB free of285KiB and
116KiB boot low-water. CPU counter formula was unchanged. This confirms the
disconnected-console yield fixes the observed standalone C3 idle saturation.

S3 received one24-s3 request at04:12:26. The broker confirms a full command
write; at the following check it still reported21-mux1.4 and had not confirmed
the target. Do not claim successful download, rollback or completed update.
