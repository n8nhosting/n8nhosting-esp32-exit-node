# Sales page v2 and admin Overview — 23 October 2026

Implements [the sales-page design handoff](../proxy-server-salespage-handoff/DESIGN_HANDOFF.md)
(P1–P4) and, at the owner's request, carries the same visual family into the
whole admin dashboard with a rebuilt Overview. Base: `main` at `5d5bee4`.
**Local code, tests and screenshots only: nothing is committed, pushed or deployed.**
The concept images in the handoff are AI art direction; every image under
[evidence-2026-10-23-salespage-v2](evidence-2026-10-23-salespage-v2/) is a real
screenshot of this code on the local fixture (isolated SQLite, fake broker,
synthetic plans/orders marked LOCAL QA).

## สรุปสำหรับเจ้าของ

- หน้าขายใหม่ตาม handoff ครบ P1–P4: หัวเว็บแยกเมนูสาธารณะกับเมนูบัญชี, ป้ายสถานะที่อ่านแล้วไม่เหมือนระบบพัง
  และแสดงตั้งแต่ HTML แรก (ไม่กระตุก), แผนที่ระบุว่าเป็น "ภาพประกอบ" แทน "เชื่อมต่อแล้ว", แถบจุดเด่น 4 ข้อ,
  ขั้นตอนที่ 2 บอก "ใช้ได้เมื่อเปิดรับคำสั่งซื้อ", ส่วนราคาว่างที่มีลิงก์ไปต่อได้, การ์ดราคาจริงจากฐานข้อมูล,
  หมายเหตุเรื่อง IP ข้างบอร์ดเฉพาะบัญชี, FAQ และ CTA ท้ายหน้าที่ไม่ชวนสั่งซื้อขณะยังไม่เปิดขาย
- หน้า admin ทั้งหมดใช้ภาษาภาพเดียวกับหน้าขาย (เส้นขอบบาง, โลโก้, พื้นหลัง) แต่ยังสงบตาม DESIGN.md
- Overview ใหม่: ยอดขายเดือนนี้, ออร์เดอร์ที่ชำระแล้ว, แพ็กเกจที่ใช้งานอยู่, สลิปรอตรวจ + ตัวเลขเครือข่ายเดิม,
  ตัวเลือกช่วงเวลา 24 ชม./7 วัน/30 วัน, แผนที่บอร์ดจริงตามพิกัด, การ์ดบอร์ดทุกตัว, ลำดับคำสั่งซื้อ, ผู้ใช้ที่ใช้มากสุด
- ข้อความใหม่ 11 key ครบ 6 ภาษา ไทย/อังกฤษพร้อมใช้ อีก 4 ภาษาควรให้เจ้าของภาษาตรวจก่อนลงโฆษณา
- แก้คำที่ผิดกฎเดิมไปพร้อมกัน: "ไม่จำกัด GB" (ทุกภาษา) → "ไม่มีโควตาข้อมูล", รัสเซีย "плата" → "устройство"

## Sales page (P1–P4)

| Handoff item | Implementation |
|---|---|
| 6.1 header | `nav` moved into the header: public (plans, guide) left, account (my plans, orders) grouped right. Same routes and `data-page` hooks. |
| 6.1 readiness | Server renders the banner for its own readiness (`portalReadinessBanner`); `showReadiness()` replaces it only when the state differs, so no layout shift and no re-announcement. Copy no longer says "provider is setting up". |
| 6.2 hero | One H1 (`seo.home.h1`), no gradient text; Thai/CJK line heights; caption "Thailand network illustration" replaces "Connected"; the live blink dot is gone. `data-photo-slot="hero"` kept. |
| 6.3 benefits | Four-cell band (`landing.proof.1–4`), icons `aria-hidden`. |
| 6.4 steps | Numbered sequence; step 2 shows `landing.step.2.closed` while orders are closed (server state). |
| 6.5 plans | Same `portalPlanCard` and active-plan query; adds bonus days and the dedicated IP note; button says "View plan details" while closed, "Select this plan" when open. Empty state: icon, title, factual body, links to plans and guide. |
| 6.6 compare | Two panels with intro; dedicated note under the dedicated panel. |
| 6.7–6.9 | Use-case cards, native `<details>` FAQ (first open), closing band with plans + guide; body copy follows readiness. |
| P4 SEO | SEO head, canonical/hreflang/OG, sitemap, CSP and private noindex untouched; no structured data added. |

