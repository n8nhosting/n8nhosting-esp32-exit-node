# Claude continuation handoff — 22 October 2026

เริ่มอ่านไฟล์นี้ก่อนแผนเก่า งาน implementation และ deployment หลักเสร็จแล้ว
งานถัดไปคือ activation/integration acceptance และ hardware acceptance ที่มีเงื่อนไข
อย่าเริ่มทำ Dashboard Phase 2 หรือระบบขายใหม่จากต้นแผนเดิม

เอกสารนี้ถูกเขียนลง repo เพื่อส่งต่อบริบทให้ Claude ไม่ได้หมายถึงได้เปิด Claude session
หรือส่งข้อความผ่าน service ภายนอกแล้ว
เปิด Claude ที่ repo root เพื่ออ่าน `CLAUDE.md` ซึ่งชี้มาที่ handoff นี้

## 1. เริ่มงานจากสถานะนี้

Repo: `D:\WorkSpace_AI\Gost Proxy + Esp32-S3 Flash\esp32-exit-node-v1.4-mux1.3-relaybuf\repo`

- Branch `main`; HEAD ก่อน commit handoff นี้คือ `8a7294b`
- Runtime ที่ตรวจยืนยันหลัง deploy: `7a6f8843e8ff9741de7745fe241b8ce6b66836fa`
- `8a7294b` เป็น docs/evidence เท่านั้น ไม่ได้ deploy
- `git status --short` ก่อนงาน handoff มีเพียง `?? output/`; รักษา artifacts นี้
  และตรวจ dirty files ใหม่ เพราะเจ้าของหรือ agent อื่นอาจทำงานต่อแล้ว
- Handoff นี้ไม่เปลี่ยน runtime ไม่ deploy ไม่ restart collector และไม่แตะ firmware

อ่านตามลำดับ:

1. [`AGENTS.md`](../../../AGENTS.md) — operational rules, serial reset, ESP-IDF, deployment
2. [สรุปงานภาษาไทย](../../dashboard-review/08-work-summary-and-next-steps.md)
3. [Portal/SEO/SEM/analytics delivery](../../dashboard-review/07-portal-language-seo-sem-analytics.md)
4. [Production manifest](../../dashboard-review/evidence-2026-10-22-portal/production-verification.json)
   และ [local verification manifest](../../dashboard-review/evidence-2026-10-22-portal/local-verification.json)
5. [Platform/commerce report](../../dashboard-review/06-platform-commerce-acceptance.md)
   — ส่วน runtime/soak เก่าถูก supersede โดยรายงาน 07
6. [Per-board seat decision](../specs/2026-10-22-per-board-seats-design.md),
   [portal design](../specs/2026-10-22-portal-international-marketing-design.md),
   [hardware recorder](../../../tools/HARDWARE_SOAK.md)

ใช้ git/โค้ด/หลักฐานล่าสุดก่อนอ้างสถานะจากรายงานย้อนหลัง โดยเฉพาะ README ตารางจำนวน
tests เก่า และแผน Phase 2 ที่เดิมใช้ IP-group admission

## 2. สิ่งที่เจ้าของตัดสินใจแล้ว — ไม่ต้องถามซ้ำ

- Counter reset: delta = ค่าปัจจุบันหลัง reset; first sample = 0
- Drawer: `/api/metrics?entity=node&id=<mac>&range=24h`
  และ `/api/events?subject=<mac>&limit=50`
- `throughput_bps` เป็น **bytes/s**, แสดง Mbps ด้วย `×8/1e6`
- Seat/admission แยกต่อบอร์ด/MAC แม้ public IP เดียวกัน
- Shared capacity ไม่เป็นบวก = 0 seats; วัดเป็นบวกใช้
  `min(3, max(1, floor(capacity_mbps/5)))`; Dedicated = 1 seat และสงวนบอร์ด
