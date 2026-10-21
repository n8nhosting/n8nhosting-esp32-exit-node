# Platform completion — 22 October 2026

This extends Dashboard Phase 2 with the seven follow-ups approved by the operator:
bounded firmware OTA, per-user live/history UI, RSSI, commerce, Stripe/manual
payment, customer identity/portal, and durable lifecycle/operational email.
The older Phase 2 acceptance report remains historical evidence. Per-board/MAC
seats continue to apply even when boards share a public IP.

## Delivered behavior

The admin console keeps its existing Devices, Users, connection sheet, Assign,
drawer, measurement and OTA workflows. Sales adds Plans, Orders, Subscriptions,
Approvals and Email log. Plan editors use integer satang on the server and
explicit THB input in the UI. Existing orders retain immutable plan/price
snapshots after a plan is edited or archived.

Published plans require a price. The researched catalog is added explicitly as
**inactive drafts**, never silently published: Lite 79 THB / 1 Mbps, Plus 139 THB /
5 Mbps, Dedicated Board 249 THB / measured capacity; each includes 30 + 3 days
and unmetered GB under its rate/board/stream limits. Dedicated means one board,
not exclusive public IP. See [pricing research](05-thai-pricing-research.html).

An order holds a measured, online, unblocked, correctly routed board seat for
15 minutes. Commerce reservations are included in legacy admission checks.
Renewal on an active subscription does not consume a second seat. Enabled,
unexpired users retain seats while over quota, so quota reset cannot sell the
same paid seat twice. Expiry releases a seat. Legacy manual users keep calendar
month usage; subscribers use purchased periods and queued prepaid renewals.
An independent durable calendar-month ledger keeps Overview traffic intact
across subscription-period changes and support quota resets. Managed users'
rate, quota and paid-through date are changed through subscription orders;
legacy direct extension/deletion cannot bypass the period ledger.

Payment handling records verified funds even if capacity is no longer available:
the order stays paid and waiting for capacity, with a receipt and admin retry.
Unpaid pending orders may be cancelled. Expired Stripe Checkout attempts require
a replacement order; old linked sessions remain eligible for valid late payment
callbacks. A redirect, slip upload or provider timeout is not payment proof.
Slip review requires an operator decision and recorded reason. Raw uploads are
private, tenant-scoped, MIME-sniffed, bounded to 5 MiB per file and 512 MiB total
by default. The filesystem path is not a static URL.

The Thai-first portal lives at `/portal/` on the admin-api host. It supports
email sign-in, published plans, own subscriptions/period usage/paid-through date,
renewal, orders, Checkout, bank instructions/slips, receipts and explicit
credential read/rotation. It exposes current connection host/port but no fleet
MAC/WiFi/admin token or other customer's resources. Credentials are cleared when
the page hides, changes section or authentication expires.

Magic links expire in 15 minutes and are single-use. Tokens and sessions are
hashed in SQLite. Cookies are Secure/HttpOnly/SameSite; mutations require CSRF
and the configured Origin. Turnstile is validated server-side with hostname and
action checks. AUP version/time are associated with the verified identity.
Email bodies are encrypted in a durable outbox, sent with provider idempotency
and bounded retries, and cleared after delivery/expiry. Provider logs omit
credentials, tokens and message bodies. No extra telemetry ticker was added.
Sign-in email has priority over notification backlog. Payment receipt,
activation and renewal use distinct durable keys so a paid order that waited
for capacity can later send its activation notice exactly once. Optional ops
email begins with new system/device transitions and excludes customer payloads.

Users show live Mbps and a keyboard-inspectable 24-hour traffic graph/table from
the existing metrics API. Missing samples remain gaps. Optional firmware RSSI
has unknown and stale states; old firmware stays compatible.

Firmware **2.4.25** is archived for both C3 and S3 as **testing only**. It adds
optional RSSI and a 180-second normal overall OTA budget, with an additional
10-second reboot fail-safe for a wedged blocking IDF call. Host tests validate
transfer/cleanup boundaries. Both ESP-IDF builds passed. Every older archive and
the stable/latest `2.4.21-mux1.2` selection are preserved.

## Configure before accepting money or sending email

Set these in Coolify's environment settings, never in chat, source control or
screenshots. Compose forwards the variables with empty fail-closed defaults.

| Purpose | Variables / setting |
|---|---|
| Portal origin | `PORTAL_BASE_URL=https://admin.jbacteam.com/portal/` or another configured HTTPS host on this service |
| Anti-abuse | `TURNSTILE_SITE_KEY`, `TURNSTILE_SECRET`; register the actual portal hostname |
| Email | `EMAIL_FROM`, `RESEND_API_KEY`; verified sending domain/account |
| Operational email | Optional `OPS_ALERT_EMAIL`; first enable starts from current events, then sends new allowlisted system/device transitions without customer payloads |
| Stripe | `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET`; start with test keys; enable card/PromptPay in Dashboard |
| Stripe webhook | HTTPS `/webhook/stripe`; `checkout.session.completed` and `checkout.session.async_payment_succeeded`; API `2026-09-30.endive` |
| Bank transfer | `PAYMENT_BANK_NAME`, `PAYMENT_ACCOUNT_NUMBER`, `PAYMENT_ACCOUNT_NAME` |
| Private storage | `/data/slips` in the existing admin volume; optional `SLIP_STORAGE_MAX_BYTES` |
| Proxy-aware rate limits | Optional `PORTAL_TRUSTED_PROXY_CIDRS` with explicitly trusted immediate proxy peers; otherwise ignore forwarded IP headers |

