# Security notes

## The ESP32 ⇄ VPS hop is TLS with a pinned private CA
The pool link is TLS 1.2+. The firmware embeds **our own CA**
(`firmware/components/rproxy/broker_ca.pem`) and accepts only a broker
certificate signed by it (hostname check skipped on purpose — the CA issues
exactly one server cert, so CA trust alone authenticates the broker). The
broker gets its cert/key via `TLS_CERT_B64`/`TLS_KEY_B64`.

Key handling:
- `ca.key` (CA private key) is **not** in the repo and must be kept offline —
  it is only needed to issue a new broker cert (valid 5 years).
- `broker.key` lives only in Coolify's environment variables.
- Rotating the CA requires a firmware rebuild + OTA to every device.

Each device also presents its **MAC** in the handshake; the broker registry lets
you block a device even though all devices share the baked-in `POOL_TOKEN`.

## Dashboard exposure
`admin-api` serves the dashboard and can be exposed on a domain. The GOST plugin
callbacks live under `/gost/<GOST_PLUGIN_SECRET>/…` so they are not callable by
anyone who finds the host. Admin REST needs `ADMIN_TOKEN`. Passwords are stored
as PBKDF2-SHA256 (100k iterations) and never returned by the API; a generated
password is shown exactly once at creation.

## SSRF / LAN protection (implemented)
The firmware (and broker tests) refuse, **after DNS resolution**, any target in
loopback / private / link-local / CGNAT / multicast ranges, so a customer cannot
use the proxy to reach `192.168.x.x` or `127.0.0.1` on the home network. Ports
25 / 465 / 587 are refused to limit spam abuse. Review `ipv4_is_blocked()` in
`firmware/components/rproxy/rproxy.c` and `blockedIPtest()` in
`broker/broker_test.go`.

## Authentication & abuse
- Every customer connection is authenticated by the GOST auther plugin against
  the admin-api; disabled / expired / over-quota users are denied.
- GOST logs each connection (user, destination, bytes, duration) as JSON — keep
  these logs (see `docs/LEGAL.md` on retention).
- Keep `admin-api` plugin endpoints (`/gost/*`) off the public internet — in the
  provided compose they are internal-only.

## Secrets
`API_PASS`, `POOL_TOKEN`, `ADMIN_TOKEN`, `PAYMENT_WEBHOOK_SECRET` should be
alphanumeric and random. The GOST config renderer uses `awk` substitution; avoid
shell metacharacters in `API_PASS`.
