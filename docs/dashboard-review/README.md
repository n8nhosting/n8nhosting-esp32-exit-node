# Dashboard redesign — สรุปจาก mockup 3 ภาพ (2026-10-21)

> สรุปงานและจุดเริ่มต่อสำหรับ Claude ล่าสุด (22 ต.ค. 2026):
> [สรุปสิ่งที่ทำแล้วและงานค้าง](08-work-summary-and-next-steps.md) และ
> [Claude continuation handoff](../superpowers/plans/2026-10-22-claude-continuation-handoff.md)
> อ่านสถานะ runtime/activation ล่าสุดก่อนนำ backlog ด้านล่างไปทำซ้ำ

> งานต่อเนื่องระบบขาย/Portal/RSSI/OTA วันที่ 22 ต.ค. 2026:
> [รายงานตรวจรับ Platform](06-platform-commerce-acceptance.md) และ
> [การศึกษาราคาในไทย](05-thai-pricing-research.html)
> แยกผล local, production และการตั้งค่ารับเงินจริงไว้ชัดเจน

> อัปเดต 2026-10-22: Dashboard Phase 2 deploy production แล้ว รวม Seat แยกต่อบอร์ด
> ดู [ผลการตรวจรับและวิธีทดสอบ](04-phase2-acceptance.md) สำหรับ contracts,
> automated checks และ screenshot artifacts เอกสารด้านล่างเป็น design review
> ตั้งต้น; สถานะและการตัดสินใจล่าสุดอยู่ในรายงานตรวจรับ การทดสอบ OTA และ hardware soak
> เป็นการตรวจรับแยกจาก automated checks และการ deploy dashboard

operator ส่ง mockup 3 ภาพ (ตาราง "Active Services", ชุด component IoT สีเข้ม,
หน้า admin "Aura Store") และขอคำแนะนำว่าควรเอาส่วนไหนมาใช้กับ dashboard ของ
แพลตฟอร์มนี้ รายงานเต็ม 3 ฉบับอยู่ในโฟลเดอร์นี้:

| ไฟล์ | มุมมอง | ใช้ skill |
|---|---|---|
| [01-ux-ia.md](01-ux-ia.md) | UX / information architecture, backlog 12 ข้อ | ui-ux-pro-max, impeccable |
| [02-frontend-design-spec.md](02-frontend-design-spec.md) | design tokens, component HTML/CSS, แผน migration 9 ขั้นของ `admin-api/dashboard/index.html` | frontend-design, dataviz |
| [03-backend-api-plan.md](03-backend-api-plan.md) | ข้อมูลที่มี/ขาด, time series, events, alerts, ตัว resolve ลิงก์ Google Maps (พร้อมโค้ด Go + test) | senior-backend |

## ข้อสรุปร่วมของทั้งสามมุมมอง

1. **ไม่ต้องรื้อ** โครงปัจจุบัน (sidebar ซ้าย, dark theme, ระบบ pill เดียว, device editor
   ที่ a11y ดีอยู่แล้ว, OTA picker ที่รู้ชิป) ตรงกับ mockup 3 อยู่แล้ว ให้ปรับเป็น
   *refinement* ด้วย token ชุดใหม่
2. **จุดเจ็บที่สุด**: ตาราง Devices 13 คอลัมน์ ซ้อน 4–5 บรรทัดต่อช่อง, assign device
   ผ่าน `prompt()` พิมพ์ MAC, Copy conn ผ่าน `alert()` — ใช้จากมือถือหน้าบ้านลูกค้าไม่ได้
3. **สิ่งที่ควรหยิบจาก mockup**: แถวแบบ "Active Services" (ชื่อ + MAC สองบรรทัด + badge
   ชิป, pill สถานะเดียว, แถบ seats ใช้/ขายได้, ตำแหน่ง city/จังหวัด + ไอคอนแผนที่,
   FW + OTA pill, เมนู ⋯), KPI 4 ใบบน Overview, รายการ "ต้องดู" (attention) แทน
   กระดิ่งแจ้งเตือน, drawer ต่อ device สำหรับข้อมูลอ่อนไหว/ละเอียด
