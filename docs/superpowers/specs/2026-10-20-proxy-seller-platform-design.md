# Proxy seller platform — design (2026-10-20)

Status: draft for review. Builds on `docs/PLATFORM.md` (capability map) and the
running stack (gost + broker + admin-api + flasher on Coolify, ESP32-S3 exit
nodes on mux1 firmware `2.4.21-mux1.1`).

## 1. Intent

Turn the admin dashboard + exit-node stack into a small, professional
**residential Thai-IP proxy service**: the operator sells access to home IPs
(one ESP32-S3 board per IP) to customers who browse or scrape through them.
Customers get a self-service page to see their endpoint, usage and expiry and
to renew; the operator gets a fleet/customer console that cannot oversell a
board, routes each customer through the board they are bound to, and keeps
them informed by email.

Why now: the service works technically (mux1, eviction, 40 streams), but the
dashboard still lets you set a 100 Mbps "rate" on a board that can push ~10
Mbps, cannot set a user back to unlimited, does not show which board a user
rides, and routes everyone through whichever board is least loaded. None of
that holds up once a second board (WiFi 6) is added or a customer is promised
"your IP".

## 2. Goals

1. **Never oversell a board.** Each board has a measured capacity; a shared
   board carries at most 3 customers and only while each still gets ≥ 5 Mbps;
   a dedicated board carries exactly one customer. The console refuses
   assignments that break this.
2. **Binding is real.** A customer bound to a board always exits through that
   board (stable IP). Unbound customers use the shared pool as today.
3. **Rate limits mean something.** A user's rate cannot exceed the board's
   measured capacity; 0 = unlimited (bounded by capacity) and can be set back.
4. **Fleet console** shows per board: capacity, slot/port, lat/long, WiFi
   SSID (+ operator-entered password, masked), bound users, live health
   (link, streams, heap), firmware, OTA.
5. **Public self-signup**: a visitor picks a plan, verifies their email,
   accepts the AUP, pays, and receives a working endpoint within minutes —
   or a clear "sold out" if no IP has spare capacity.
5b. **Customer portal** (own login): one customer (email) can hold **many**
   subscriptions/IPs; each shows endpoint `host:port`, user:pass (rotate),
   usage vs quota for the current plan period, expiry, renew/buy more.
6. **Payments**: Stripe Checkout (card + PromptPay) activates instantly; bank
   slip upload → automatic slip verification API → staff approval queue.
7. **Email throughout**: credentials on activation, expiry reminders, expired,
   renewed, quota warnings, payment received / approved / rejected.

## 3. Non-goals (this round)

- Rotating/residential-pool style IP rotation across many boards per request.
- KYC / ID verification (PLATFORM.md Phase 1 item 1) — deferred. Because
  signup is public (§6.1), the minimum safeguards ship with it instead:
  verified email before payment, AUP consent timestamped with version,
  Cloudflare Turnstile on the signup form, Stripe Radar for cards, one
  signup per email, and per-customer caps (max active subscriptions,
  configurable). Revisit KYC before selling to high-risk segments.
- Reseller accounts, affiliate, public status page, analytics dashboards.
- Replacing gost. It stays as the customer-facing proxy; per-device routing is
  done with gost services + broker slots (see §6.2), not by rewriting gost.

## 4. Anti-goals (would make it a failure even if it "works")

- Uploading the home WiFi password from the board. The firmware never sends
  it; the dashboard field is **operator-entered** and stored encrypted.
- A customer seeing anything about the fleet, other customers, or raw device
  identifiers (MAC) in the portal.
