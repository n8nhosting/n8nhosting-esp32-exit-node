# OTA confirmation and bounded connection diagnostics

Approved follow-up to the Phase 2 acceptance review: correct OTA tracking,
update the C3 canary, then test real proxy transfers, parallel connections and
reconnection. Preserve customer assignments and archived release images.

## OTA evidence and state

- `queued`: command remains durable until claimed and completely written.
- `sent`: complete command write; the board has not yet confirmed the target
  version. This alone does not prove downloading or flashing.
- `ok`: a later HELLO reports the requested firmware version.
- `failed`: delivery or subsequent unchanged firmware shows an unsuccessful
  update. Do not label this as a confirmed rollback.
- `timeout`: no target-version confirmation within 15 minutes. A late HELLO
  reporting the target version can still resolve this to `ok`.

Persist queued URL, source firmware, source uptime, request revision and
in-flight dispatch state. A restart in the write/completion gap is ambiguous;
do not automatically resend it. Confirmed partial/failed writes stay queued.
Claims must belong to the current node and newest mux session. Cancellation,
replacement requests and forgotten nodes invalidate old completions. Keep the
existing OTA wire protocol and 255-byte URL limit. No additional ticker.

Primary device status uses `ota_failed` for online failed/unconfirmed updates;
blocked/offline precedence remains. Tags retain failure/timeout details. An
unreachable broker is stale evidence, not proof all boards are offline.

## Diagnostics contract

Existing empty-body `POST /api/devices/<mac>/measure` still performs the normal
single download and stores capacity. Optional JSON accepts only:

```json
{"parallel":2,"duration_sec":30,"persist":false}
```

Parallelism is 1, 2 or 4. Duration is 0 for one download per worker, or 30–300
seconds for repeated downloads. The origin remains the configured fixed
`SPEEDTEST_URL`; clients cannot choose a destination or proxy. Each request is
capped at 8 MiB, with a 512 MiB total budget per worker. Use one temporary,
uncapped, seatless account bound to the
specific board, then clean it up on success, failure or cancellation.

Return aggregate bytes/rate and per-worker results, with actual elapsed time.
A stream with fewer than 64 KiB is not a valid speed measurement. Distinguish
the intended duration deadline from client cancellation or a transport failure.
`persist:false` must not change capacity, seats or customer assignments.

The dashboard exposes this as **Connection test** in the device actions menu,
with 30/60/90-second options, elapsed time, Stop and readable per-stream results.
Longer internal checks avoid public reverse-proxy request deadlines; the HTTP
handler itself remains bounded at 300 seconds and stops on client cancellation.
Starting or closing another test must invalidate late responses. Existing
modal focus handling and refresh pausing apply.

Only a timed worker that already finished an object may briefly retry a typed
local proxy CONNECT 503 while the previous tunnel releases its stream slot.
Use 100/200/400 ms context-aware waits, at most three retries per object and
eight per worker, with a one-second eligibility window. Include waiting in
elapsed time and rate; return `retries` and `connect_503s` per worker and show
nonzero retry counts in the existing results. Initial CONNECT failures,
origin errors, body/read failures and unrelated network errors remain terminal.
Exhaustion or a deadline while retrying an unresolved CONNECT failure is an
incomplete result, never a successful duration sample. Keep the original
shared deadline, byte budget and stream/admission limits.

## Verification gates

Keep local unit/race/browser checks, actual production deployment, actual board
OTA confirmation and hardware load duration as separate evidence. A five-minute
diagnostic is not a 24-hour soak. Keep testing releases marked testing and leave
the stable/latest pointer unchanged until hardware acceptance is complete.

Firmware 2.4.24 adds a 100 ms yield only when the console returns NULL/EOF.
In ESP-IDF 5.5.2, disconnected USB Serial/JTAG reads can return immediately;
the previous main console loop retried without blocking and consumed idle CPU.
Keep the existing CPU counter formula so before/after hardware readings can
confirm this fix. Valid console commands and network configuration are unchanged.

Record current results in `docs/dashboard-review/04-phase2-acceptance.md` and
sanitized artifacts under `output/playwright/`; never store credentials.

## Live payload accounting

Broker-wide and per-node cumulative byte counters advance while connections
remain open, for both mux and legacy relays. Count only the payload bytes an
actual downstream write accepted, including a partial write that also returns
an error. Exclude SOCKS replies and mux frame headers. Closing a connection
must not add those bytes a second time; legacy bytes already buffered with a
DIAL reply use the same counted path as subsequent payload.

The existing metrics sampler, reset rule and API fields remain unchanged.
`throughput_bps` remains bytes per second, converted to displayed Mbps by
multiplying by 8 / 1,000,000. Rates use sampler intervals rather than a new
ticker. Verify node counters during an open transfer and the next sampled
Overview rate on actual production hardware after deployment.
