# Proxy Seller Platform — capability map

A prioritized feature map for running the ESP32 exit-node proxy as a **legitimate,
compliant** paid service. Ordered by phase; each item notes where it lives in this
repo today (✅ built), what is partial, and what is net-new.

Guiding principle: the operator's home/office IP is what gets traced, so the
platform's job is to make **who did what, when** auditable and to make abuse hard.
Every "growth" feature below is secondary to that.

---

## Phase 0 — Already built (this repo)

| Capability | Where |
|---|---|
| Per-customer proxy auth (HTTP + SOCKS5) | gost `auto` handler + admin-api `/gost/auth` ✅ |
| Per-user rate limit | admin-api `/gost/limiter` ✅ |
| Monthly quota (GB) + enforcement | admin-api observer + auth ✅ |
| User CRUD, enable/disable, expiry, extend | admin-api REST + dashboard ✅ |
| Device fleet by MAC (online, exit IP, traffic, block/allow) | broker registry + dashboard ✅ |
| OTA firmware push per device | broker + dashboard ✅ |
| Encrypted device↔VPS link (TLS, pinned CA) | firmware + broker ✅ |
| Per-connection logging (user, dst, bytes, time) | gost JSON logs ✅ |
| Payment webhook (activate/extend on paid) | admin-api `/webhook/payment` ✅ |

---

## Phase 1 — Minimum to take paying customers (do first)

1. **KYC / customer identity** *(net-new, legal-critical)*
   - Registration with verified email + phone (OTP). For higher tiers, ID
     verification via a Thai KYC provider.
   - Store: who the customer is, when they agreed to the AUP, which plan.
   - Why first: without identity you cannot answer an abuse complaint, and you
     carry 100% of the liability. See `docs/LEGAL.md`.

2. **Acceptable-Use Policy + consent gate** *(net-new)*
   - AUP shown and accepted at signup (timestamped). Prohibit: CSAM, fraud,
     intrusion, spam, anything illegal in Thailand or the destination.
   - Ties to the technical blocks already in firmware (private-IP, SMTP ports).

3. **Self-serve signup + plan purchase** *(partial — webhook exists)*
   - Public signup page → choose plan → pay (PromptPay QR via a Thai PSP such as
     Omise/GB Prime Pay/2C2P, or Stripe for cards) → webhook provisions the user.
   - Auto-generate proxy credentials; show once; let customer rotate.

4. **Customer portal** *(net-new)*
   - Separate from the admin dashboard: the customer sees only their own
     credentials, usage vs quota, expiry, invoices, and a "rotate password"
     button. Never the device fleet.

5. **Metering accuracy + invoices** *(partial)*
   - Calibrate `USAGE_MODE` against real traffic (`docs/USAGE.md`), then issue
     monthly invoices / receipts (tax invoice fields for Thailand).

6. **Audit log retention** *(partial — logs exist, retention not automated)*
   - Ship gost JSON logs off the container to durable storage, retain ~90 days
     (verify current Thai requirement), searchable by user + time for complaint
     response. Protect/encrypt; access-controlled.

---

## Phase 2 — Operate at scale without firefighting

7. **Abuse detection & automated enforcement**
   - Signals: spikes in dials to many hosts (scanning), known-bad destination
     lists, complaint volume. Action: auto rate-cut, suspend user, alert admin.
   - Destination allow/deny categories (optional): block high-risk categories.

8. **Device↔user binding & pinning** ✅ *built*
   - Customers are bound to a board, and the routing pin is built: each board has
     its own slot port (`3131`–`3160`), so a customer's traffic always exits
     through the chosen device.

9. **Fleet health & alerting**
   - Device offline > N min → alert. Low pool / high no-node rate → alert.
     Firmware version drift → bulk OTA. (Device page shows uptime/SSID/geo,
     and from firmware 2.4.23 a 30 s health sample: internal RAM free and
     low-water, CPU load, image/flash sizes — the inputs an alert rule needs;
     thresholds and history are still to do.)

   Dashboard phase 2 is implemented and locally verified; see
   `docs/superpowers/plans/2026-10-21-dashboard-phase2-plan.md` for task
   status and evidence. Final acceptance is in
   `docs/dashboard-review/04-phase2-acceptance.md`. Production deployment
   remains a separate operation. The broker now exposes the accepted `max_streams`,
   latest successful mux `link_since`, and per-node `dials_pinned_offline`.
   These are live-only observations and survive neither a broker restart
   nor a node-file reload. Losing an older overlapping link does not clear
   the surviving link's timestamp. Destination dial failures remain
   `dials_err`; a pin with no usable link is counted as pinned-offline.

   The chosen shared-seat policy is 0 until positively measured, then
   `min(3, max(1, floor(capacity_mbps / 5)))`; dedicated boards stay at 1.
   This admits a measured C3 at about 2.6 Mbps as one shared seat without
   increasing the ceiling for other boards or promising 5 Mbps of service.

10. **Status page & support**
    - Public status (proxy up/down, incidents). Support inbox / ticketing.
      SLA by plan.

11. **Billing lifecycle**
    - Auto-renew, dunning (retry failed payment), grace period, suspend→delete,
      refunds/credits, proration on plan change.

---

## Phase 3 — Growth

12. **Reseller / affiliate**
    - Sub-accounts with their own users and a markup; affiliate referral codes
      and payouts.
13. **Plan/product catalog**
    - Tiers by rate, quota, concurrency, region/device pinning, sticky sessions,
      rotating vs static exit.
14. **Analytics**
    - Business: MRR, churn, ARPU, usage cohorts. Ops: utilization per device,
      busiest hours, top destinations (aggregate, privacy-respecting).
15. **API & integrations**
    - Customer API to list/rotate credentials and pull usage; webhooks.

---

## Data model additions implied above

- **Customer**: identity, KYC status, AUP acceptance (ts, version), contact.
- **Subscription**: plan, status, current-period, renew flag, PSP references.
- **Invoice**: line items, tax fields, paid/void.
- **AbuseEvent**: user, signal, action, ts.
- **Device**: + assigned_user, region/geo, uptime, ssid (built), firmware,
  chip (`esp32s3` / `esp32c3`, from the firmware's HELLO since 2.4.22; the
  broker refuses an OTA whose image target differs from it).
- **Reseller**: parent of customers, markup, payout account.

## Explicitly out of scope / declined

- **Storing customers' credentials for other services**, or anything enabling
  account-takeover, credential stuffing, or CAPTCHA farms — these are the abuse
  cases residential proxies are infamous for; the AUP and abuse enforcement
  exist to keep the service out of that market.
- **Home WiFi passwords are never uploaded from the device** — the operator may
  enter one in the console; it is stored encrypted and shown masked.
- **Device position is operator-entered** (no GPS module): paste a Google Maps
  share link (`https://maps.app.goo.gl/…`) in the device editor and the admin
  API resolves it server side into lat/long (only google hosts are contacted,
  over TLS, no page body is read); the Devices table links the position to
  Google Maps (the original pin when a link was pasted); geo-IP city
  remains the hint.
