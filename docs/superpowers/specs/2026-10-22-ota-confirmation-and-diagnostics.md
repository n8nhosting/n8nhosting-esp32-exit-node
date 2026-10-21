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
capped at 8 MiB. Use one temporary, uncapped, seatless account bound to the
specific board, then clean it up on success, failure or cancellation.

Return aggregate bytes/rate and per-worker results, with actual elapsed time.
A stream with fewer than 64 KiB is not a valid speed measurement. Distinguish
the intended duration deadline from client cancellation or a transport failure.
`persist:false` must not change capacity, seats or customer assignments.

The dashboard exposes this as **Connection test** in the device actions menu,
with bounded options, elapsed time, Stop and readable per-stream results.
Starting or closing another test must invalidate late responses. Existing
modal focus handling and refresh pausing apply.

## Verification gates

Keep local unit/race/browser checks, actual production deployment, actual board
OTA confirmation and hardware load duration as separate evidence. A five-minute
diagnostic is not a 24-hour soak. Keep testing releases marked testing and leave
the stable/latest pointer unchanged until hardware acceptance is complete.

Record current results in `docs/dashboard-review/04-phase2-acceptance.md` and
sanitized artifacts under `output/playwright/`; never store credentials.