4. **สิ่งที่ไม่ควรหยิบตอนนี้**: เลข NO ขนาดใหญ่, สีแบรนด์ 5 สีย้อม KPI (ชนกับสี
   warn/bad), sparkline/delta pill/กราฟรายวัน (ยังไม่มี history ใน API และบอร์ด 2–5
   ตัวเป็น noise), Top products, avatar, gradient ทั้งแถว
5. **สีจาก mockup ใช้ตรง ๆ ไม่ได้ทั้งหมด**: `#727C94` บน `#222631` ได้ 3.6:1 ไม่ผ่าน AA →
   ใช้ `#868FA3` สำหรับข้อความ, สีแบรนด์เป็น *fill* และใช้ ink ที่ยกความสว่างสำหรับ
   ข้อความ (ตารางอัตราส่วนอยู่ใน 02 §1)
6. **ภาษาไทย**: line-height ≥ 1.5–1.6, ไม่ใช้ตัวพิมพ์ใหญ่/letter-spacing กับหัวตาราง,
   ฟอนต์ `Leelawadee UI` / `Noto Sans Thai` / `IBM Plex Sans Thai` + mono สำหรับ MAC/IP
7. **Google Maps** (คำขอของ operator): วางลิงก์ `maps.app.goo.gl/...` ในหน้าแก้ไข device →
   admin-api ตาม redirect ฝั่งเซิร์ฟเวอร์ (allowlist โดเมน google, ไม่อ่าน body, 6 วินาที,
   สูงสุด 5 hop) → เติม lat/lon; ช่องตำแหน่งในตารางเปิด `https://www.google.com/maps?q=lat,lon`;
   เพิ่ม `country_code` จาก geo-IP ถ้าอยากได้ธง (03 §4 มีโค้ด Go และ test พร้อม)

## ลำดับงานที่แนะนำ (รวมจาก backlog ทั้งสาม)

| ลำดับ | งาน | ขนาด | ต้องมี backend ก่อน? |
|---|---|---|---|
| 1 | Google Maps resolver + `maps_url` + ลิงก์ออก Google Maps | S | ใช่ (03 §4) — ทำได้ทันที |
| 2 | Design tokens + base/pill/focus/sprite (02 ขั้น 1–2) | S | ไม่ |
| 3 | แถว Devices แบบ card: 7 คอลัมน์, pill สุขภาพเดียว, seats gauge, geo cell, เมนู ⋯ (01 #2, 02 ขั้น 5a–5b) | M | `deviceView.status` (03 #2, S) |
| 4 | Assign-device picker แทน `prompt()` (01 #1) | M | ไม่ (ใช้ `/api/devices` เดิม) |
| 5 | Overview KPI ใหม่ + attention list (01 #4–5, 02 ขั้น 4) | M | `connected_now`, `throughput_bps` (03 #2) |
| 6 | Device drawer `#/devices/<mac>` (01 #3) | L | ไม่ |
| 7 | Auto-refresh แบบ patch แถว (01 #6), mobile pass (01 #10, 02 ขั้น 9) | M | ไม่ |
| 8 | Poller + `metrics_*` + `GET /api/metrics` → sparkline/กราฟ 30 วัน (03 #3–4, 02 ขั้น 7) | M+M | ใช่ |
| 9 | Events + Recent Activity, alerts (03 #5, #7) | M+M | ใช่ |
| 10 | Health score ผสม availability/dial success (03 #6) | S | หลังข้อ 8 |

หมายเหตุ: ข้อ 1 ทำแล้วใน branch ถัดไป (ดู git log); ข้อ 2–7 ใช้ endpoint ที่มีอยู่เป็นหลัก
จึงเริ่มได้โดยไม่รอ time series.
