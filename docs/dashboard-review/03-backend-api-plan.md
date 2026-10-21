# Dashboard widgets — backend / API plan

Repo: `esp32-exit-node-v1.4-mux1.3-relaybuf/repo` (branch `claude/c3-exit-node`), read-only review on 2026-10-21.
`go test ./...` passes in `admin-api/` and `broker/` (both `ok`, cached) — nothing in the repo was modified.

Ownership rule used throughout: the **broker** is the live truth (registry + atomic
counters, `nodes.json` for policy only, no history, no DB); **admin-api** owns everything
persisted or derived (SQLite `platform.db`, single origin for the dashboard). Broker changes
in this plan are additive JSON fields only; the current dashboard keeps working untouched
because every change is a new field or a new endpoint (the Phase A/B plan's "add fields, never
rename" rule).

---

## 1. Data inventory

Line references: `R` = `broker/registry.go`, `BM` = `broker/main.go`, `D` = `admin-api/devices.go`,
`DS` = `admin-api/devices_store.go`, `S` = `admin-api/store.go`, `G` = `admin-api/geo.go`,
`GP` = `admin-api/gost_plugins.go`, `A` = `admin-api/admin.go`, `DB` = `admin-api/db.go`.

| Widget | Exists today (source) | Missing | Owner |
|---|---|---|---|
| **KPI: devices online** | broker `GET /status` → `nodes_online` / `nodes_total` (`BM:798-799`, from `registry.onlineCount()` `R:169-179`; a node is online if it has idle/active/mux links or a HELLO < 45 s ago, `R:68-71`). Surfaced as `/api/overview.broker.nodes_online` (`D:441-449`). Per node: `online` on `GET /nodes` (`R:86-89`). | 30-day history / sparkline. | broker (live), admin-api (history) |
| **KPI: active users** | `/api/overview.users.active` (`D:424-431`) = count of `Enabled` users — **not** "connected now". "Takes a seat" (enabled + unexpired) is `takesSeat` (`admission.go:32-42`). gost's observer already sends `currentConns` per client (`GP:131-135`) but it is ignored (`GP:154-177`). | `users.connected_now` (clients with a stats event in the last 60 s), history. | admin-api |
| **KPI: aggregate throughput** | broker `/status.bytes_up/bytes_down` cumulative since broker start (`BM:807-808`, `b.started` `BM:110`, `uptime_sec` `BM:809`); per node `bytes_up/bytes_down` (`R:45-46`). Counters live in RAM only: `persistedNode` deliberately excludes them (`R:318-334`), so they reset on broker restart. | A rate (bytes/s) — needs two samples; history. | admin-api (derive from samples) |
| **KPI: GB this month** | `/api/overview.month_gb` = Σ `users.usage_bytes` (`D:438-447`), monthly reset per user via `rollMonth` (`S:217-224`), fed by `/gost/observer` (`GP:147-179`). Caveat: a user's counter rolls only on their first event in the new month, so the sum is slightly stale on the 1st. Broker bytes ≠ observer bytes (relay side vs customer side), use one source per widget. | 30-day daily series; per-plan-period quota is Phase C. | admin-api |
| **KPI: MRR** | Nothing. No `plans`/`orders` tables (spec §7, Phase C). | Everything. Return `null` and let the dashboard hide the card until Phase C. | admin-api (Phase C) |
| **Per-device health gauge** | Live fields on `/nodes`: `online`, `blocked`, `idle_conns`, `mux_links`, `active_dials`, `dials_ok`, `dials_err`, `bytes_*` (`R:40-46`), `uptime_sec` (`R:22`), `firmware`, `chip`, `ssid`, `remote_ip`. Record fields: `capacity_mbps`, `capacity_measured_at`, `capacity_active_streams` (`DS:19-21`), `max_users`/`active_users` (`D:86-87`, `maxUsers` `D:93-102`). | `max_streams` per node (known in `helloOpts`/`muxSession.maxStreams` `mux.go:90`, never put on the node), free heap / RSSI (firmware logs heap locally once a minute `rproxy.c:736-742`, never sent), any history (availability, error rate over 24 h). | broker (`max_streams`, later heap), admin-api (score) |
| **Status pill states** | `blocked` (`R:25`), `online` (`R:88`), `ota_status` ∈ queued/sent/ok/failed + `ota_at`/`ota_target` (`R:33-35`; transitions `R:130-147`, `R:197-214`, `R:232-243`), `capacity_mbps == 0` = unmeasured (`D:93-102`, `admission.go:126`), `slot_mismatch` (`D:81,155`). | A single server-side `status` string so dashboard + future portal agree (today the dashboard derives online/offline/blocked itself, `index.html:371`). | admin-api |
| **Location (flag + city, clickable)** | Operator `lat`/`lon` (`DB:40-41`, `DS:22`, `D:75-76`, validated `D:255-262`); geo-IP `country`, `city`, `region`, `isp`, `lat`, `lon` from ip-api.com cached 6 h in memory (`G:14-21`, fields list `G:52`). Dashboard links to OpenStreetMap (`index.html:376`). | `country_code` (ip-api has `countryCode`, not requested); `maps_url` column; Google Maps link resolver (§4). | admin-api |
| **Due date** | `users.expires_at` RFC3339, "" = never (`S:112`, `PublicUser.ExpiresAt` `S:128`); expiry logic in `takesSeat` (`admission.go:36-40`), `Authorize` (`S:324-328`), overview (`D:431-435`); `/extend` (`A:220-237`), webhook extends (`webhook.go:79-95`). Device row already lists `users []string` (`D:64`). | `next_expiry` on the device row (earliest expiry among its seat-taking users); a `status` on `PublicUser`. | admin-api |
| **Sparklines / 30-day charts** | Nothing persisted anywhere. | Sampler + tables + read endpoint (§2). | admin-api |
| **Activity feed** | Only log lines: broker `mux link up/down` (`BM:303,316`), `ota: pushing` (`BM:267,296`, `mux.go:276`), OTA ok/failed set silently in `touch` (`R:142,145`), `socks: slot device … has no usable link` (`BM:378`); admin-api `audit reveal-wifi` (`D:368`), `measure …` (`measure.go:176`), `payment: activated` (`webhook.go:100`), `auth deny` (`GP:39`). | `events` table + emitters + `GET /api/events` (§3). | admin-api |
| **Alerts** | Inputs exist (`online`, `ota_status`, `usage_bytes` vs `quota_gb`, `expires_at`, `dials_pinned_offline` `BM:804`, `dials_no_node` `BM:803`, `slot_mismatch`). | Evaluator + `GET /api/alerts` (§6). | admin-api |
| **"Orders" table / "top items"** | No orders (Phase C). `GET /api/users` has username, device, rate, quota, usage, expiry (`S:125-137`). | `status` on `PublicUser`; per-id totals from metrics for "top devices/users by traffic". | admin-api |

Two small broker additions pay for themselves (both optional, S): `max_streams` (set in
`registry.touch` from `helloOpts.MaxStreams`) and `link_since` (set in `serveMux` after
`READY mux1`), so the health gauge gets stream headroom and the feed gets exact online
timestamps. `admin-api/devices.go:34-59` `brokerNode` ignores unknown fields, so they are safe
to add first and consume later; `brokerNode` is also missing `mux_links`, which the broker
already sends.

---

## 2. Time series

### Where it runs

| Option | For | Against |
|---|---|---|
| **admin-api poller goroutine → SQLite** (chosen) | SQLite and the schema-version mechanism already exist (`DB:13-45`); admin-api is the dashboard's only origin; per-user bytes can only come from admin-api (observer) anyway, so one sampler covers nodes, users and fleet; survives broker restarts; one `GET /nodes` + `/status` per minute is ~5 KB. | 60 s resolution for online/offline transitions; bytes moved while admin-api is down are not attributed (counters are deltas). |
| broker-side ring buffer | Exact transition times, no polling. | Broker has no DB and loses RAM on restart; the dashboard would still read through admin-api; per-user series impossible there; new config/coupling. |

Poller: one goroutine started in `main()` after the slot re-push, `POLL_INTERVAL` (default 60 s).
Each tick: `GET /status` + `GET /nodes` (5 s timeout, same `brokerGET`), diff against the
in-memory previous snapshot (transitions → §3 events, §6 alerts), accumulate per-node deltas
into the current 5-minute bucket. Every `METRICS_STEP` (default 5 m) write one transaction.
Observer bytes: `handleObserver` adds its computed `add` (`GP:174-176`) into an in-memory
`map[user]int64` (mutex, reserved `_measure-*` users skipped) that the same flush drains — exact
deltas, no reset ambiguity. Gather everything (broker JSON, `store.List()`, `devices.List()`)
**before** entering `inTx`: with `SetMaxOpenConns(1)` (`DB:56`) any store call inside the
transaction's `fn` deadlocks (`DB:87-90` says so).

Counter resets: per MAC keep the previous cumulative `bytes_up/down`, `dials_ok/err`; `delta =
cur - prev`, and `cur` when `cur < prev` or `/status.uptime_sec` went backwards (broker
restart). First tick after admin-api start has no `prev` → delta 0 (never attribute lifetime
counters to one bucket).

### Schema (migration v4 in `db.go`'s `migrations` slice; v3 is the `maps_url` column of §4)

Timestamps are INTEGER unix seconds here (bucket arithmetic, smaller index) even though
`users`/`devices` use RFC3339 text; the convention is documented on the tables.

```sql
-- v4: metrics (5-minute samples for 48 h, hourly rollups for METRICS_RETENTION_DAYS)
CREATE TABLE metrics_fleet (
  ts            INTEGER NOT NULL,          -- bucket start, unix seconds UTC
  step          TEXT    NOT NULL,          -- '5m' | '1h'
  nodes_online  REAL    NOT NULL,          -- mean over the polls in the bucket
  nodes_total   INTEGER NOT NULL,
  mux_links     REAL    NOT NULL,
  users_total   INTEGER NOT NULL,
  users_seated  INTEGER NOT NULL,          -- takesSeat()
  users_connected INTEGER NOT NULL,        -- distinct observer clients seen in the bucket
  users_over_quota INTEGER NOT NULL,
  users_expired INTEGER NOT NULL,
  bytes_up      INTEGER NOT NULL,          -- delta of broker /status counters
  bytes_down    INTEGER NOT NULL,
  dials_ok      INTEGER NOT NULL,
  dials_err     INTEGER NOT NULL,          -- dials_err + dials_no_node + dials_pinned_offline deltas
  PRIMARY KEY (step, ts)
);
CREATE TABLE metrics_node (
  ts          INTEGER NOT NULL,
  step        TEXT    NOT NULL,
  mac         TEXT    NOT NULL,
  online_frac REAL    NOT NULL,            -- polls online / polls in bucket (0..1)
  bytes_up    INTEGER NOT NULL,
  bytes_down  INTEGER NOT NULL,
  dials_ok    INTEGER NOT NULL,
  dials_err   INTEGER NOT NULL,
  active_max  INTEGER NOT NULL,            -- max active_dials seen in the bucket
  PRIMARY KEY (step, mac, ts)
);
CREATE TABLE metrics_user (
  ts       INTEGER NOT NULL,
  step     TEXT    NOT NULL,
  username TEXT    NOT NULL,
  bytes    INTEGER NOT NULL,               -- observer bytes in the bucket (in+out)
  PRIMARY KEY (step, username, ts)
);
```

Rows are only written when there is something to say (a user with 0 bytes in a bucket gets no
row; readers treat a missing row as 0 for counters and `null` for gauges).

### Rollup and retention (hourly maintenance tick inside the poller)

```sql
-- idempotent: the PK makes re-running a rollup a no-op
INSERT OR REPLACE INTO metrics_node (ts, step, mac, online_frac, bytes_up, bytes_down, dials_ok, dials_err, active_max)
SELECT (ts/3600)*3600, '1h', mac, AVG(online_frac), SUM(bytes_up), SUM(bytes_down), SUM(dials_ok), SUM(dials_err), MAX(active_max)
FROM metrics_node WHERE step='5m' AND ts < (strftime('%s','now')/3600)*3600 GROUP BY (ts/3600)*3600, mac;
-- same shape for metrics_fleet (AVG gauges, SUM counters) and metrics_user (SUM bytes)
DELETE FROM metrics_node  WHERE step='5m' AND ts < strftime('%s','now') - 48*3600;
DELETE FROM metrics_node  WHERE step='1h' AND ts < strftime('%s','now') - :retention_days*86400;
```

`METRICS_RETENTION_DAYS` default 90 (30 is the product need; 90 keeps "vs last month"). Daily
buckets are not stored: a 30-day daily chart is `GROUP BY ts/86400` over 720 hourly rows at
read time.

### Read endpoint

```
GET /api/metrics?entity=fleet|node|user&id=<mac|username>&range=24h|48h|7d|30d&step=5m|1h|1d&fields=a,b,c
GET /api/metrics?entity=nodes|users&range=30d            -- per-id totals (for "top devices/users")
```
Admin bearer (`requireAdmin`), `Cache-Control: no-store`.

Rules: `step` defaults to `5m` for range ≤ 48 h else `1h`; `5m` is refused for range > 48 h
(400); `n = range/step` capped at 1440; `fields` ≤ 8, unknown field → 400 `{"error":"unknown field
x"}`; unknown `id` → 200 with all-`null` arrays (nodes get forgotten, charts must not 404). For
`step=1h` the current partial hour is aggregated from `5m` rows so the last point is never
missing. Counters (`bytes_*`, `dials_*`) aggregate by SUM, gauges (`online_frac`, `nodes_online`,
`users_*`, `active_max`) by AVG/MAX.

```json
{
  "entity": "node", "id": "aa:bb:cc:dd:ee:ff",
  "from": 1758412800, "to": 1761004800, "step_sec": 3600, "n": 720,
  "series": {
    "bytes_up":    [0, 1203391, null, ...],
    "bytes_down":  [0, 19392911, null, ...],
    "online_frac": [1, 1, 0.8, ...]
  },
  "summary": {
    "bytes_up":    {"last": 1.2e9, "prev": 1.1e9, "delta_pct": 9.1, "window_sec": 86400},
    "online_frac": {"last": 0.99, "prev": 1.0, "delta_pct": -1.0, "window_sec": 86400}
  }
}
```
`summary.last` is the last complete window (24 h for ranges ≥ 7 d, else one step) and `prev`
the one before it — exactly what a "+10% vs yesterday" pill needs; `delta_pct` is `null` when
`prev` is 0. Response size for 30 d × 1 h × 3 fields ≈ 10 KB. `entity=nodes` returns
`{"totals": {"<mac>": {"bytes_up": …, "bytes_down": …, "online_frac": …}}}`.

Live throughput (no storage): the poller keeps the last two `/status` samples and exposes
`broker.throughput_bps` on `/api/overview` (bytes delta / interval); the poller's cached
`/nodes` snapshot (≤ 60 s old, with `as_of`) also feeds `/api/alerts` so those pages stop
hitting the broker on every refresh. `/api/devices` keeps its live GET (operators expect fresh
state right after an action).

### Cost (assumptions: 10 boards, 50 users)

| Table | Rows | Size |
|---|---|---|
| `metrics_node` 5m × 48 h | 288 × 10 × 2 = 5,760 | ~0.6 MB |
| `metrics_node` 1h × 90 d | 24 × 10 × 90 = 21,600 | ~2 MB |
| `metrics_user` 5m / 1h (worst case, every user busy every bucket) | 28,800 / 108,000 | ~3 MB / ~10 MB (sparse in practice) |
| `metrics_fleet` | 576 + 2,160 | ~0.3 MB |

Worst case ≈ 16 MB, typical < 8 MB, WAL checkpoints automatically. CPU: one 5–10 KB JSON parse
and an O(N) diff per minute; one write transaction of ≤ 61 inserts per 5 min (< 5 ms); the hourly
rollup groups ≤ 30 k rows (< 50 ms). A 30-day read touches 720 rows (< 5 ms). Nothing the
single-writer SQLite notices next to the observer's 10 s bursts.

### Migration

Append to `migrations` (`DB:15-45`); the loop records each version in `schema_version` inside one
`BEGIN IMMEDIATE` transaction (`DB:65-85`). Keep **one statement per entry** (the loop executes
each entry with a single `ExecContext`; modernc handles multi-statement strings, but one-per-entry
is unambiguous and matches v1/v2). `openTestStore` in `store_test.go` runs the migrations, so new
tables exist in every existing test without changes.

---

## 3. Activity feed

### Sources and emitters

| Event kind | Emitted by | Severity |
|---|---|---|
| `node.first_seen`, `node.online`, `node.offline`, `node.ip_changed` (IP group moves matter for admission), `node.firmware_changed` | poller diff of `/nodes` | info / info / warn / warn / info |
| `node.ota_queued` | `handleDeviceAction` passthrough after a 2xx from the broker (`D:404-413`) | info |
| `node.ota_sent`, `node.ota_ok`, `node.ota_failed` | poller diff of `ota_status` (`R:130-147`, `R:232-243`) | info / info / error |
| `node.blocked`, `node.allowed`, `node.forgotten` | passthrough after 2xx | warn / info / warn |
| `device.updated` (changed fields: name, kind, slot, lat/lon, maps_url, wifi set/cleared — never values of secrets), `device.measured {capacity_mbps, active_streams, bytes}`, `device.wifi_revealed` | `handlePutDevice`, `handleMeasure` (`measure.go:176`), `handleRevealWifi` (`D:368`) | info / info / warn (audit) |
| `user.created`, `user.updated`, `user.deleted`, `user.enabled`, `user.disabled`, `user.extended {days}`, `user.quota_reset`, `user.credentials_rotated`, `user.bound {mac}`, `user.unbound`, `user.binding_refused {reason}` | `handleUsers`, `handleUser`, `userAction`, `handleCredentials` (`reset=1`), `admit` 409 | info (refused: warn) |
| `user.expired`, `user.over_quota` | scheduler transition in the poller tick | warn |
| `payment.received {days, quota_gb, rate_mbps}` | `handlePayment` (`webhook.go:100`) | info |
| `system.started`, `system.broker_unreachable`, `system.broker_recovered` | `main`, poller (2 consecutive failed ticks / first success after) | info / error / info |
| `alert.raised`, `alert.cleared {key}` | §6 evaluator | alert's severity |

Polling vs broker→admin-api webhook: polling needs **no broker change**, is restart-safe
(below) and gives ±60 s timestamps, which is enough for a feed. A webhook (`EVENTS_URL` POST from
`serveMux`, `touch`) would give exact link up/down times but adds a retry queue the broker does
not have and a second config surface; if exact times are ever needed, the cheaper route is the
additive `link_since` field on `/nodes` (§1). Keep the broker's log lines as they are.

Restart safety without extra tables: transitions are deduped against the feed itself.
`lastEvent(subjectType, subjectID, kinds...)` returns the newest matching row (indexed); at the
first tick after start, `node.online/offline` and `user.expired/over_quota` are emitted only if
the current state differs from the last recorded one — so a board that died while admin-api was
down still gets its `node.offline` (timestamped at detection). Pre-existing conditions never spam
the feed.

### Schema (migration v5)

```sql
CREATE TABLE events (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,   -- keyset cursor, monotonic
  ts           TEXT NOT NULL,                        -- RFC3339 UTC, like users/devices
  kind         TEXT NOT NULL,                        -- 'node.offline', 'user.created', ...
  severity     TEXT NOT NULL DEFAULT 'info',         -- info | warn | error
  subject_type TEXT NOT NULL,                        -- node | device | user | system | alert
  subject_id   TEXT NOT NULL,                        -- mac, username, alert key, ''
  actor        TEXT NOT NULL DEFAULT '',             -- admin | webhook | poller | scheduler | system
  message      TEXT NOT NULL,                        -- one human line, already rendered
  data         TEXT NOT NULL DEFAULT '{}'            -- JSON details (never secrets)
);
CREATE INDEX events_subject ON events (subject_type, subject_id, id);
CREATE INDEX events_kind    ON events (kind, id);
```

Emitter: `func (s *server) emit(kind, severity, subjType, subjID, actor, msg string, data any)` —
a synchronous single INSERT (sub-millisecond) called **after** any `inTx`/`Update` returns, never
inside the mutator. Node subjects are lower-case MACs (the broker lower-cases, `BM:236`).

### Endpoint

```
GET /api/events?limit=50&before=<id>&kind=node.&subject=<id>&severity=warn,error&since=<RFC3339>
→ 200 {"events":[{"id":812,"ts":"…","kind":"node.offline","severity":"warn","subject_type":"node",
         "subject_id":"aa:bb:…","actor":"poller","message":"Living room (aa:bb:…) went offline","data":{}}],
       "next_before": 763}
```
Keyset pagination on `id` (newest first; `next_before` = last id returned, `null` when exhausted);
`limit` 1..200 (default 50); `kind` is a prefix match (`LIKE 'node.%' ESCAPE '\'`, with `%`/`_`
escaped); `severity` is a comma list; 400 on a bad cursor/limit. Retention:
`EVENTS_RETENTION_DAYS` (90) and a hard cap of 100 k rows, purged in the hourly maintenance tick
(`DELETE … WHERE id < (SELECT MAX(id) - 100000 FROM events)`).

---

## 4. Google Maps link → coordinates

### Surface

- `POST /api/geo/resolve {"url":"https://maps.app.goo.gl/Wi3Rzy4X2tRmbQW77"}` (admin bearer) →
  `200 {"lat":16.7923,"lon":100.2617,"source":"pin","final_url":"https://www.google.com/maps/place/…","maps_url":"https://www.google.com/maps?q=16.7923,100.2617"}`.
  The dashboard's device editor gets one "Paste Google Maps link" field; on blur it calls resolve,
  previews the coordinates/link, and the existing `lat`/`lon` inputs are filled (still editable).
- `PUT /api/devices/{mac}` accepts `maps_url *string` (≤ 512 chars). Non-empty: resolve it with the
  same rules, set `lat`/`lon` from it **unless** `lat`/`lon` are also in the body (explicit wins),
  store the original link in the new `devices.maps_url` column (migration v3:
  `ALTER TABLE devices ADD COLUMN maps_url TEXT NOT NULL DEFAULT ''`; add it to `deviceCols`,
  `scanDevice`, `deviceArgs` and the 12th `?` in `Upsert`, `DS:31-73,122`). `""` clears the stored
  link and leaves `lat`/`lon` alone. Resolve **before** the broker slot push (`D:292-304`) so a bad
  link cannot leave the broker changed while nothing is saved — same "nothing saved on refusal"
  contract the handler already has. One-shot API clients can therefore do it in one call.
- `deviceView` gains `maps_url` (stored original) and the dashboard renders the location cell as
  `🇹🇭 Phitsanulok` (flag from `geo.country_code`, see below) linking to
  `https://www.google.com/maps?q=<lat>,<lon>` (deterministic, always the stored pin; the Maps URLs
  API form `https://www.google.com/maps/search/?api=1&query=lat,lon` is equivalent), with the original
  `maps_url` as a secondary "open original" link when set. Flag: request `countryCode` from ip-api
  (`G:52`, add to the struct `G:14-21` as `CountryCode string json:"country_code"`) and let the
  dashboard build the emoji from the two regional-indicator code points; no new storage.

### Resolver rules

- Input: `http`/`https` (http upgraded to https in the handler) or `geo:` URIs; ≤ 2048 chars.
- Host allowlist (initial URL **and every redirect hop**): `maps.app.goo.gl`, `goo.gl`,
  `[www.|maps.]google.<tld>` with `<tld>` ∈ `com | xx | co.xx | com.xx` (the operator is in
  Thailand: `google.co.th` must work). Anything else → 400 `{"error":"host not allowed"}` for the
  input, or `{"error":"redirect outside google maps"}` when a hop leaves the allowlist (a
  `goo.gl` link to anywhere else is refused at its first hop).
- Fast path: if the pasted URL already carries coordinates, no network call at all.
- Network: a dedicated `http.Client`, `Timeout` 6 s (handler context 8 s), `DisableKeepAlives`,
  `Proxy: nil` (never the environment proxy), at most 5 hops (`CheckRedirect`), GET with the
  body **never read** (`resp.Body.Close()` right after headers; with keep-alives off nothing is
  drained), no cookies, concurrency bounded by a 4-slot semaphore.
- SSRF: beyond the allowlist, the transport's `DialContext` resolves the host once, drops any
  non-public address (loopback, RFC1918, link-local, CGNAT `100.64/10`, ULA, multicast,
  unspecified) and dials the first public IP — TLS verification still uses the hostname because
  the transport, not the dialer, does the handshake. The consent interstitial
  (`consent.google.com/m?continue=<maps url>`) is not followed: the redirect is stopped and the
  `continue` parameter is parsed instead.
- Validation: lat ∈ [-90, 90], lon ∈ [-180, 180], not both 0 ("Null Island" = missing data),
  rounded to 6 decimals. Precedence when a URL carries several pairs: the place pin
  (`!3d<lat>!4d<lon>` in the `data=` path segment) > explicit query (`q=`, `query=`, `ll=`,
  `center=`, `destination=`) > coordinates in the path (`/maps/place/lat,lon`,
  `/maps/search/lat,+lon`) > the viewport centre (`/@lat,lon,17z`). Rationale: `@` is the camera,
  `!3d!4d` is where the marker is.
- Shapes supported: `/@lat,lon,zoom` (also street-view `…,3a,75y,…`), `!3dLAT!4dLON`,
  `?q=lat,lon`, `?q=loc:lat,lon`, `?query=lat,lon` (Maps URLs API), `?ll=lat,lon`, `?center=`,
  `?destination=`, `/maps/place/lat,lon`, `/maps/search/lat,+lon` (the common "dropped pin"
  share), `geo:lat,lon`, `google.<cc>` domains, `maps.app.goo.gl` / `goo.gl` redirects, consent
  interstitial. Not supported (422): plus codes, place-name-only links without a pin, directions
  without coordinates. DMS place names (`16°47'32.3"N 100°15'42.1"E`) are an optional later
  fallback — in practice such links also carry `!3d!4d`.
- Errors: 400 `bad url` / `url too long` / `host not allowed` / `redirect outside google maps`
  / `too many redirects`; 422 `{"error":"no coordinates in url","final_url":"…"}`; 502
  `{"error":"resolve failed"}` (timeout, DNS, TLS); the `final_url` is included on 422 so the
  operator can see what the short link became. Never log or return the request body.

### Reference implementation (`admin-api/maps.go`)

```go
package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	errBadURL     = errors.New("bad url")
	errNotAllowed = errors.New("host not allowed")
	errOffsite    = errors.New("redirect outside google maps")
	errNoCoords   = errors.New("no coordinates in url")
)

const mapsMaxHops = 5

var (
	reGoogleTLD = regexp.MustCompile(`^(?:www\.|maps\.)?google\.(?:com|[a-z]{2}|co\.[a-z]{2}|com\.[a-z]{2})$`)
	reAt        = regexp.MustCompile(`/@(-?\d{1,2}(?:\.\d+)?),(-?\d{1,3}(?:\.\d+)?)(?:,|/|$)`)
	rePin       = regexp.MustCompile(`!3d(-?\d{1,2}(?:\.\d+)?)!4d(-?\d{1,3}(?:\.\d+)?)`)
	rePath      = regexp.MustCompile(`/maps/(?:place|search)/(-?\d{1,2}(?:\.\d+)?),\+?(-?\d{1,3}(?:\.\d+)?)(?:/|$)`)
	rePair      = regexp.MustCompile(`^(?:loc:)?\s*(-?\d{1,2}(?:\.\d+)?)\s*,\s*\+?(-?\d{1,3}(?:\.\d+)?)\s*$`)
)

func isGoogleMapsHost(h string) bool {
	h = strings.ToLower(strings.TrimSuffix(h, "."))
	return h == "maps.app.goo.gl" || h == "goo.gl" || reGoogleTLD.MatchString(h)
}

func validLatLon(lat, lon float64) bool {
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 && !(lat == 0 && lon == 0)
}

func pair(a, b string) (float64, float64, bool) {
	lat, e1 := strconv.ParseFloat(a, 64)
	lon, e2 := strconv.ParseFloat(b, 64)
	if e1 != nil || e2 != nil || !validLatLon(lat, lon) {
		return 0, 0, false
	}
	return lat, lon, true
}

// parseMapsCoords extracts the pin from a Google Maps URL: the place pin
// (!3d!4d) over an explicit query (q=, query=, ll=, center=, destination=)
// over coordinates in the path (/place/lat,lon) over the viewport centre
// (/@lat,lon). src names the shape that matched.
func parseMapsCoords(u *url.URL) (lat, lon float64, src string, ok bool) {
	if u.Scheme == "geo" {
		if m := rePair.FindStringSubmatch(strings.SplitN(u.Opaque, "?", 2)[0]); m != nil {
			if lat, lon, ok = pair(m[1], m[2]); ok {
				return lat, lon, "geo", true
			}
		}
		return 0, 0, "", false
	}
	if m := rePin.FindStringSubmatch(u.Path); m != nil {
		if lat, lon, ok = pair(m[1], m[2]); ok {
			return lat, lon, "pin", true
		}
	}
	q := u.Query()
	for _, k := range []string{"q", "query", "ll", "center", "destination"} {
		if m := rePair.FindStringSubmatch(q.Get(k)); m != nil {
			if lat, lon, ok = pair(m[1], m[2]); ok {
				return lat, lon, "query", true
			}
		}
	}
	if m := rePath.FindStringSubmatch(u.Path); m != nil {
		if lat, lon, ok = pair(m[1], m[2]); ok {
			return lat, lon, "path", true
		}
	}
	if m := reAt.FindStringSubmatch(u.Path); m != nil {
		if lat, lon, ok = pair(m[1], m[2]); ok {
			return lat, lon, "at", true
		}
	}
	return 0, 0, "", false
}

// mapsResolver follows a share link to the URL that carries coordinates.
// allowURL gates the first request and every redirect hop.
type mapsResolver struct {
	client   *http.Client
	allowURL func(*url.URL) bool
	sem      chan struct{}
}

func (r *mapsResolver) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= mapsMaxHops {
		return errors.New("too many redirects")
	}
	if r.allowURL(req.URL) {
		return nil
	}
	if strings.EqualFold(req.URL.Hostname(), "consent.google.com") {
		return http.ErrUseLastResponse // keep the 3xx; resolve() reads its continue= parameter
	}
	return errOffsite
}

func isPublicIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1]&0xc0 == 64 { // 100.64.0.0/10
		return false
	}
	return true
}

func newMapsResolver() *mapsResolver {
	r := &mapsResolver{sem: make(chan struct{}, 4),
		allowURL: func(u *url.URL) bool { return u.Scheme == "https" && isGoogleMapsHost(u.Hostname()) }}
	d := &net.Dialer{Timeout: 3 * time.Second}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSHandshakeTimeout: 3 * time.Second,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isPublicIP(ip.IP) {
					return d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
				}
			}
			return nil, errors.New("no public address for " + host)
		}}
	r.client = &http.Client{Transport: tr, Timeout: 6 * time.Second, CheckRedirect: r.checkRedirect}
	return r
}

// resolve returns the pin in raw, following redirects when raw itself has
// none. final is the URL the coordinates were read from.
func (r *mapsResolver) resolve(ctx context.Context, raw string) (lat, lon float64, src, final string, err error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "geo") {
		return 0, 0, "", "", errBadURL
	}
	if lat, lon, src, ok := parseMapsCoords(u); ok {
		return lat, lon, src, u.String(), nil // no network needed
	}
	if u.Scheme == "geo" {
		return 0, 0, "", "", errNoCoords // nothing to follow for a geo: URI
	}
	if !r.allowURL(u) {
		return 0, 0, "", "", errNotAllowed
	}
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return 0, 0, "", "", ctx.Err()
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	req.Header.Set("User-Agent", "esp32-exit-node-admin/1.0 (+maps link resolver)")
	resp, err := r.client.Do(req)
	if err != nil {
		if errors.Is(err, errOffsite) {
			return 0, 0, "", "", errOffsite
		}
		return 0, 0, "", "", err
	}
	resp.Body.Close() // headers are all we need; the body is never read
	fu := resp.Request.URL
	if resp.StatusCode/100 == 3 { // stopped on the consent interstitial
		loc, err := fu.Parse(resp.Header.Get("Location"))
		if err != nil {
			return 0, 0, "", "", errOffsite
		}
		cont, err := url.Parse(loc.Query().Get("continue"))
		if err != nil || !r.allowURL(cont) {
			return 0, 0, "", "", errOffsite
		}
		fu = cont
	}
	if lat, lon, src, ok := parseMapsCoords(fu); ok {
		return lat, lon, src, fu.String(), nil
	}
	return 0, 0, "", fu.String(), errNoCoords
}

func mapsLink(lat, lon float64) string {
	return "https://www.google.com/maps?q=" + strconv.FormatFloat(lat, 'f', 6, 64) + "," + strconv.FormatFloat(lon, 'f', 6, 64)
}
```

Handler (`admin-api/geo.go` or `maps.go`), wired as
`mux.HandleFunc("/api/geo/resolve", s.requireAdmin(s.handleGeoResolve))` in `main.go` and in
`newTestServer`:

```go
// POST /api/geo/resolve {"url": "<google maps share link>"}
func (s *server) handleGeoResolve(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if r.Method != http.MethodPost || json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if len(in.URL) > 2048 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "url too long"})
		return
	}
	in.URL = strings.TrimSpace(in.URL)
	if strings.HasPrefix(in.URL, "http://") { // a pasted http link is followed over TLS only
		in.URL = "https://" + in.URL[len("http://"):]
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	lat, lon, src, final, err := s.maps.resolve(ctx, in.URL)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"lat": lat, "lon": lon, "source": src, "final_url": final, "maps_url": mapsLink(lat, lon)})
	case errors.Is(err, errNoCoords):
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error(), "final_url": final})
	case errors.Is(err, errBadURL), errors.Is(err, errNotAllowed), errors.Is(err, errOffsite):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	default:
		log.Printf("geo resolve: %v", err) // never the URL body; the link may embed a place name
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "resolve failed"})
	}
}
```

### Tests (`admin-api/maps_test.go`)

```go
func TestParseMapsCoords(t *testing.T) {
	cases := []struct {
		name, in string
		lat, lon float64
		src      string
		ok       bool
	}{
		{"viewport @", "https://www.google.com/maps/@16.7923,100.2617,17z", 16.7923, 100.2617, "at", true},
		{"street view @", "https://www.google.com/maps/@16.7923,100.2617,3a,75y,90t/data=!3m6!1e1", 16.7923, 100.2617, "at", true},
		{"place pin beats viewport", "https://www.google.com/maps/place/Wat+X/@16.79,100.26,17z/data=!3m1!4b1!4m6!3m5!1s0x1:0x2!8m2!3d16.7923!4d100.2617!16s%2Fg%2F1", 16.7923, 100.2617, "pin", true},
		{"dropped pin DMS name with pin", "https://www.google.com/maps/place/16%C2%B047'32.3%22N+100%C2%B015'42.1%22E/@16.7923,100.2617,17z/data=!4m4!3m3!8m2!3d16.7923!4d100.2617", 16.7923, 100.2617, "pin", true},
		{"q=", "https://maps.google.com/?q=16.7923,100.2617", 16.7923, 100.2617, "query", true},
		{"q=loc:", "https://maps.google.com/?q=loc:16.7923,100.2617&z=15", 16.7923, 100.2617, "query", true},
		{"maps urls api query=", "https://www.google.com/maps/search/?api=1&query=16.7923%2C100.2617", 16.7923, 100.2617, "query", true},
		{"ll=", "https://maps.google.com/maps?ll=16.7923,100.2617&z=16&t=m", 16.7923, 100.2617, "query", true},
		{"search path with plus", "https://www.google.com/maps/search/16.7923,+100.2617?entry=tts", 16.7923, 100.2617, "path", true},
		{"place path", "https://www.google.com/maps/place/16.7923,100.2617", 16.7923, 100.2617, "path", true},
		{"southern/western hemisphere", "https://www.google.com/maps/@-33.8688,-70.6693,12z", -33.8688, -70.6693, "at", true},
		{"geo uri", "geo:16.7923,100.2617?z=17", 16.7923, 100.2617, "geo", true},
		{"co.th domain", "https://www.google.co.th/maps/place/Bangkok/@13.7563,100.5018,15z", 13.7563, 100.5018, "at", true},
		{"short link has nothing to parse", "https://maps.app.goo.gl/Wi3Rzy4X2tRmbQW77", 0, 0, "", false},
		{"out of range", "https://www.google.com/maps/@95.0,200.0,12z", 0, 0, "", false},
		{"null island", "https://maps.google.com/?q=0,0", 0, 0, "", false},
		{"directions without pin", "https://www.google.com/maps/dir/Bangkok/Chiang+Mai", 0, 0, "", false},
		{"plus code", "https://www.google.com/maps/place/7P52QRP6%2B3J", 0, 0, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, err := url.Parse(c.in)
			if err != nil {
				t.Fatal(err)
			}
			lat, lon, src, ok := parseMapsCoords(u)
			if ok != c.ok || src != c.src || math.Abs(lat-c.lat) > 1e-6 || math.Abs(lon-c.lon) > 1e-6 {
				t.Fatalf("got (%v,%v,%q,%v) want (%v,%v,%q,%v)", lat, lon, src, ok, c.lat, c.lon, c.src, c.ok)
			}
		})
	}
}

func TestIsGoogleMapsHost(t *testing.T) {
	for h, want := range map[string]bool{
		"maps.app.goo.gl": true, "goo.gl": true, "www.google.com": true, "google.com": true,
		"maps.google.com": true, "www.google.co.th": true, "google.co.uk": true, "google.com.au": true,
		"GOOGLE.COM.": true,
		"evil.com": false, "google.com.evil.com": false, "notgoogle.com": false, "consent.google.com": false,
		"www.google.evil": false, "maps.app.goo.gl.evil.com": false, "": false,
	} {
		if got := isGoogleMapsHost(h); got != want {
			t.Errorf("%q: got %v want %v", h, got, want)
		}
	}
}

// TestResolveMapsFollowsRedirects runs the resolver against a local server
// standing in for maps.app.goo.gl + google.com; allowURL admits that host only.
func TestResolveMapsFollowsRedirects(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Wi3Rzy4X2tRmbQW77":
			http.Redirect(w, r, "/maps/place/Pin/@16.79,100.26,17z/data=!4m5!3m4!8m2!3d16.7923!4d100.2617", http.StatusFound)
		case "/hop":
			http.Redirect(w, r, "/Wi3Rzy4X2tRmbQW77", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/offsite":
			http.Redirect(w, r, "https://evil.example/maps/@1,2,3z", http.StatusFound)
		case "/consent":
			http.Redirect(w, r, "https://consent.google.com/m?continue="+url.QueryEscape(srv0(r)+"/maps/@16.7923,100.2617,17z"), http.StatusFound)
		case "/nopin":
			http.Redirect(w, r, "/maps/place/Somewhere+Nice", http.StatusFound)
		default:
			// the final Maps page: headers only, then block. A resolver that
			// reads the body hangs here until the client timeout and fails.
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-release
		}
	}))
	// LIFO: close(release) runs first so the blocked handlers finish, then
	// srv.Close() (which waits for them) can return.
	defer srv.Close()
	defer close(release)
	host := srv.Listener.Addr().String()
	r := &mapsResolver{sem: make(chan struct{}, 4), allowURL: func(u *url.URL) bool { return u.Host == host }}
	r.client = &http.Client{Timeout: 2 * time.Second, CheckRedirect: r.checkRedirect,
		Transport: &http.Transport{DisableKeepAlives: true}}
	ctx := context.Background()

	cases := []struct {
		name, path string
		lat, lon   float64
		err        error
	}{
		{"short link", "/Wi3Rzy4X2tRmbQW77", 16.7923, 100.2617, nil},
		{"two hops", "/hop", 16.7923, 100.2617, nil},
		{"consent interstitial", "/consent", 16.7923, 100.2617, nil},
		{"page without pin", "/nopin", 0, 0, errNoCoords},
		{"offsite redirect", "/offsite", 0, 0, errOffsite},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			lat, lon, _, _, err := r.resolve(ctx, srv.URL+c.path)
			if !errors.Is(err, c.err) {
				t.Fatalf("err = %v, want %v", err, c.err)
			}
			if err == nil && (math.Abs(lat-c.lat) > 1e-6 || math.Abs(lon-c.lon) > 1e-6) {
				t.Fatalf("got %v,%v want %v,%v", lat, lon, c.lat, c.lon)
			}
		})
	}
	t.Run("redirect loop stops at mapsMaxHops", func(t *testing.T) {
		if _, _, _, _, err := r.resolve(ctx, srv.URL+"/loop"); err == nil || !strings.Contains(err.Error(), "too many redirects") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("host outside the allowlist is refused before any request", func(t *testing.T) {
		if _, _, _, _, err := r.resolve(ctx, "https://evil.example/maps/x"); !errors.Is(err, errNotAllowed) {
			t.Fatalf("err = %v", err)
		}
	})
}

// srv0 is the scheme+host the test server sees itself as, for building the
// consent continue= target.
func srv0(r *http.Request) string { return "http://" + r.Host }
```

Note for the httptest test: `httptest.NewServer` is plain HTTP, so the test's `allowURL`
checks the host only; production's `allowURL` also requires `https`. The 1 MB-body variant
(write a large body instead of blocking) is weaker — the blocking handler is the one that proves
the body is never read. A fourth test (`TestDevicePutMapsURL`) reuses `newTestServerWithBroker`
and a local redirect server pointed at by `s.maps` to check that `PUT /api/devices/{mac}
{"maps_url": …}` stores `maps_url`, sets `lat`/`lon`, and that a 422 leaves the record and the
fake broker's call log untouched.

---

## 5. Health gauge and status pill

### What the percentage means

| Option | Formula from existing data | Verdict |
|---|---|---|
| Seats: `active_users / max_users` | `D:86-87`, `D:93-102` | Occupancy, not health — it is a sales number and already shown as `n/m`; keep it as its own bar. |
| Capacity utilisation: live Mbps / `capacity_mbps` | needs `throughput` from `metrics_node` bytes per bucket ÷ step | Good secondary signal once §2 exists ("hot board"); unmeasured boards have no denominator. |
| Free heap / RSSI | not sent (`rproxy.c:736-742` logs only) | Needs a firmware + broker change (`heap=`/`rssi=` tokens on HELLO give a boot-time value only; a periodic STATS frame on sid 0 gives live values). Phase E / firmware backlog. |
| Availability (uptime SLA) | `AVG(online_frac)` over 24 h from `metrics_node` | The number a proxy seller actually promises; needs §2. |
| Dial success | `dials_ok / (dials_ok + dials_err)` over 24 h (fallback: lifetime counters on `/nodes`) | Catches a board that is "online" but failing (WiFi flapping, DNS at home broken). |
| Stream headroom | `1 - active_dials / max_streams` | Needs the additive broker field; cheap. |

Recommendation: `health.score` (0–100) computed in `buildDeviceViews`, with the components
exposed so the tooltip can explain it:

```
avail    = mean(online_frac) over the last 24 h          (nil until the first sample; fallback online?1:0)
success  = dials_ok / (dials_ok + dials_err), 24 h        (nil when < 20 dials in the window)
headroom = 1 - active_dials / max_streams                 (nil without max_streams)
score    = 100 × (0.6·avail + 0.3·success + 0.1·headroom), weights renormalised over non-nil components
caps:      blocked → 0;  status offline/updating → min(score, 30)
gauge:     segments = ceil(score / 10);  ≥ 80 green, 50–79 amber, < 50 red
```

`deviceView.health = {"score": 87, "availability_24h": 0.98, "dial_success_24h": 0.995,
"stream_load": 0.3, "reasons": ["dial errors 0.5% (24h)"]}`. Before the sampler ships, the
same field is filled from instantaneous data only (score 100/0 from `online`, lifetime dial
ratio), so the gauge can be built first and gets smarter when §2 lands.

### Status pill — exact derivation (server-side `deviceView.status`)

```go
// deviceStatus: blocked > updating > offline > rolled_back > unmeasured > online.
func deviceStatus(v deviceView, now time.Time) (status string, tags []string) {
	otaAt, _ := time.Parse(time.RFC3339, v.OTAAt)
	switch {
	case v.Blocked:
		return "blocked", nil
	case v.OTAStatus == "sent" && now.Sub(otaAt) <= 15*time.Minute:
		return "updating", nil // firmware 2.4.22+ drops the link to download (registry.go:134-139)
	case !v.Online:
		if v.OTAStatus == "sent" {
			tags = append(tags, "ota_stalled") // sent > 15 min ago and never came back
		}
		return "offline", tags
	case v.OTAStatus == "failed":
		return "rolled_back", nil // sticky until the next /ota queues a new image (registry.go:197-214)
	case v.Kind != "dedicated" && v.CapacityMbps <= 0:
		return "unmeasured", nil // admission refuses bindings (admission.go:126)
	}
	if v.OTAStatus == "queued" {
		tags = append(tags, "ota_queued")
	}
	if v.SlotMismatch {
		tags = append(tags, "slot_mismatch")
	}
	return "online", tags
}
```

`PublicUser.status` gets the matching derivation from `Authorize`'s checks (`S:321-340`):
`disabled` > `expired` > `over_quota` > `active`, so the overview counts, the users table and
the "due date" column agree on one rule.

---

## 6. Alerts

Alerts are **derived state**, recomputed each poller tick from the cached `/nodes` + `/status`
snapshot and the users/devices tables — no table in v1. Transitions (raise/clear) write
`alert.raised`/`alert.cleared` events, so the feed shows them and `since` survives restarts (the
`since` of an active alert is the `ts` of its last `alert.raised` event; at startup the active set
is seeded from the last `alert.*` event per key).

| Key | Condition | Severity | Env |
|---|---|---|---|
| `node.offline:<mac>` | `status == offline` for > N min (from the last `node.offline` event) | critical if the board has seat-taking users, else warn | `ALERT_OFFLINE_MIN=10` |
| `node.ota_failed:<mac>` | `ota_status == failed` | warn | |
| `node.ota_stalled:<mac>` | `ota_status == sent` and `ota_at` > 15 min ago | warn | |
| `node.ip_changed:<mac>` | `node.ip_changed` event in the last 24 h (IP group — the sellable unit — moved) | warn | |
| `device.slot_mismatch:<mac>` | `slot_mismatch` | warn | |
| `device.unmeasured:<mac>` | online shared board with `capacity_mbps == 0` | info | |
| `user.over_quota:<name>` | enabled, `quota_gb > 0`, `usage_bytes ≥ quota` | warn | |
| `user.expiring:<name>` | enabled, `0 < expires_at − now ≤ D days` | info | `ALERT_EXPIRING_DAYS=3` |
| `user.expired_enabled:<name>` | enabled but expired (cleanup / renewal candidate) | info | |
| `system.broker_unreachable` | ≥ 2 consecutive failed polls | critical | |
| `system.slot_dials_refused` | `/status.dials_pinned_offline` grew in the last tick (a slot customer was refused) | warn | |

```
GET /api/alerts → 200 {
  "as_of": "2026-10-21T10:12:00Z",
  "counts": {"critical": 1, "warn": 2, "info": 3},
  "alerts": [
    {"key": "node.offline:aa:bb:cc:dd:ee:ff", "kind": "node.offline", "severity": "critical",
     "subject_type": "node", "subject_id": "aa:bb:cc:dd:ee:ff", "since": "2026-10-21T09:49:00Z",
     "message": "Living room offline 23 min (2 bound users)", "data": {"minutes": 23, "users": ["earth", "testuser"]}}
  ]
}
```
Sorted by severity then `since`; `counts` feeds the alarm tiles; `?severity=` filters. The
overview gets `alerts: {"critical": n, "warn": n}` so the home page needs no second call.

Notifications (later): Phase E in the spec is email via Resend (§6.2.10). Add a generic
`ALERT_WEBHOOK_URL` (JSON POST; Discord/Telegram/LINE Notify all take one) in the same step —
send on raise and on clear only, with a per-key cooldown and quiet hours. That is the point at
which a small `alert_state(key, raised_at, notified_at, acked_until, acked_by)` table appears
(acknowledge/mute from the dashboard needs it); until then nothing is stored.

---

## 7. Implementation backlog (ordered, backward compatible)

| # | Item | Size | Unlocks |
|---|---|---|---|
| 1 | **Google Maps resolver**: `maps.go` + tests, `POST /api/geo/resolve`, `maps_url` on `PUT /api/devices/{mac}` + migration v3 + `deviceView.maps_url`; `geo.country_code` from ip-api. | S | Operator's explicit ask: paste a share link, clickable 🇹🇭 City → Google Maps. |
| 2 | **Derived status**: `deviceView.status` + `tags`, `PublicUser.status`, `deviceView.next_expiry`, `brokerNode.mux_links`; `/api/overview` adds `users.connected_now` (observer `currentConns`/recent clients) and `broker.throughput_bps` (two in-memory `/status` samples). | S | Status pills, due-date column, "active now" and throughput KPI cards (no history yet). |
| 3 | **Poller + metrics tables** (migration v4): 60 s poll, 5 m flush, observer byte accumulator, hourly rollup, retention, counter-reset handling, cached snapshot. | M | Everything time-based. |
| 4 | **`GET /api/metrics`** (fleet/node/user series + `nodes`/`users` totals, `summary` with last/prev/delta_pct). | M | KPI sparklines, "+10% vs yesterday" pills, 30-day area chart with range, top devices/users lists. |
| 5 | **Events** (migration v5): `emit()`, emitters in admin handlers + webhook + passthrough actions, poller transitions with last-event dedupe, `GET /api/events` keyset pagination, retention. | M | Recent Activity feed; audit trail for reveal/forget/block. |
| 6 | **`deviceView.health`** composite from `metrics_node` (24 h availability, dial success) with instantaneous fallback; optional broker `max_streams` for headroom. | S | 10-segment gauge with a percentage and an explainable tooltip. |
| 7 | **`GET /api/alerts`** evaluator in the poller tick, `alert.raised/cleared` events, `counts` on overview. | M | Alarm tiles with counts; offline/OTA/quota/expiry badges. |
| 8 | **Per-user live rate**: EWMA of observer bytes per client → `PublicUser.live_bps`; `metrics_user` sparkline on the users table. | S | Spec Phase E "live Mbps per user", users table as the "orders" analogue. |
| 9 | **Broker additive fields**: `max_streams`, `link_since`, per-node `dials_pinned_offline`; admin-api consumes when present. | S (broker) | Exact online timestamps in the feed, stream headroom in health, "slot customers refused" per board. |
| 10 | **Notifications + acks** (`ALERT_WEBHOOK_URL`, Resend email per spec §6.2.10, `alert_state` table); firmware `heap`/`rssi` reporting for the health gauge. | L | Phase E; the only items that need new secrets or a firmware release. |

Items 1–2 ship without a sampler and give the services table its pills, flags, links and due
dates immediately; 3–4 are one PR; 5–7 can land in any order after 3. Every item adds fields or
endpoints only — the current `index.html` (`/api/overview`, `/api/users`, `/api/devices`,
`PUT /api/devices/{mac}` with numeric `lat`/`lon`) keeps working at each step, and the OSM link
can be swapped for the Google link in the same dashboard change that adds the paste field.