- มี automatic slot reservation ที่แก้ได้ และต้องแสดง slot/port ชัดเจน
- รายละเอียดบอร์ดแสดงครบพร้อมกัน ไม่กลับไปเป็น section tabs ที่ซ่อนข้อมูล
- Portal ใช้ `th`, `en`, `zh-CN`, `ja`, `ru`, `vi`; dynamic operator data ไม่แปลเงียบ ๆ
- ช่องทางชำระเงิน Stripe + โอนเงินแนบสลิป; ราคา researched drafts ยัง inactive
- เจ้าของจะตั้ง Stripe/bank/Resend/Turnstile ใน Coolify เอง แล้วแจ้งว่าพร้อมตรวจ
  อย่าขอ secret ในแชตหรือกรอกข้อมูลบัญชีสมมติแทน
- มีสิทธิ์ทำ implementation/ตรวจ/แก้ตามงานที่ตกลงไว้ แต่ไม่มีคำสั่งเปิด campaign
  ใช้งบโฆษณา หรือทำธุรกรรมเงินจริงในงาน Portal นี้

Health weights 40/30/10/10/10 และกฎ stale/missing ใช้ตามโค้ด/รายงานรับมอบ
SQLite มี connection เดียว: อ่าน store/network ก่อน write transaction;
events หลัง mutation สำเร็จและนอก transaction อย่าเพิ่ม telemetry ticker อีกตัว

## 3. แผนที่โค้ดที่ต้องรู้

| ตำแหน่ง | หน้าที่ |
|---|---|
| `admin-api/dashboard/index.html` | Dashboard ไฟล์เดียว, Devices/Users/Sales/Settings, Portal marketing editor |
| `admin-api/marketing.go`, `marketing_test.go` | Typed IDs, SEO overrides, optimistic revision, migration/config API contracts |
| `admin-api/portal_embed.go` | Public SSR/metadata, locale routes, canonical/hreflang/OG, sitemap/robots/CSP/private noindex |
| `admin-api/portal_auth.go` | Customer identity/login/verification contracts |
| `admin-api/portal/portal.js` | Customer views, account/order/slip/receipt flow, public/private document boundary, legacy route normalization |
| `admin-api/portal/i18n.js`, `locales/*.json` | Route/locale helpers, 212 keys ต่อภาษา, formatting |
| `admin-api/portal/analytics.js` | Consent/config revision, typed provider loaders, sanitized manual public events |
| `admin-api/portal/portal.css`, `og/` | Portal styling และ six-language PNG/SVG social artwork |
| `admin-api/db.go`, `commerce_test.go` | Schema/migrations และ commerce regression |
| `tools/portal_i18n_test.cjs` | Dictionary/placeholder/route/privacy checks |
| `tools/portal_marketing_smoke.cjs` | Real isolated Go/SQLite, marketing UI/SSR/consent, inert vendor stubs, 11 groups |
| `tools/dashboard_commerce_smoke.cjs` | Commerce UI + 6 localized private journeys, 19 groups |
| `tools/render_portal_og.cjs` | Reproduce local six-language OG assets |
| `tools/hardware_soak.cjs` | Read-only recorder, private allowlisted evidence, no load/OTA/serial |

`/api/marketing` เป็น admin authenticated GET/PUT; stale edit คืน 409
`/portal/api/marketing` เป็น sanitized public config ไม่เปิด secret หรือ arbitrary scripts
GTM ใช้แทน direct tags ไม่ใช่เปิดทั้งสองแบบพร้อมกัน

## 4. Runtime และข้อมูลจริงที่ตรวจไว้

ข้อมูลต่อไปนี้เป็น **snapshot หลัง deploy 22 ต.ค. 2026 ประมาณ 13:26–13:27 +07**
ไม่ใช่ live verification ณ เวลาที่ Claude อ่าน ต้อง refresh แบบ read-only ก่อน action

