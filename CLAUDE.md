# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

Read [AGENTS.md](AGENTS.md) first. It covers the following, and none of it is repeated here:

- ESP-IDF 5.5.6 activation on Windows;
- firmware builds from the space-free `D:\espbuild` mirror;
- versioning and `tools/fw_release.py`;
- the Coolify manual deploy;
- the local bench rig and the C3 test board;
- the settled capacity and seat decisions.

Then read the [continuation handoff](docs/superpowers/plans/2026-10-22-claude-continuation-handoff.md).
The [Thai work summary](docs/dashboard-review/08-work-summary-and-next-steps.md) is for the owner.
[PRODUCT.md](PRODUCT.md), [DESIGN.md](DESIGN.md) and `docs/dashboard-review/` are the design source for UI work.

## Current state

Dashboard Phase 2, platform commerce and the six-language portal are implemented and deployed. Start from the handoff's remaining activation and acceptance work; do not restart the historical Phase 2 plan.

Values in reports (deployments, collectors, providers) are timestamped snapshots, so check current git and live state before acting. Preserve:

- per-board/MAC seats;
- existing users;
- firmware archives;
- the ongoing hardware evidence collection.

The owner sets provider secrets in Coolify and will say when they are ready. Local fixtures and screenshots do not prove real provider delivery, a production deploy or hardware acceptance. Each of those is a separate gate.

## Architecture

All server pieces deploy together as one Coolify stack from `deploy/docker-compose.yml`: gost, broker, admin-api and flasher.

**Customer traffic**, end to end:

1. **gost** accepts customer connections. Port 3128 is the shared pool. Ports 3131–3160 are per-board slots, where slot N is port 3130+N; `deploy/gost/gen-config.sh` generates one gost service per slot.
2. For every connection, gost calls admin-api's plugins in `admin-api/gost_plugins.go`: `/gost/auth`, `/gost/limiter` and `/gost/observer`.
3. gost forwards over SOCKS5 to the **broker** on `:1080`. Slot services authenticate as `slot-N` using `SLOT_SECRET`.
4. The broker (`broker/registry.go`) picks a board. It pins slots and can overflow to a shared sibling behind the same public IP.
5. It sends the connection as a stream inside that board's single **mux1 TLS link** (`:7000`). `broker/mux.go` defines the frame format, `hello.go` the HELLO negotiation, and the legacy one-connection-per-dial pool is still accepted.
6. The **firmware** (`firmware/components/rproxy/rproxy.c`) dials the target from the home WiFi.

The board always dials out to the broker, so no port-forwarding is needed at home.

**Control and telemetry:**

- Boards send HELLO (their limits) and a STATS frame every 30 s. STATS is a key=value line built in `health_send`, with fields from headers like `env_stats.h`.
- The broker keeps live-only state (`nodeStats`, `max_streams`, `link_since`, dial counters). This state is never persisted to the node file.
- The broker serves that state on its status listener (`:7001`, endpoints `/nodes` and `/status`). admin-api reads it via `BROKER_STATUS_URL` and pushes changes to `/nodes/{mac}/...`: block/allow, slot, pool flag and OTA.
- A new telemetry field crosses every layer, with a test at each:
  1. firmware, plus a host test in `tools/test_firmware_*.c`;
  2. broker `hello.go` and `registry.go`;
  3. admin-api `devices.go`;
  4. the dashboard Health cell.

**admin-api** is a single Go `main` package:

- SQLite store via modernc (no cgo), with migrations in `db.go`;
- seat admission (`admission.go`) and capacity measurement (`measure.go`);
- commerce: plans, orders, slips and Stripe (`commerce*.go`, `payments_*.go`);
- the customer identity flow (`portal_auth.go`) and marketing/SEO config (`marketing.go`);
- metrics, events and alerts.

It embeds two front ends, both vanilla JS/CSS with no build step.

- `admin-api/dashboard/index.html` is the operator console, a single file. `dashboard_test.go` checks the served HTML for marker strings, so renaming a marked function or id fails tests on purpose. Update the marker together with the code.
- `admin-api/portal/` is the customer portal.
  - `portal_embed.go` server-renders the public pages (home, plans, help) per locale into the `<!--PORTAL_PUBLIC_CONTENT-->` slot of `portal/index.html`. That includes canonical, hreflang and OG tags, the sitemap and robots, and the CSP.
  - The home landing is built in `portal_landing.go`.
  - `portal.js` runs the private account SPA.
  - Moving between public and private pages is a full document load. This keeps optional analytics SDKs off private pages.
  - The CSP is `style-src 'self'`: no inline `<style>` and no `style=` attributes; use classes in `portal.css`.

## Commands

Run Go from PowerShell. The module cache is already populated, so work offline:

```powershell
$env:GOTOOLCHAIN = 'local'; $env:GOPROXY = 'off'
Push-Location admin-api; go test ./...; go vet ./...; Pop-Location
Push-Location broker;    go test ./...; go vet ./...; Pop-Location
go test -run 'TestPortalLanding' ./...   # one test, from inside the module dir
go test -race ./...                      # when changing concurrent/backend behavior
```

A Go telemetry `upload.token Access denied` message is harmless; check the exit code. The broker's gost-config tests need `sh` on PATH (Git Bash).

Portal locale checks (key parity across th/en/zh-CN/ja/ru/vi, identical `{placeholders}`, plain-text values, routes):

```powershell
node --test tools/portal_i18n_test.cjs
```

Firmware host tests compile the firmware's pure headers with gcc:

```bash
gcc -std=c11 -Wall -Wextra -Werror tools/test_firmware_env.c -o output/test_firmware_env.exe && ./output/test_firmware_env.exe
# likewise test_firmware_link.c and test_firmware_ota.c
```

### Browser suites and fixture

Browser suites run a real admin-api binary on an isolated SQLite database, with a fake broker:

```powershell
Push-Location admin-api; go build -o ../output/playwright/admin-claude-fixture.exe .; Pop-Location
$env:DASHBOARD_FIXTURE_EXE = (Resolve-Path output/playwright/admin-claude-fixture.exe).Path
node tools/portal_marketing_smoke.cjs
node tools/dashboard_commerce_smoke.cjs
```

The two suites share `output/playwright/fixture.json`. Run them one at a time, and don't rebuild the exe while a fixture is running.

For manual viewing, `node tools/dashboard_fixture.cjs <exe>` prints a JSON line with the fixture's `url`. The admin token is `dashboard-fixture-local-only`, kept in the dashboard's `localStorage` key `adm_tok`. Add sample plans with `POST /api/plans`. The handoff's section 6 has the full regression list.

## Rules the owner or the tests enforce

- **Customer-facing copy:**
  - Never promise an exclusive or fixed IP, a guaranteed speed or an SLA. "Dedicated" means one customer on one board.
  - Avoid "residential", "unlimited", "bypass", "unblock" and "anonymous".
  - Russian calls the board "устройство".
  - Admin labels stay English; the portal is Thai-first.
- **Locales:** every key must exist in all six `admin-api/portal/locales/*.json` files.
- **Firmware releases:**
  - Don't hand-edit `flasher/firmware/releases.json`.
  - Keep `firmware/sdkconfig.prov` (provisioning secrets) uncommitted; it is git-ignored.
- **Outward actions:**
  - Pushing `main` does not deploy, and docs-only changes are not deployed at all.
  - Confirm with the owner before an OTA to production boards or before interrupting a hardware soak.