Readiness used by the page (`portalOrdersOpen`) is the same expression as
`/portal/api/config` (`portalLoginReady` now feeds `login_ready`).

## Admin

- Shared CSS only for Users/Devices/Sales/Settings: one fine edge per container,
  the sales-page wash and brand mark, a full-height sidebar layer, login with the
  dot map. Markup, ids and workflows unchanged.
- `/api/overview` gains `sales` (`overview_sales.go`): revenue and paid orders by
  `paid_at` in the UTC month, last month's revenue, orders holding a seat, slips
  in review, paid orders waiting for a board, active subscriptions, 30 daily
  revenue buckets. A failed query is logged and leaves the network overview usable.
- Overview: business row, network row, one range control (24 h / 7 days / 30 days)
  for traffic and top users, fleet map, board tiles, order pipeline, attention and
  activity. Chart series changed from accent + warning orange (a status colour) to
  validated steps `#7184E2` / `#B88A34`; the y-axis margin now fits its labels.
- Fleet map: `tools/render_landing_map.py` also writes
  `admin-api/dashboard/thailand-map.svg`; the landing map output is byte-identical.

## Copy changes (all six locales)

Added: `landing.card.illustration`, `landing.proof.4`, `landing.step.2.closed`,
`landing.pricing.empty.title`, `landing.pricing.empty.body`, `landing.plan.view`,
`landing.compare.intro`, `landing.dedicated.note`, `landing.faq.intro`,
`landing.cta.body.closed`, `ui.readiness.closed.body`.
Removed (replaced): `landing.card.online`, `landing.pricing.empty`,
`ui.the.provider.is.setting.up.email.verification.and.sign.in.you.can`.
Reworded: `ui.unmetered.gb.within.the.plan.speed.cap`, `ui.unmetered.gb`
(every locale said "unlimited/no limit GB"; now "No data quota", review C7).
Russian: 23 strings use `устройство` for the board instead of `плата` (review C14).
zh-CN, ja, ru and vi are drafts that need a native reviewer before paid campaigns.

## Tests (Windows, local)

| Command | Result |
|---|---|
| `go vet ./...`, `go test ./...`, `go test -race ./...` (admin-api) | pass |
| `go test ./...` (broker, untouched) | pass |
| `node --test tools/portal_i18n_test.cjs` | pass (253 keys × 6) |
| `tools/dashboard_smoke.cjs` (Overview assertions updated: 4 + 4 KPIs, map, tiles, pipeline, range) | pass |
| `tools/dashboard_connection_smoke.cjs`, `tools/dashboard_settings_smoke.cjs` | pass |
| `tools/portal_marketing_smoke.cjs` | 11 groups pass |
| `tools/dashboard_commerce_smoke.cjs` | 19 groups pass |

New Go tests: `TestPortalLandingArtMakesNoLiveClaim`,
`TestPortalLandingFollowsOrderReadiness`, extended
`TestPortalLandingRendersActivePlans`/`WithoutPlans`, `TestOverviewSummarizesSales`,
`TestDashboardServesThailandMap`, `TestDashboardOverviewMarkers`.

Browser checks on the fixture:

- **Overflow and headings:** six locales × 320/390/768/1440 px show no horizontal overflow, exactly one H1, and no control under 44 px in the header or main.
- **Layout shift:** CLS 0–0.0009, because the readiness banner arrives with the HTML.
- **Reduced motion:** 0 of 11 map animations run with `prefers-reduced-motion: reduce`, and all 11 run otherwise.
- **No JavaScript:** curl of `/portal/th/` returns one H1, every section, the banner and the empty state, with no inline styles.
- **Japanese and Chinese headlines:** they break at phrase or punctuation boundaries (`word-break: auto-phrase` / `keep-all`, and an em-based measure instead of `19ch`).

Screenshots and their provenance: [evidence-2026-10-23-salespage-v2](evidence-2026-10-23-salespage-v2/README.md).

Design checks: dataviz palette validator passes for the two series on `#222631`;
the impeccable detector reports no anti-patterns, only advisory "outside DESIGN.md"
literals (113, many pre-existing); DESIGN.md now documents the added tokens.

## Not done / owner decisions

- No commit, push or deploy. Production still shows "no plans on sale" until plans
  are activated in Sales → Plans.
- Native review of zh-CN/ja/ru/vi copy; Thai keyword/"residential" decisions (C11/C12) unchanged.
- The hero photo slot stays SVG until a licensed photo is supplied.
- Revenue is gross `amount_satang` of paid orders; there is no refund model to subtract.
