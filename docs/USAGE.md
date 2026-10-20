# Usage accounting & GOST observer calibration

Monthly per-user traffic (for quota enforcement) comes from the GOST **observer**
plugin, which POSTs periodic `stats` events to `admin-api` `/gost/observer` with
`inputBytes` / `outputBytes` per client (the authenticated username).

GOST versions differ in whether those byte counters are **per-period deltas** or
**cumulative per connection**, so `admin-api` has a `USAGE_MODE`:

- `USAGE_MODE=delta` (default) — adds each event's `inputBytes+outputBytes`
  as-is. Correct when GOST reports per-period deltas.
- `USAGE_MODE=cumulative` — adds only the increase over the last value seen per
  `(service, client)`, resetting when the counter drops (new connection).

## How to calibrate (once, against your running GOST)

1. Create a test user with a small quota (e.g. `quota_gb: 1`).
2. Through the proxy, download a known amount (e.g. `curl -x ... http://.../100MB`).
3. `GET /api/users/<name>` and compare `usage_bytes` to the real bytes moved.
   - Roughly 1× the real bytes → the current mode is correct.
   - Roughly N× inflated or far too low → switch `USAGE_MODE` and retest.
4. Cross-check against `broker` `/status` `bytes_up + bytes_down` (global) and
   GOST's own stats.

Quotas reset on the first observer/auth event of each new calendar month
(`usage_month`). You can also `POST /api/users/<name>/reset-quota`.

If your GOST build's observer semantics don't match either mode cleanly, open
`admin-api/gost_plugins.go` (`handleObserver`) — the accounting is ~20 lines.
