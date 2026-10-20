# Security notes

## The ESP32 ⇄ VPS hop is plain TCP in this release
Customer **HTTPS** traffic stays end-to-end encrypted regardless. But the
control link and any **plain-HTTP** customer traffic between the VPS broker and
the ESP32 are **not encrypted** yet. Until TLS is added:

- Use a long, random `POOL_TOKEN` (alphanumeric, 32+ chars).
- Firewall the broker pool port (`7000`) to your home IP range while testing.
- Treat the platform as experimental; do not carry sensitive plaintext.

Planned upgrade: wrap the pool link in TLS (esp-tls / mbedTLS on the ESP32,
standard TLS on the broker). The firmware already has a `tls` config flag
(currently a no-op that warns).

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
