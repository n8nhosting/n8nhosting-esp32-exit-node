# Platform commerce and reliability completion

Approved by the operator on 2026-10-22: use subagents to implement the remaining
planned platform work. The operator chooses Stripe plus bank-transfer slips and
asks for Thai competitor research, comparable pricing and additional benefits.
The existing seller-platform design is the baseline; per-board/MAC seats from
2026-10-22 supersede every older IP-group admission rule.

## Boundaries and ownership

- Commerce worker owns Go schema, plans, orders, subscriptions, reservations,
  paid provisioning, approvals, period quota and main wiring.
- Identity/provider worker owns new Go portal auth, Stripe, slip upload,
  email/outbox and notification code, coordinating schema with commerce.
- Firmware worker owns broker/firmware RSSI, overall OTA deadline, firmware
  builds/archives and an unattended soak recorder.
- Integrator owns the single-file admin dashboard, separate customer portal,
  pricing research, integration, browser verification and final documentation.

Go + SQLite and the existing dashboard remain. Add migrations after v6; never
edit shipped migrations or overwrite archived firmware. Keep existing manual
users, assignments, credentials, metrics and APIs backward compatible.

## Commerce

Money is integer satang. Plan fields: id, name, days (total including bonus),
bonus_days, description, quota_gb, rate_mbps, kind, price_satang, active.
No unpriced active plan and no invented market claims. Product price is a cap
on access, not a throughput guarantee. Dedicated reserves a board, not a public
IP shared with other boards. ISP changes can change the exit IP.

Admin collections return objects: `{plans:[]}`, `{orders:[]}`,
`{subscriptions:[]}`, `{approvals:[]}`, `{customers:[]}`. Plans support create,
edit and archive; preserve order snapshots when a plan changes. Order amounts,
duration and quota are immutable snapshots. Receipt data excludes credentials.

Reserve one eligible online, unblocked, routed, positively measured board seat
for 15 minutes before payment. Match board kind and measured rate to the plan.
Count reservations in both commerce and legacy admin admission. Renewing an
active subscription on its board must not reserve a second seat. Concurrent
orders and provider retries must not oversell or provision twice.

Paid provisioning is idempotent by event and order. Recheck capacity for an
expired reservation; if unavailable, retain a paid waiting_capacity order and
show its status rather than silently routing elsewhere or losing the payment.
Stripe's minimum Checkout expiration differs from seat reservation duration;
the system must handle a payment after the seat hold expires.

Subscriptions own quota per purchased period. Legacy users retain calendar
month accounting. Prepaid renewal queues the next period without resetting the
current period early. Expired subscriptions release their seat. Never query
stores or perform network calls inside a SQLite single-connection transaction.

## Identity and providers

`GET /portal/api/config` exposes readiness booleans, public site key, AUP
version and portal URL, never secrets. Auth request accepts email, AUP version
and Turnstile token; magic links use a URL fragment, single-use hashed tokens
and expiry. Sessions use Secure/HttpOnly/SameSite cookies. Customer mutations
require CSRF and matching configured Origin. Customers can only read or mutate
their own orders, subscriptions, credentials, slips and receipts. No fleet MAC,
WiFi information, admin token or other customer's data appears in the portal.

Unconfigured email, Turnstile, bank details or Stripe disable the corresponding
public actions with actionable readiness messages. No production mock payment,
fake delivery or token returned in login responses. Existing admin access does
not become customer access. Rate-limit public auth requests.

Stripe uses server-created THB Checkout Sessions, signed raw-body webhooks with
timestamp validation, exact amount/currency/order checks, replay protection and
atomic fulfillment. A success redirect is never proof of payment. Slip uploads
are bounded JPG/PNG/PDF, authenticated and stored outside public static files;
an unavailable verifier leaves a manual-review status. Admin approval records
actor/reason, and duplicate clicks do not extend twice.

Email uses a durable outbox with unique lifecycle keys, claims, retries and
sanitized delivery logs. Send activation, renewal, expiry/quota reminders and
operational alerts only when configured. Hook housekeeping into the existing
sampler; avoid an unrelated second telemetry ticker.

## Interface

Keep the approved dark palette and Thai-capable system font. Admin gains one
Sales entry with Plans, Orders, Approvals, Subscriptions and Email log sections;
retain the compact mobile bottom navigation. Use inline editors and real empty,
loading, failure, stale and unconfigured states. Pause refresh during editing,
keep search and focus, and discard old responses on navigation.

Customer portal is Thai-first with English supporting labels at `/portal/`:
plan selection and explicit measured-speed constraints; email login; own
subscriptions and copy/rotate credentials; usage/expiry; renewal; orders,
checkout/slip upload and receipts. Keep payment state visible and recoverable.
No fabricated social proof, guaranteed static IP, unlimited concurrency or SLA.

Users gain live Mbps plus accessible per-user traffic history from the existing
metrics API. Optional valid RSSI appears with unknown/stale fallbacks.

## Firmware and verification

Overall OTA deadline defaults to 180 seconds, bounded 30–600. Normal timeout
aborts and reconnects; an additional 10-second fail-safe can reboot the current
valid boot image if a blocking IDF operation wedges. No concurrent TLS cleanup
or task deletion. RSSI is an optional STATS token and missing values stay unknown.
Version 2.4.25 is testing only; build both targets, preserve old archives/latest,
then record actual canary evidence separately from builds.

Meaningful tests cover migrations, concurrent reservation/admission, payment
replay/amount mismatch, renewal boundaries, customer isolation, CSRF, token reuse,
slip validation, outbox dedupe/retry and missing configuration. Run both Go
modules' tests/vet, relevant race tests, real browser tests at 1440x900 and
375x812, and Impeccable detector. Provider fixtures are labeled simulated.
External account setup and a completed 24-hour soak are independent gates.
Never claim live payment/email or 24-hour hardware acceptance from local tests.