- Coolify app `j2qrpgae6rhrcuxiferirwn0`, deployment `jokgjuch6v5y7lvkjz9iuyjo` Success
- ทุก service เริ่มครบ 13:25:38 +07; container suffix `20261022T062427`
- Admin `https://admin.jbacteam.com`, portal `/portal/th/`, flash `https://flash.jbacteam.com`
- `PORTAL_BASE_URL=https://admin.jbacteam.com/portal/` ตั้งแล้ว
- SQLite integrity ok, schema 8; users/devices/plans = 4/4/3,
  customers/subscriptions/orders = 0/0/0; legacy user contracts เปลี่ยน 0
- Marketing disabled, IDs/SEO overrides ว่าง; login/Stripe/manual readiness ยัง false
- Private pre-v8 backup: `/root/exitnode-pre-v8-20261022-portal/platform.db`
  directory 0700 / file 0600; live DB อยู่ใน existing admin-data Docker volume
  ห้ามคัดลอก live DB, slips, tokens หรือ secrets เข้า repo
- AEC0 capacity = **1.4 Mbps** จาก audited `device.measured` เวลา 13:07:18 +07
  บน runtime ก่อนหน้า ระหว่าง build; งาน Portal ไม่ได้ยิง measure นี้
  **อย่า restore 2.4 Mbps จาก backup**; name/kind/slot เดิมยังคงอยู่

บอร์ดที่เก็บหลักฐาน: C3 `1c:db:d4:c5:7e:b0` slot 3,
C3 `ac:a7:04:ba:ae:c0` slot 4, S3 `e8:3d:c1:f2:86:bc` slot 1
ทั้งสาม online / firmware 2.4.25 / OTA ok ใน snapshot
latest/stable ยังคง **2.4.21-mux1.2**; อย่า promote จากการ build สำเร็จ

## 5. งานต่อไปตามลำดับ

### A. ตรวจสถานะก่อนเปลี่ยนอะไร

อ่าน git status/log, ตรวจ deployment source, readiness และ collector ปัจจุบันแบบ read-only
อย่าใช้ serial C3 ตรวจสุขภาพ อย่าทำ measure/OTA/redeploy เพียงเพื่อ refresh handoff
ถ้า owner ยังไม่พร้อม config ให้เก็บ pending ไว้และทำ local/content review ต่อได้

### B. Hardware collection — รักษาความต่อเนื่อง

- Current snapshot collector: `exitnode-soak-20261022-v2425-i18n`
- Namespace: `broker-j2qrpgae6rhrcuxiferirwn0-20261022T062427`
- Output: `/root/exitnode-soak-20261022-0550/evidence/run-5/`
- เริ่ม `2026-10-22T06:26:02.944Z` = 22 ต.ค. **13:26:02 +07**
- คาดครบ duration 23 ต.ค. **13:26:02 +07** ถ้าไม่ถูกขัดจังหวะ
- Snapshot แรก 2 samples / 0 failed polls / ทั้งสาม online; ยังไม่ใช่ 24-hour pass
- Run 2 เก็บ 416 samples / 6h55m40s / 0 failed polls แล้วถูกหยุดก่อน deploy;
  run 3/4 มี 5 samples ต่อรอบ ทั้งหมด `completed:false`; อย่ารวมเวลาข้ามรอบ
- Collector code server: `/root/exitnode-soak-20261022-0550/code/hardware_soak.cjs`
  ใช้ official pinned Node image, user 10001, read-only filesystem/code mount,
  private evidence mount, broker-local GET only ไม่มี admin token/port สาธารณะ

อ่าน `run.json`, `samples.jsonl`, `result.json` จริงและรายงานช่วงที่ขาด
`completed:true` หมายถึงครบ duration ไม่ใช่ hardware pass โดยอัตโนมัติ
ถ้า deployment ถัดไปเปลี่ยน broker ต้องปิด recorder เดิมอย่างเรียบร้อย
เก็บ interrupted result แล้วเริ่ม container/output **ใหม่** เช่น run 6
ตาม `tools/HARDWARE_SOAK.md`; อย่า reuse path หรือปล่อยใช้ namespace ของ broker เก่า