`GET /portal/api/config` reports configuration readiness, not evidence that a
provider account can transact. Missing configuration keeps public actions
disabled; no mock payment or email is enabled in production. After configuration,
verify a real test-mode email/login, Stripe test payment/webhook and private slip
review on the intended hostname before publishing plans. Real payment acceptance
and merchant/bank setup remain operator-owned activation steps.

The current Checkout contract follows Stripe's
[Endive payment-method migration](https://support.stripe.com/questions/fix-the-payment-method-types-error):
`allowed_payment_method_types`, with a pinned API version. A Checkout session
expires after at least 30 minutes while a seat hold is 15 minutes; late payments
always recheck capacity. Orders under 10 THB cannot use the current Stripe path.

## Verification and release evidence

Local implementation, simulated provider tests, browser fixtures, production
deployment and actual hardware observation are separate evidence categories.
Provider tests use a local substituted HTTP transport and simulated signed
webhooks; they do not prove a live merchant account or real email delivery.

Final command results, screenshots, source/deployment IDs and actual production
checks are recorded below when completed. A completed build is not a deployment.
A short hardware check is not a 24-hour soak. The prepared recorder and acceptance
instructions are in [HARDWARE_SOAK.md](../../tools/HARDWARE_SOAK.md); no release
is promoted merely because a recorder finishes.

### Local results

- Firmware host policy: 10 cases passed; C3 and S3 builds passed; archive tool
  tests passed (10). RSSI broker tests/vet/full race passed.
- Final integrated admin `go test ./...`: **273 cases / 144 top-level tests
  passed (13.513 s)**; `go vet ./...` passed; full `go test -race ./...`:
  **273 cases / 144 top-level tests passed (56.031 s)**. Tests include the final
  calendar ledger, financial guards, lifecycle mail and deterministic email
  priority clock correction. Independent review findings were fixed and
  regression-tested before freezing the code.
- Existing dashboard acceptance: 11 groups passed; device-settings follow-up:
  10 groups passed at desktop and mobile sizes. These use the real isolated
  admin API/SQLite with fake broker/geo data.
- A final settings run against the integrated binary caught an extra reservation
  fact row pushing the drawer's Users section below the 900px frame. Reservations
  now share the existing Board seats row; no information was removed. The final
  10-group settings run passes, including all ten sections inside the desktop
  frame, all 50 activity entries, mobile layout and both assignment pickers.
- Commerce/portal browser verification: **13 groups passed**, 16 screenshots,
  zero uncaught JavaScript errors and zero external provider requests. Admin
  plan CRUD, approvals/download, order retry/cancel, email log and user history
  use the real local Go API and synthetic SQLite records. Customer subscription,
  renewal, credentials, raw slip, receipt and 401 journeys explicitly intercept
  customer APIs. They do not prove live payment, login delivery or bank transfer.
- Desktop 1440x900 and mobile 375x812 show no page horizontal overflow. Keyboard
  chart/table inspection, delayed history response rejection, form/focus refresh
  preservation and managed-versus-legacy editor guards pass. Mobile quota bars
  render at 8px height with their actual fixture value. RSSI tests include unknown,
  exactly 120 seconds, stale and offline values; these are UI fixtures, not radio
  measurements.
- Selected [screenshots and machine-readable evidence](evidence-2026-10-22-platform/README.md)
  are committed alongside this report. The pricing HTML was rendered/read back
  at desktop and mobile widths, including source qualifications.
- Static Impeccable scan: no non-advisory findings; 88 advisory notes about
  documenting the inherited font/color/radius vocabulary. It is not a pixel or
  full WCAG certification. Screenshot/keyboard/overflow checks are recorded
  separately.
- Checked opaque text/background pairs exceed 4.5:1: primary button 4.99,
  muted panel text 6.43, link on panel 5.37, and success/warning/error labels
  6.12/7.63/6.79. These token calculations are not a full page accessibility audit.

Production preparation: an online SQLite backup was verified with
`PRAGMA integrity_check=ok`, schema v6, 4 users and 4 saved device records, at
`/root/exitnode-pre-v7-20261022-0538/platform.db` on the authorized server.
The private directory is mode 0700 and the database file 0600. No live database
or secret was copied into the repository.

### Production and activation state

Pending this release's final integration and deployment evidence. The prior
runtime was `184726f`, with three boards reporting testing firmware 2.4.24.
Do not infer that 2.4.25, commerce or public payments are live from the local
results above. A full 24-hour hardware result has not yet been collected.