- Silent overselling: a "100 Mbps" plan on a 10 Mbps board.
- Password rotation as a side effect of viewing (today's `/credentials` quirk).

## 5. Constraints

- Hardware: ESP32-S3 on 2.4 GHz WiFi, one TLS link; realistic 5–15 Mbps per
  board total. Capacity is **measured**, never assumed.
- One VPS (Coolify, docker compose), Cloudflare in front of HTTP hosts; the
  proxy port itself is DNS-only (`proxy.jbacteam.com:3128`).
- Existing code: Go (admin-api, broker), static HTML/JS dashboard, JSON file
  store, gost v3 with http plugins (auth/limiter/observer). Build on it.
- Security posture from `docs/SECURITY.md` / `PLATFORM.md` stays: private-IP
  and SMTP blocks in firmware, AUP, per-connection logs.
- Thai customers and operator: UI strings Thai-first with English fallback;
  currency THB; email in Thai.

## 6. Decisions

### 6.1 Decided by the operator (as stated)

| Topic | Decision |
|---|---|
| Customers | Want a residential **Thai** IP for browsing/scraping. |
| Sharing rule | One IP/board is shared by **2–3** customers max, only while each still gets **≥ 5 Mbps**; a **dedicated** package pins one customer to one board exclusively. |
| Speed | Rate limits are capped by what the board can actually do. |
| Users table | Must show which device each user is bound to. |
| Device page | Operator can enter **lat/long**; show WiFi **SSID and password** (masked `****`). |
| Portal | Customers have their own profile page to **renew**; **email notifications** throughout. |
| Payments | **Stripe** (instant; card + Thai QR/PromptPay) **and** slip upload → verification API → **staff approval**. |
| Onboarding | **Public self-signup**: choose plan → pay → IP issued immediately (operator-created accounts remain possible from the console). |
| Account shape | **One customer (email) may buy several packages / several IPs.** |
| Quota period | **Per plan period** (from purchase/renewal for `days`), not calendar month; renewal resets the counter. |
| After expiry | **Released immediately** — the IP/slot is sellable to someone else at once; a late renewal may land on a different IP/port (customer is emailed the new endpoint). |

### 6.2 My calls (change if wrong)

1. **Routing pin via device slots.** gost gets a fixed range of extra
   services, one per *slot*: `:3131`–`:3160` (30 slots; host ports published
   in compose). Each slot's chain dials the broker's SOCKS5 with username
   `slot-N`. The broker maps `slot-N → device MAC` from the registry
   (admin-api sets it via `POST /nodes/{mac}/slot`) and opens that customer's
   streams only on that device's mux link (fallback: refuse with "device
   offline", never silently use another board). `:3128` stays the shared pool
   (least-loaded) for unbound users. A customer's endpoint is therefore
   `proxy.jbacteam.com:<slot port>`; moving a customer to another board changes
   the port and triggers an email. Rationale: gost cannot pass the customer
   identity down the chain, so the port is the identity; this is also how
   commercial proxy sellers hand out `ip:port:user:pass`.
2. **Capacity = measured.** Device page has **Measure** → admin-api downloads
   a 8 MB test object (`flash.jbacteam.com/speedtest.bin`, added to the
   flasher) through that device's slot and records `capacity_mbps` +
   timestamp; re-measure any time; show "never measured" loudly. Derived:
   `max_shared_users = min(3, floor(capacity_mbps / 5))`; a dedicated board
   has `max = 1`. The limiter answers `min(user.rate_mbps or ∞, capacity)`.
3. **Store → SQLite** (`modernc.org/sqlite`, no cgo) with a one-time import of
   `users.json`; tables: `users`, `devices`, `plans`, `orders`, `payments`,
   `slips`, `email_log`, `login_tokens`, `audit`. Keep the public REST shapes
   the dashboard already uses; add new endpoints rather than reshaping.
4. **WiFi password** is an operator-entered device field, AES-GCM with
   `CRED_KEY` (same as customer passwords), returned only on an explicit
   `reveal` call that is audit-logged. Update `PLATFORM.md`'s "declined" list
   to say *entered, never uploaded*.
5. **Lat/long** manual fields override geo-IP; device page shows a map link
   (OpenStreetMap) and the geo-IP city as a hint.
6. **Portal** at `portal.jbacteam.com`, served by admin-api as a second static
   app. Login by **email magic link** (users gain an `email` field; required
   for portal + notifications). Customers see: endpoint, credentials with
   explicit **Rotate** button, usage bar, expiry, plan, renew/buy, invoices
   (simple receipts), AUP text. Nothing about devices beyond "Thai residential
   IP, city X".
7. **Plans** are a small catalog managed in the admin console: name, days,
   GB (0 = unlimited), rate cap (0 = board capacity), kind `shared|dedicated`,
   price THB, active flag. Buying = `orders` row → payment → on paid:
   create/extend user, bind to a board with free capacity (dedicated plans
   require the operator to pick the board; shared plans auto-pick the board
   with the most spare capacity, or queue for the operator if none).
8. **Stripe**: Checkout Session (payment methods: `card`, `promptpay`,
   currency THB), `checkout.session.completed` webhook verified with the
   Stripe signing secret → mark order paid → provision. Existing
   `/webhook/payment` stays for the slip path.
9. **Slip path**: portal upload (jpg/png/pdf ≤ 5 MB) → stored under
   `/data/slips` → verification API call (pluggable `SlipVerifier` interface;
   provider and credentials are the operator's choice; fall back to "needs
   manual check" when unavailable) → appears in admin **Approvals** queue with
   verifier result, amount expected vs read → approve/reject with reason →
   email.
10. **Email** via Resend's HTTP API (`EMAIL_FROM`, `RESEND_API_KEY`), Thai
    templates with English second half; every send logged in `email_log`
    (user, template, status, provider id). Scheduler inside admin-api (hourly):
    expiring in 7/3/1 days, expired today, quota ≥ 80 % / 100 %.
11. **Admin console** (same static app, grown): Users (device, plan, expiry,
    usage bar, live Mbps from observer, actions), Devices (as in Goals §4 plus
    slot/port assignment and capacity), Plans, Orders, Approvals, Email log,
    Settings. Keep the current look; no framework.
12. **Fix now, independent of the rest**: rate/quota update ignoring 0
    (`admin.go` update path must distinguish "absent" from `0`); broker sends a
    queued OTA to an already-connected mux link (keepalive tick checks
    `takeOTA`).
13. **Sellable unit is the exit IP, not the board.** Boards that report the
    same `RemoteIP` form one IP group; capacity, the 3-customer rule and
    "dedicated" apply to the group (a dedicated customer owns the whole IP).
    The console warns when two boards share an IP and shows the group.
14. **Pool port `:3128` is transitional.** Every customer is bound to an IP
    group and uses a slot port. Dedicated IP groups never serve pool
    traffic; shared groups keep serving `:3128` only for *unbound* legacy
    users. Phase B migrates today's users (`earth`, `testuser`) to slots and
    emails them; afterwards `:3128` stays up as a documented fallback only.
15. **Plan changes take effect at renewal.** Mid-term kind/size changes are
    an operator action (force-move with a port-change email), not a
    customer self-service path, in this round.
16. **Language.** Portal, signup and all emails are Thai-first with English
    below. The admin console stays English; its strings live in one map from
    Phase A so a Thai pass later is mechanical.
17. **Ports.** `3128` reaches gost because Coolify sets `PROXY_PORT=3128` for
    the compose publish; no provider firewall was observed (7000/8000/3128
    open). Slot ports `3131–3160` are published the same way in compose and
    verified reachable from outside as a Phase B acceptance check.
18. **Signup flow** (public): plan page → email → magic link verifies the
    email and creates the `customer` → AUP consent (version + ts) → Turnstile
    → Stripe Checkout (or slip upload) → on paid: generate username
    `u-<6 base32>` + password, bind to the IP group with the most spare
    capacity matching the plan kind (dedicated → an empty group); if none,
    the order is held as `waiting_capacity`, the customer gets a "sold out,
    we will email you" page and the operator an alert; nothing is charged for
    a dedicated plan until a group is available (Checkout is created only
    after a capacity reservation that expires in 15 min).

## 7. Data model (new/changed fields)

- `customers` (new): `id`, `email` (unique, verified_at), `name`,
  `aup_version`, `aup_accepted_at`, `max_subscriptions`, `created_at`,
  `last_login_at`, `notes`.
- `subscriptions` (new; one per purchased IP): `id`, `customer_id`, `plan_id`,
  `username` (the proxy user, fk), `ip_group`, `device_mac`, `slot`,
  `period_start`, `period_end`, `quota_bytes`, `used_bytes`, `status`
  (`active|expired|suspended|waiting_capacity`), `last_notified` (json: which
  reminder templates were sent for this period).
- `users` (proxy credentials; keep for gost plugins): + `subscription_id`,
  `device_mac` (now enforced), `slot` (derived), `status`. `rate_mbps` /
  `quota` semantics move to the subscription's plan; the plugins read through.
- `devices` (new table; registry stays the live source for online/stats):
  `mac` (pk), `name`, `kind` (`shared|dedicated`), `ip_group` (derived from
  exit IP, cached), `slot` (unique, nullable), `capacity_mbps`,
  `capacity_measured_at`, `lat`, `lon`, `wifi_password_enc`, `notes`.
- `plans`: `id`, `name`, `days`, `quota_gb`, `rate_mbps`, `kind`, `price_thb`,
  `active`.
- `orders`: `id`, `customer_id`, `subscription_id` (null for a new IP until
  provisioned), `plan_id`, `amount_thb`, `method` (`stripe|slip`), `status`
  (`pending|paid|approved|rejected|cancelled|waiting_capacity`),
  `stripe_session_id`, `reservation_expires_at`, `created_at`, `paid_at`.
- `slips`: `order_id`, `file`, `verifier_result` (json), `reviewed_by`,
  `reviewed_at`, `reason`.
- `email_log`, `login_tokens` (email, token hash, expires, used), `audit`
  (who, what, target, ts).

## 8. Interfaces

- gost: `deploy/gost/gost.yaml` gains 30 slot services (generated by a small
  template in the Dockerfile from `SLOT_COUNT`), each with its own chain
  whose SOCKS5 connector auth is `slot-N:<POOL_TOKEN-derived secret>`.
- broker: SOCKS5 server reads the username; `slot-N` → pinned device;
  `/status` adds per-slot stats; `/nodes/{mac}/slot {slot}`; `/nodes/{mac}/speedtest`
  is **not** on the broker — measurement goes through gost like a customer.
- admin-api REST additions: `/api/devices/{mac}` PUT (name, kind, lat, lon,
  notes, wifi_password), `POST …/reveal-wifi`, `POST …/measure`,
  `POST …/slot`; `/api/plans` CRUD; `/api/orders`; `/api/approvals` +
  `approve|reject`; `/api/email-log`; portal endpoints under `/portal/api/*`
  with magic-link sessions; Stripe webhook `/webhook/stripe`.
- Firmware: **no change required** for this round.

## 9. Phasing (input to the plan)

- **A — foundation**: SQLite store + import; fix rate/quota 0; device fields
  (name, kind, lat/lon, WiFi pw enc, notes); users table shows device/plan;
  broker OTA-to-live-link fix.
- **B — capacity & pinning**: speedtest object + Measure; capacity rule and
  admission checks in assign; slot services in gost; broker slot routing;
  device page slot/port; portal-facing endpoint string uses the slot port.
- **C — plans & money**: plans catalog; customers + subscriptions; orders;
  capacity reservation; Stripe Checkout + webhook; provisioning on paid;
  receipts; per-period quota in the plugins; expiry releases the slot.
- **D — signup, portal & email**: public plan page + signup (Turnstile,
  email verification, AUP); magic-link login; portal pages (multi-IP);
  Resend + templates + scheduler; slip upload + verifier interface +
  approvals queue; migration of legacy users to slots with emails.
- **E — polish**: live Mbps per user, audit views, alerts (device offline),
  PLATFORM.md/README updates.

Each phase ships deployable on its own; A and the OTA fix first because they
are small and remove today's sharp edges.

## 10. Left to the builder

- Exact SQL schema/migrations, SQLite busy handling, backup of `/data`.
- Portal and console layout details (keep the existing visual style).
- Slot count and port range (30 is a guess; make it `SLOT_COUNT`).
- Speedtest size/duration and how to present a stale measurement.
- Email template wording (Thai first); magic-link TTL (suggest 15 min, single use).
- Stripe test vs live keys via env; idempotency on webhooks.
- How the scheduler avoids double-sending across restarts (persist last-sent per
  user+template).

## 11. Still needed from the operator (do not block Phases A–B)

1. Slip verification API: which provider (SlipOK, EasySlip, bank API…) and
   credentials. Until then the slip path works with manual approval only.
2. Stripe account + test keys; Turnstile site/secret keys.
3. Portal domain (`portal.jbacteam.com`?) and email sender
   (`noreply@jbacteam.com` — needs domain verification at Resend).
4. Initial plan catalog (names, days, GB, THB). Default if none given: Shared
   30 days and Dedicated 30 days, prices TBD by the operator before go-live.
5. Who approves slips — the admin token holder only, or named staff logins
   (would add a small `staff` table + roles).

## 12. Resolved by the builder check (2026-10-20)

Operator answered: public self-signup; one customer may hold several
packages/IPs; quota per plan period; expired slots are released at once.
Settled as the author's calls: IP group as the sellable unit (§6.2.13), pool
port transitional with legacy migration (§6.2.14), plan changes at renewal
(§6.2.15), language split (§6.2.16), ports (§6.2.17), signup flow with
capacity reservation (§6.2.18).