### C. Provider activation หลังเจ้าของแจ้งพร้อม

ใช้ variable names/checklist ในรายงาน 06; secrets เป็น runtime config
เริ่ม Stripe test mode, email/login บน hostname จริง, Turnstile server verification,
signed raw-body webhook, amount/currency/replay/late-capacity handling,
private slip upload/review และ own-account isolation
การ approve slip หรือจ่ายเงินจริงต้องเป็นการกระทำที่เจ้าของตั้งใจ ไม่สร้างรายการจริง
เพื่อให้ผลทดสอบดูครบ และยังไม่เปิดขาย draft plans อัตโนมัติ
Readiness true เพียงอย่างเดียวไม่พิสูจน์ว่า provider ใช้งานได้

### D. Analytics/SEO/SEM acceptance

ตั้ง real IDs ใน Settings → Portal marketing เมื่อเจ้าของพร้อม แล้วตรวจ consented
network และ provider dashboard จริง รวม reject/withdraw/config change/private boundary
disable/ตรวจ provider-side automatic collection ตามคู่มือก่อนใช้งานจริง
Google Ads conversion label เก็บได้ แต่ **ไม่มี verified purchase conversion pipeline**
และยังไม่มี cross-page UTM/click-ID attribution
อย่าเปลี่ยน click, redirect หรือ slip upload ให้กลายเป็น purchase event

Public SEO/OG มี 18 หน้าแล้ว; private 36 routes noindex และไม่มี canonical/OG
ทำ native commercial review, ตรวจ keyword demand และ Search Console ต่อได้ตามสิทธิ์
keyword ปัจจุบันเป็น hypothesis ไม่ใช่ Keyword Planner data; meta keywords ไม่ช่วย
Google ranking; ไม่มี campaign/spend/indexation/ranking claim
หากเปิด provider ใหม่ต้องตรวจ CSP เฉพาะ origin ที่จำเป็น ไม่เพิ่ม wildcard/unsafe-inline
Cloudflare edge เคย inject beacon markup แต่ app CSP ไม่อนุญาตโดเมนนั้น
อย่าแก้ edge security/analytics หรือ whitelist เพื่อทำให้ log เงียบโดยไม่มีเหตุจากงาน

## 6. Regression ที่มีหลักฐานและวิธีรันซ้ำ

ล่าสุด admin test 278 cases / 149 top-level ผ่าน 13.199s ที่ `7a6f884`; vet ผ่าน
Full race 278 cases ผ่าน 47.610s ที่ `8782a12` (Go source เดียวกันก่อน tiny JS fix)
Broker tests/vet ผ่าน (test cached; broker ไม่เปลี่ยนใน Portal task)
Marketing 11 groups ที่ `7a6f884`, commerce/i18n 19 ที่ `7419069`;
แยก source และ fixture scope ไว้ใน manifest อย่าอ้างว่า 30 groups รันที่ commit เดียว

บน Windows PowerShell จาก repo root:

```powershell
$env:GOTMPDIR = $env:TEMP
$env:GOCACHE = Join-Path $env:TEMP 'codex-exitnode-go-cache'
$env:GOTOOLCHAIN = 'local'
$env:GOPROXY = 'off'
Push-Location admin-api
go test ./...
go vet ./...
# ใช้เมื่อแก้ concurrent/backend behavior หรือจำเป็นต่อ regression:
go test -race ./...
go build -o ../output/playwright/admin-claude-fixture.exe .
Pop-Location
Push-Location broker
go test ./...
go vet ./...
Pop-Location
node --test tools/portal_i18n_test.cjs
$env:DASHBOARD_FIXTURE_EXE = (Resolve-Path output/playwright/admin-claude-fixture.exe).Path
node tools/portal_marketing_smoke.cjs
# รอ suite แรกจบและปิด fixture ก่อนรัน suite ที่สอง:
node tools/dashboard_commerce_smoke.cjs
Remove-Item Env:DASHBOARD_FIXTURE_EXE
```

Browser suites ใช้ Playwright-core/Chromium ที่ติดตั้งจาก package cache หรือระบุ
`PLAYWRIGHT_CORE_PATH` / `DASHBOARD_CHROME` ตามตัว script; ไม่เพิ่ม framework/dependency
เพื่อรันทดสอบโดยไม่จำเป็น ทั้งสอง suite ใช้ `output/playwright/fixture.json` ร่วมกัน
จึงต้อง **รันทีละตัว** และอย่า overwrite executable ขณะที่ fixture ยังเปิด
ตรวจ exit code ทุกคำสั่ง; Go telemetry upload.token Access denied เคยเป็นข้อความ
ประกอบที่ไม่ทำให้ test/build fail อย่าคิดว่าล้มเหลวจากข้อความนี้โดยไม่ดู exit/result

Marketing ใช้จริงเฉพาะ isolated Go/SQLite/SSR; SDK ถูกแทนด้วย inert local stubs
Commerce customer APIs บาง journey ถูก intercept ด้วย synthetic data
ผลผ่านนี้ไม่พิสูจน์ live delivery/payment/email/native translation/indexation
ภาพ production กับ local ระบุแยกไว้ใน evidence README แล้ว

## 7. จุดแก้ท้ายสุดที่มี regression แล้ว — อย่าย้อนกลับ

- Private document title/description แปลครบ 36 routes ไม่มี canonical/OG
- `/portal/` และ `/portal/index.html` full-document redirect ไป remembered locale
  ก่อน bootstrap/SDK; `index.html` เป็น home ไม่ใช่ชื่อ page ใหม่
- Legacy verify hash token ส่งได้ครั้งเดียวและ scrub จาก URL;
  sensitive query ยัง block SDK แม้เคย accept consent ไว้
- การข้าม public → private แทน document เพื่อไม่ให้ SDK/listener จาก public ค้าง
- Auth check ล้มเหลวถือ unknown ไม่ใช่ signed-out ที่ยอมให้โหลด tracker
- TikTok loader มี canonical bootstrap/instance queue fields; อย่าลดเหลือ stub
- Unsaved admin marketing edits รักษาไว้, refresh skip Settings, stale save 409

## 8. รูปแบบส่งมอบรอบหน้า

บอกว่าแก้/ทดสอบ/commit/push/deploy อะไรจริง แยก local fixture, test-mode provider,
production และ hardware acceptance พร้อม source/deployment/time
อัปเดต handoff เมื่อมีสถานะใหม่โดยรักษาประวัติ ไม่แทนค่าจาก snapshot ด้วยการเดา
Commit เฉพาะ source/docs/curated evidence; ไม่ stage `output/` ทั้งโฟลเดอร์
Coolify เป็น manual deploy: push ไม่ deploy และ deploy จะเปลี่ยนทั้ง stack
ไม่ deploy เอกสารล้วนเพื่อให้ HEAD กับ runtime hash เหมือนกัน

ข้อความเริ่มต้นให้ Claude:

> อ่าน AGENTS.md และ docs/superpowers/plans/2026-10-22-claude-continuation-handoff.md
> ก่อนทำงาน ตรวจ git และ production/collector snapshot แบบ read-only แล้วสรุปงานที่ยังค้าง
> รักษางานที่ทำแล้วและกฎ Seat ต่อบอร์ด ทำ activation acceptance เมื่อเจ้าของแจ้งพร้อม
> หากยังไม่มี provider config ให้ทำ local/content review ที่ไม่ต้องใช้ secrets ต่อ
> อย่าเริ่ม implement Phase 2 ใหม่ อย่า deploy docs-only หรือรบกวน soak โดยไม่จำเป็น
