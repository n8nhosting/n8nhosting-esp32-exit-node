# รีวิว UX / Information Architecture — Admin Dashboard (ESP32 Exit-Node Proxy)

วันที่: 2026-10-21 · ผู้รีวิว: UX research / IA (read-only, ไม่แก้ไฟล์ใน repo)

> 2026-10-22: เจ้าของเปลี่ยนจาก Seat ตาม exit IP เป็น **Seat แยกต่อบอร์ด/MAC**
> แม้บอร์ดใช้ public IP เดียวกัน; IP group เหลือเป็นข้อมูลประกอบเท่านั้น
> และ Dedicated สงวนบอร์ดเดียว ไม่ใช่ทั้ง IP กฎ Shared ปัจจุบันคือ 0 seats เมื่อยังไม่มีค่าวัดที่เป็นบวก
> แล้ว `min(3, max(1, floor(capacity_mbps/5)))` ดู
> [per-board seat contract](../superpowers/specs/2026-10-22-per-board-seats-design.md)
> สูตร `min(3, floor(Mbps/5))` และข้อความเรื่อง IP group ด้านล่างเป็นบริบทย้อนหลัง
> รายละเอียดบอร์ดที่ deploy แล้วแสดงครบพร้อมกันใน drawer ดู [ผลตรวจรับ Phase 2](04-phase2-acceptance.md)

> วิธีการ (ตามแนว `impeccable critique`): **single-context review — อ่านจาก source เท่านั้น**
> (`admin-api/dashboard/index.html`, `admin-api/*.go`, `broker/registry.go`, `broker/main.go`,
> `docs/superpowers/specs/2026-10-20-proxy-seller-platform-design.md`, `README.md`,
> `docs/PLATFORM.md`) ไม่ได้รัน browser/detector จึงไม่มี screenshot evidence
> ส่วน mockup ทั้ง 3 ภาพประเมินจากคำบรรยายที่ได้รับ ไม่ได้เห็นภาพจริง

---

## 0. สรุปสั้น (TL;DR)

1. โครงหลักปัจจุบัน **ถูกทางอยู่แล้ว**: มี left sidebar, dark theme, semantic colour tokens,
   status pill ระบบเดียว, device editor ที่ a11y ดีมาก, OTA picker ที่เลือก image ตาม chip เอง
   สิ่งที่ mockup 3 (Aura Store) เสนอด้าน shell จึง "ปรับ" ไม่ใช่ "รื้อ"
2. ปัญหาใหญ่สุดคือ **Devices table 13 คอลัมน์ + stacked content ใน cell** และ
   **flow ที่ใช้ `prompt()/alert()/confirm()`** (assign device ต้องพิมพ์ MAC เอง, Copy conn เป็น alert)
   สองอย่างนี้ทำให้ใช้งานจากมือถือหน้าบ้านลูกค้าแทบไม่ได้
3. จาก mockup ควรหยิบมา **ตอนนี้**: (a) row = "Active Services" style (ชื่อ 2 บรรทัด + chip icon,
   status pill, seats gauge, location + map link, FW), (b) KPI 4 ใบที่ตอบคำถามคนขาย proxy
   (devices online, **seats sold / sellable**, active users + expiring, GB เดือนนี้),
   (c) Attention / alerts (ไอเดีย alarm tile + bell badge) ซึ่ง **คำนวณได้จาก API ปัจจุบันทั้งหมด**,
   (d) search กรองในหน้า, (e) detail drawer แทน row ที่อัดข้อมูล
4. **ข้าม / เลื่อน**: sparkline + delta pill (ไม่มี history ใน API; กับบอร์ด 2–5 ตัวจะเป็น noise),
   area chart รายวัน (ไม่มี daily rollup), 5 เฉดสีแต้ม KPI card (สีต้องสื่อสถานะ ไม่ใช่ตกแต่ง),
   Top products, avatar, NO numerals
5. "CPU gauge" ของเรา = **seats ที่ขายแล้ว / seats สูงสุด** (`active_users/max_users`) + `capacity_mbps`
   ตอนนี้; live load = `active_dials/max_streams` ต้องให้ broker เปิด `max_streams` ใน `/nodes`;
   free heap ยังไม่มีใน protocol (HELLO/PING ไม่ส่ง) → รอ firmware
6. Google Maps: operator ต้องการลิงก์ "เข้า-ออก" → เปิดพิกัดใน Google Maps จากแถว/drawer
   และ **วาง URL Google Maps แล้ว parse lat/lon เข้าช่องพิกัดได้** (ตอนนี้เป็น OSM และต้องพิมพ์ตัวเลขเอง)
7. คะแนน Nielsen heuristics (ประเมินจาก source): **24 / 40** — พื้นฐานดี แต่ visibility,
   recognition-over-recall และ flexibility ต่ำ

---

## 1. Dashboard ปัจจุบันทำอะไรได้ดี / แย่

อ้างอิง `admin-api/dashboard/index.html` (641 บรรทัด, vanilla JS, ไม่มี framework)

### 1.1 ทำได้ดี (ควรรักษาไว้)

| ส่วน | สิ่งที่ดี |
|---|---|
| **Shell / navigation** | เป็น **left sidebar 220px อยู่แล้ว** (`#app{grid-template-columns:220px 1fr}`) ไม่ใช่ top nav — โครงเดียวกับ mockup 3; ที่ ≤720px sidebar กลายเป็นแถวเลื่อนแนวนอน (ยังใช้ได้ แต่ไม่ใช่ bottom tab) |
| **Design tokens** | มี CSS vars ครบ: `--bg #0b0e14 / --panel #141924 / --panel2 / --line / --fg / --muted #8c99ad / --accent #4b9fff / --ok / --warn / --bad`; `--muted` บน `--panel` ≈ **6.1:1** ผ่าน WCAG AA |
| **Status pill** | ระบบเดียว (`.pill.ok/.warn/.bad/.off`) ใช้ร่วมกันทั้ง Users, Devices, OTA; ลำดับความสำคัญใน Users ถูก (expired > over quota > disabled/active) |
| **Users table** | คอลัมน์ Device แสดง `name (MAC) · :port` ตามที่ spec §6.1 ต้องการ; `Copy conn` ให้ connection string พร้อมส่งลูกค้า; error จาก admission rule แสดง `reason` ("shared IP full (3/3)") ชัด |
| **Devices table — ข้อมูล** | ครบและถูกต้องตาม domain: seats `n/max` (แดงเมื่อเต็ม), pill `unmeasured`, pill `slot not on broker` + tooltip อธิบายวิธีซ่อม, IP-group badge เมื่อ 2 บอร์ดอยู่หลัง IP เดียว, chip pill (esp32s3/c3), OTA state machine (queued → updating → updated / rolled back) พร้อม tooltip และปุ่ม cancel |
| **Measure** | ปุ่มใน cell เปลี่ยนเป็น "Measuring…" + disabled, error inline (`role=alert`), disabled เมื่อยังไม่มี slot (ตรง rule ใน `measure.go`) |
| **Device editor modal** | a11y ดีมาก: `role=dialog aria-modal`, `#app.inert`, focus trap, Esc ปิด, คืน focus ไปปุ่ม Edit เดิมแม้ตารางถูก re-render; ส่งเฉพาะ field ที่เปลี่ยน; slot → แสดง port สด; `validity.badInput` guard; checkbox "Clear stored WiFi password" แยกจาก "blank = keep" |
| **OTA modal** | version picker มี tag (latest/testing/current/protocol/chips); เลือก image ตาม chip ของบอร์ดอัตโนมัติ และ fail ชัดเมื่อ release ไม่มี image ของ chip นั้น; custom URL บังคับ https; banner อธิบาย auto-rollback |
| **Security hygiene ใน UI** | ไม่มี inline handler (ใช้ `data-act/data-user/data-mac`), `esc()` ทุกค่า, WiFi reveal 10 วินาทีแล้วซ่อน, generated password แสดงครั้งเดียว |
| **i18n** | มี map `i18n` + `T()` แล้ว (ใช้ในหน้า Devices) — รองรับการทำภาษาไทยภายหลังตาม spec §6.2.16 |

### 1.2 ทำได้แย่ (ระบุตาม section/element)

**Overview (`viewOverview`)**
- 6 stat cards เป็น **counter ดิบ ไม่มีบริบท**: ไม่มี trend, ไม่มี link ไปหน้าที่เกี่ยวข้อง, ไม่มี threshold สี
- "Idle connections" = `broker.idle_nodes` เป็นแนวคิด legacy pool (one-DIAL-per-connection) — ยุค mux1 ค่านี้แทบไม่มีความหมายกับ operator
- "Dials (ok / fail)" นับสะสม **ตั้งแต่ broker start** ไม่มี time window → อ่านแล้วตัดสินใจอะไรไม่ได้
- **ไม่มี KPI ของคนขาย proxy**: seats ที่ขายได้/ขายแล้วทั้ง fleet, users ใกล้หมดอายุ, บอร์ด offline ที่มี user ผูกอยู่
- ไม่มี "สิ่งที่ต้องดูตอนนี้" (attention list) แม้ข้อมูลทุกอย่างมีใน `/api/devices` + `/api/users`
- Refresh เป็นปุ่มกดเอง ไม่มี auto-refresh / polling เลย (ทั้งแอป)

**Users table (`viewUsers`)**
- **assign device ใช้ `prompt()` ให้พิมพ์ MAC เอง** (hint เป็นรายการ MAC ล้วน) — pure recall; บนมือถือเกือบเป็นไปไม่ได้; ไม่เห็น seats ว่าง/capacity/online ของแต่ละบอร์ดก่อนเลือก → เจอ error "capacity" หลังพิมพ์เสร็จ
- `Copy conn` แสดงผลผ่าน `alert()`; `delUser` ใช้ `confirm()`; สร้าง user สำเร็จก็ `alert()`
- **5 ปุ่มต่อแถว** (Copy conn / Edit / Enable-Disable / +30d / Delete): Delete สีแดงอยู่ติดกับปุ่มใช้งานประจำ (ผิด `destructive-emphasis`), `+30d` ถูก hardcode
- Usage เป็นตัวเลข "1.23 / 50 GB" ไม่มี bar; Expires เป็น ISO date ไม่มี "อีก N วัน"/สีเตือน; Rate "—" สำหรับ 0 ไม่บอกว่าแปลว่า unlimited-bounded-by-capacity
- ไม่มี search / sort / filter; ไม่แสดง `note`; ไม่แสดง `created_at`
- Edit modal: "Expires in (days)" **นับจากวันนี้** (ไม่ใช่ต่อจากวันหมดอายุ — ต่างจากปุ่ม +30d ที่ต่อจากของเดิม) และ **ตั้งกลับเป็น never ไม่ได้**; ไม่มี unassign/assign ใน modal

**Devices table (`viewDevices`)**
- **13 คอลัมน์** (Name, Kind, Status, Users, WiFi, Exit IP, Location, Capacity, ISP, Uptime, Traffic, FW, actions) + cell ซ้อนหลายบรรทัด (Name cell มี name / MAC / chip pill / slot·port / mismatch pill = 4–5 บรรทัด) → เลื่อนแนวนอนแม้บน laptop 1366px; มือถือพัง (ไม่มี `overflow-x` wrapper → ทั้งหน้า scroll แนวนอน)
- **WiFi password reveal อยู่ใน list view** — ข้อมูลอ่อนไหวควรอยู่ใน detail เท่านั้น (ยิ่งบนมือถือหน้าบ้านลูกค้า)
- ปุ่ม Measure (ใช้เวลาสูงสุด 45 s) อยู่ใน cell ของตาราง ไม่มี progress นอกจากข้อความ
- Location: เมื่อมีพิกัดแสดง **เลข lat/lon ดิบ** เป็นลิงก์ (OSM) แทนชื่อเมือง — ชื่อเมือง (`geo.city`) หายไปทันทีที่ใส่พิกัด; ISP แยกคอลัมน์ทั้งที่เป็นข้อมูลชุดเดียวกับ location
- Traffic ↑↓ สะสมตั้งแต่ broker start; Uptime ไม่มีค่า **last seen** เมื่อ offline (API มี `last_seen`)
- **4 ปุ่มต่อแถว** Edit / Update FW / Block / Forget — Forget (ลบถาวรจาก broker) น้ำหนักเท่า Edit, ไม่มี row expand หรือ detail view
- Status มีแค่ online/offline/blocked — ไม่รวม "ต้องสนใจ" (unmeasured + มี slot, slot mismatch, OTA rolled back, offline แต่มี user ผูก)

**Device editor modal**
- lat/lon เป็นช่องตัวเลขเปล่า 2 ช่อง ไม่มีปุ่ม "ใช้ค่าจาก geo-IP" แม้ API ส่ง `geo.lat/geo.lon` มาแล้ว; ลบพิกัดไม่ได้ (hint บอกเอง `coordHint`); ไม่มี map preview/ลิงก์ในโมดอล
- เปลี่ยน Kind shared↔dedicated มีผลข้างเคียงสำคัญ (pool flag บน broker, admission rule, max_users=1) แต่ไม่มีคำอธิบายใน UI
- error เป็น raw string จาก API ("slot taken", "broker: …", "could not save device") ไม่มีทางแก้แนะนำ

**OTA**
- หลัง push ต้องกด Refresh เองเพื่อเห็น queued → updating → updated/rolled back (ไม่มี polling)
- "Update all online…" ไม่ preview รายชื่อบอร์ด + chip + FW ปัจจุบัน ก่อนยืนยัน

**Settings**
- เป็นแค่ 3 URL ใน `localStorage` + ย่อหน้าคู่มือ — ชื่อ "Settings" สื่อเกินจริง

**ข้ามทุกหน้า (cross-cutting)**
- ไม่มี toast/success feedback (บันทึกสำเร็จเงียบ หรือ `alert('Saved')`)
- Loading เป็นข้อความ "Loading…" ไม่มี skeleton; re-render ทั้งตารางทุกครั้ง
- ⚡ emoji เป็น icon (ขัด rule "SVG icons, no emoji")
- `.pill` font `.74rem` ≈ **11.8px** (< 12px floor), `th .76rem` ≈ 12.2px uppercase
- `.row-actions button` padding 5px/10px font .78rem → สูง ≈ 26px (**< 44px touch target**)
- ไม่มี URL routing (hash) → refresh หน้ากลับ Overview, deep link ไม่ได้
- i18n ครึ่งทาง: หัวตาราง Users, ปุ่ม, modal user เป็น English hardcode

### 1.3 Nielsen heuristics (0–4, ประเมินจาก source)

| # | Heuristic | คะแนน | ประเด็นหลัก |
|---|---|---|---|
| 1 | Visibility of system status | 2 | ไม่มี auto-refresh; OTA/online state ต้องกด Refresh; Measure มี state ดี |
| 2 | Match system / real world | 3 | ศัพท์ตรง domain ของ operator; แต่ "Idle connections", "Dials" เป็น broker-internal |
| 3 | User control & freedom | 2 | `prompt/alert/confirm`; ไม่มี undo; ตั้ง expiry กลับเป็น never ไม่ได้ |
| 4 | Consistency & standards | 3 | pill/ปุ่ม/โมดอลสอดคล้อง; i18n ใช้ครึ่งเดียว |
| 5 | Error prevention | 3 | admission rule บังคับที่ server และบอก reason; slot/https validation ดี; แต่พิมพ์ MAC เอง, Delete/Forget ติดปุ่มปกติ |
| 6 | Recognition over recall | 2 | assign ด้วย MAC; kind/slot ไม่มีคำอธิบาย; location เป็นเลขพิกัด |
| 7 | Flexibility & efficiency | 2 | ไม่มี search/sort/filter, keyboard shortcut, bulk select; +30d ค่าเดียว |
| 8 | Aesthetic & minimalist | 2 | Devices 13 คอลัมน์ + cell ซ้อน; Overview counters ไร้บริบท |
| 9 | Error recovery | 3 | capacity reason + measure error inline ดี; raw "broker: …" ไม่มีทางแก้ |
| 10 | Help & documentation | 2 | tooltip บน pill, banner OTA ดี; kind/slot/measure ไม่มี inline help |
| | **รวม** | **24 / 40** | พื้นฐานดี ต้องแก้จุดอ่อนด้าน visibility, recall, density |

---

## 2. Mapping: องค์ประกอบใน mockup → product นี้

ลำดับความสำคัญ: **now** (ทำได้ด้วย API ปัจจุบัน) · **Phase C+** (ต้อง plans/orders หรือ data ใหม่) · **skip**

### 2.1 Mockup 1 — "Active Services" table

| องค์ประกอบ | เหมาะกับเราไหม | จะแสดงอะไร (field) | ลำดับ |
|---|---|---|---|
| NO 01–05 ตัวเลขใหญ่จาง | ไม่ | ลำดับเปลี่ยนตาม sort; identity ของเราคือชื่อ+MAC | **skip** |
| Service name 2 บรรทัด + OS icon | **ใช่ — ดีมาก** | บรรทัด 1 `name` (fallback MAC), บรรทัด 2 `mac` mono; icon = chip (`chip` esp32s3 / esp32c3) เป็น SVG badge แทน pill | **now** |
| Location: ธงกลม + "Frankfurt, Germany" | ใช่ แต่ปรับ | `geo.city`, `geo.region` (จังหวัด), subtitle `geo.isp`; ธง: ip-api ถูกเรียกด้วย `fields=…country…` **ไม่มี `countryCode`** → ต้องเพิ่ม field เล็กๆ ใน `geo.go`; ทุกบอร์ดเป็นไทย ธงจึงมีค่าน้อย ใช้ icon หมุดแทนก็ได้ | **now** (S) |
| → ลิงก์แผนที่ | **ใช่ — operator ขอ Google Maps เข้า-ออก** | ออก: `https://www.google.com/maps?q=<lat>,<lon>` (ใช้ `lat/lon` ที่ operator ใส่ ถ้าไม่มีใช้ `geo.lat/geo.lon` และบอกว่า "โดยประมาณจาก IP"); เข้า: ช่อง "วางลิงก์ Google Maps / พิกัด" ใน editor แล้ว parse `@lat,lon` หรือ `q=lat,lon` → เติม lat/lon อัตโนมัติ; ปัจจุบันเป็น OSM และ spec §6.2.5 บอก OSM — **บันทึกการเปลี่ยนเป็น Google Maps ไว้ใน spec** | **now** (S) |
| IP monospace | ใช่ | `remote_ip` + badge IP group เมื่อ `ip_group_size>1` (มีแล้ว) | **now** |
| DUE DATE "14 Oct 2027" | ใช่ แต่คนละตาราง | บอร์ดไม่มี due date → ใช้ใน **Users**: `expires_at` แสดง "14 ต.ค. 2027 · อีก 12 วัน" สี amber ≤7 วัน, แดงเมื่อหมด; สำหรับ Devices ค่าที่มีความหมายใกล้กันคือ **ความสดของ capacity** (`capacity_measured_at` → "วัดเมื่อ 3 วันก่อน") และ `last_seen` เมื่อ offline | **now** (Users) · Phase C: `subscription.period_end` |
| CPU 10-segment bar + 80% | **ใช่ — แต่ "CPU" ของเราคือ seats** | **ตอนนี้**: segmented bar `active_users / max_users` (สูงสุด 3 ช่อง, dedicated = 1 ช่อง) + ตัวเลข `capacity_mbps` ("9.8 Mbps · 2/3 seats"); สีเทาเมื่อ `max_users=0` (unmeasured) พร้อม label; **ต่อไป**: live load `active_dials / max_streams` — `/nodes` ยังไม่ส่ง `max_streams` (อยู่ใน muxSession) ต้องให้ broker เปิด; **free heap / RSSI: ไม่มีใน HELLO/PING** → ต้องแก้ firmware ก่อน (Phase E) | **now** (seats) · Phase E (streams/heap) |
| STATUS pill Active / Paused / Inactive | ใช่ (มีแล้ว) | รวมเป็น **health pill ตัวเดียวต่อบอร์ด**: Online · Updating (OTA sent) · Attention (slot mismatch / unmeasured+slot / rolled back) · Offline (+ "last seen 2 h") · Blocked — สี + icon + ข้อความ ไม่ใช้สีอย่างเดียว | **now** |
| แถวเป็น card แยก + tint สีสถานะที่ขอบขวา | ใช่แบบเบา | ใช้ **เส้นซ้าย 3px สีสถานะ** แทน gradient ทั้งแถว (gradient ลด contrast ของตัวอักษร); card ต่อแถวกินพื้นที่แนวตั้ง — ใช้กับ Overview mini-table (≤5 แถว) ไม่ใช้กับตารางเต็ม | **now** (S) |
| Header "● Active Services · 3 Servers • 1 Servers" | ใช่ | "Devices · 3 online · 1 offline · 1 attention" จาก `/api/devices` | **now** |

### 2.2 Mockup 2 — Dark IoT component sheet

| องค์ประกอบ | เหมาะกับเราไหม | จะแสดงอะไร (field) | ลำดับ |
|---|---|---|---|
| Colour tokens (#455DDC blue, #F73463, #9646EF, #FF862E, #43D1A7; neutrals #222631/#2F374C/#727C94/#A0A9C0) | **บางส่วน** | ของเรามี token ครบแล้ว; รับ "1 accent + 4 semantic" ไม่รับ 5 brand hues; **ระวัง**: `#727C94` บน `#222631` ≈ **3.6:1 ไม่ผ่าน AA** สำหรับตัวอักษรปกติ (ใช้ได้เฉพาะ ≥18px/bold) ส่วน `#A0A9C0` ≈ 6.4:1 ผ่าน → ถ้าจะยืมเฉดใช้ `#A0A9C0` เป็น muted | **now** (จัด token ให้เป็นทางการ) · skip multi-hue |
| Stat tile: icon, big number, unit, delta pill "+10%" | ใช่ ถ้ามี history | `/api/overview` ไม่มี history → **needs new data**: ตาราง snapshot รายชั่วโมง (users active, devices online, month GB, bytes) ใน SQLite; delta มีความหมายกับ **GB เดือนนี้** และ **active users** เท่านั้น (delta ของ "devices online 2→3" คือ noise) | Phase C+ (E) |
| Tile + sparkline | ใช่ ถ้ามี history | เหมือนข้างบน; กับ fleet 2–5 บอร์ด sparkline ของ count แทบไม่มีข้อมูล — ใช้กับ traffic เท่านั้น | Phase C+ (E) |
| Donut / radial gauge "94" | **ใช่ 1 ตัว** | **Fleet capacity utilisation** = Σ`active_users` / Σ`max_users` (seats ที่ขายแล้ว / ขายได้) — นี่คือ "inventory" ของธุรกิจ proxy; แสดงตัวเลขเสมอ ไม่พึ่งวงแหวนอย่างเดียว | **now** (S) |
| Horizontal segmented bar | ใช่ | seats ต่อบอร์ด (≤3 ช่อง), quota ต่อ user (`usage_gb/quota_gb`), capacity vs rate ของ user | **now** |
| Stacked progress bar + legend | ใช่ | Fleet seats: dedicated taken / shared taken / free / unmeasured (ยังขายไม่ได้) | **now** (S) |
| Small area chart แกนวัน | ใช่ ถ้ามี data | observer เก็บ bytes **ต่อ user ต่อเดือน** เท่านั้น ไม่มี daily series → needs daily rollup table | Phase C+ (E) |
| คู่ tile มี footer "Connection rate 92%" | **ใช่ — มี data แล้ว** | dial success = `dials_ok / (dials_ok+dials_err+dials_no_node+dials_pinned_offline)` จาก broker `/status`; แสดงพร้อม `dials_pinned_offline` ("ลูกค้าโดนปฏิเสธเพราะบอร์ด offline") ซึ่งเป็นสัญญาณธุรกิจจริง; **ควรเพิ่ม window** (reset counter รายวันหรือเก็บ snapshot) | **now** (ค่าสะสม) · Phase E (window) |
| Alarm tile + red count badge | **ใช่ — สำคัญสุดในชุดนี้** | "Attention" นับจาก client-side ได้ทันที: บอร์ด offline ที่มี `users.length>0`; `slot_mismatch`; shared+slot แต่ `capacity_mbps=0`; `ota_status=failed`; users `expires_at` ≤3 วัน; users over quota; dedicated board ที่มี 0 user (ขายได้แต่ว่าง) | **now** (M) |

### 2.3 Mockup 3 — "Aura Store" admin

| องค์ประกอบ | เหมาะกับเราไหม | จะแสดงอะไร (field) | ลำดับ |
|---|---|---|---|
| Left sidebar: logo, nav, avatar ล่าง | ใช่ (มีแล้ว) | จัดกลุ่ม nav: **Operate** (Overview, Devices, Users, Alerts) · **Sell** (Plans, Orders, Approvals — Phase C) · **System** (Email log, Settings); badge ตัวเลขที่ Alerts/Approvals; avatar → skip (operator คนเดียว login ด้วย token) แสดง "admin · <host>" + Sign out แยกส่วนล่าง | **now** (โครง) · Phase C (รายการ Sell) |
| Top bar + search | **ใช่** | search กรองในหน้า (client-side พอ: users ไม่กี่สิบ, devices ≤30): username, note, device name, MAC, IP, SSID, city; `/` focus ช่องค้นหา | **now** (S) |
| Notifications + bell badge แดง | ใช่ | = Attention drawer (รายการเดียวกับ alarm tile) เปิดจากทุกหน้า; push จริง (email/LINE Notify) เมื่อ device offline > N นาที อยู่ใน spec Phase E | **now** (drawer) · Phase E (push) |
| Primary CTA "Add New Product" | ใช่ | CTA ตามหน้า: Users → "+ New user" (มีแล้ว), Devices → "Flash a new board ↗" (ลิงก์ flasher), Plans → "+ Plan" | **now** |
| 4 KPI cards แต้มสีน้ำเงิน/ส้ม/ม่วง/เขียว + sparkline + % | ใช่ แต่เปลี่ยนเนื้อหา | KPI ที่ตอบคำถามคนขาย proxy: **(1)** Devices online `nodes_online/nodes_total` + "N attention", **(2)** Seats sold / sellable (stacked bar) — ไม่มีใน `/api/overview` แต่คำนวณจาก `/api/devices` ได้, **(3)** Active users `users.active` + "k expiring ≤7d" (ต้องคำนวณจาก `/api/users`), **(4)** Traffic this month `month_gb` (+ วันนี้เมื่อมี rollup); Phase C เพิ่ม **MRR (THB)** และ **Orders pending / Approvals**; **Aggregate Mbps live**: ไม่มีใน API (observer ให้ bytes เป็นก้อน) → คำนวณจาก delta ระหว่าง poll ได้ใน Phase E; สีแต้ม 4 เฉด → **skip** (สีสถานะเท่านั้น) | **now** (4 ใบ) · Phase C (MRR/orders) |
| "Sales Overview" area chart + legend + date range | ใช่ใน Phase C | ต้อง orders + daily rollup; ก่อนนั้นไม่มีข้อมูลพอ | Phase C+ |
| Recent Activity feed | **ใช่** | ต้องมี `events`/`audit` table (spec §7 มี `audit` อยู่แล้ว); แหล่งที่มีตอนนี้: `ota_at`+`ota_status`, `first_seen`, `last_seen`, `created_at` ของ user, `capacity_measured_at`, log reveal-wifi (log เท่านั้น) → **now-lite**: feed สังเคราะห์จาก timestamp เหล่านี้ (10 รายการล่าสุด); **จริง**: บันทึก event ที่ admin-api เมื่อ: device online/offline เปลี่ยน, OTA state, user created/assigned/extended, measure, reveal-wifi, payment | now-lite (S) · Phase C (audit table) |
| Order Management table + status pill + "View/Manage" | Phase C ตามเนื้อหา; **pattern ใช้ตอนนี้** | แทน 4–5 ปุ่มต่อแถวด้วย **row click → drawer** + เมนู ⋯ (kebab) ที่มี action รอง และ destructive แยกส่วนล่างสีแดง | **now** (pattern) · Phase C (orders) |
| Top Selling Products + thumbnail | ไม่ | ไม่มี analog ที่มีค่า ("top devices by traffic" ไม่ช่วยตัดสินใจ) | **skip** |
| Date-range picker | ภายหลัง | ต้องมี time-series ก่อน | Phase C+ |

---

## 3. Information Architecture ที่เสนอ

### 3.1 Shell

- **Desktop (≥ 900px)**: left sidebar คงเดิม (จัดกลุ่ม Operate / Sell / System), top bar ต่อหน้า =
  ชื่อหน้า + สรุปสั้น ("3 online · 1 attention") + search + bell (Attention) + CTA ของหน้า
- **Mobile (< 720px)**: **bottom tab bar ≤ 5 รายการ** (Overview · Devices · Users · Alerts · More) icon + label
  แทน sidebar ที่กลายเป็นแถวเลื่อน; search อยู่ในหน้า; drawer เปิดเป็น full-screen sheet
- **URL hash routing** `#/devices/<mac>` `#/users/<name>` เพื่อ deep link / refresh ไม่หลุด / แชร์ลิงก์ให้ตัวเองในมือถือ
- **Auto-refresh**: poll `/api/devices` ทุก 10 s เฉพาะหน้าที่เปิดและเมื่อ tab visible; **patch DOM ต่อแถว** (ไม่ re-render ทั้งตาราง) เพื่อไม่ทำลาย focus/reveal timer; แสดง "updated 3 s ago" ที่ header

### 3.2 หน้าและ above-the-fold

**Overview** (คำถาม: วันนี้มีอะไรต้องทำ / ขายได้อีกเท่าไร)
```
┌ Devices online  3/4 ┐┌ Seats sold 5/7 ┐┌ Active users 6 ┐┌ Traffic (month) 184 GB ┐
│ ● 1 attention       ││ ▇▇▇▇▇░░ ded/shared/free ││ 2 expiring ≤7d ││ dial success 98.7 %   │
└─────────────────────┘└────────────────┘└────────────────┘└───────────────────────┘
┌ Attention (3) ───────────────────────────┐ ┌ Devices (mini, "Active Services" style) ┐
│ ⚠ Board "Bang Na" offline 42 min · 2 users│ │ ● Bang Na  esp32s3  2/3 seats 9.8 Mbps  │
│ ⚠ Board "Rama 9" slot not on broker       │ │ ● Rama 9   esp32c3  1/1 seats 2.7 Mbps  │
│ ⚠ user earth expires in 2 days            │ │ ○ Ladprao  offline · last seen 42 min   │
└──────────────────────────────────────────┘ └─────────────────────────────────────────┘
  Broker: TLS · uptime 12d · ↑ 41 GB ↓ 143 GB · pinned-offline refusals 12   (บรรทัดเดียว)
```
- ลบ "Idle connections"; ย้าย Dials ไปเป็น "dial success %" ใน card 4 และรายละเอียดบรรทัด Broker
- Phase C: เพิ่มแถว KPI ที่ 2 (MRR THB, Orders pending, Approvals) และ chart รายได้/traffic เมื่อมี rollup

**Devices** — ตาราง **7 คอลัมน์** + drawer

| เก็บ (คอลัมน์) | เนื้อหา |
|---|---|
| Health | pill ตัวเดียว (Online / Updating / Attention / Offline + last seen / Blocked) + เส้นซ้ายสี |
| Device | `name` บรรทัด 1 · `mac` mono + chip badge บรรทัด 2 |
| Routing | `kind` + `Slot N · :port` (หรือ "no slot") |
| Seats / Capacity | segmented bar `active_users/max_users` + `capacity_mbps` + อายุการวัด ("3 d") หรือ pill **unmeasured** |
| Users | ชื่อผู้ใช้เป็น chip (≤3 ตัว — ตาม rule สูงสุด 3) |
| Location | `geo.city`, `geo.region` · `geo.isp` subtitle · icon แผนที่ (Google Maps) · badge IP group |
| FW | `firmware` + OTA pill |
| ⋯ | เมนู: Edit · Measure · Update FW · Block/Allow · ── · Forget (แดง) |

| ย้ายไป drawer / ลบจากแถว | เหตุผล |
|---|---|
| WiFi SSID + reveal | ข้อมูลอ่อนไหว; reveal เฉพาะใน drawer, 10 s เหมือนเดิม |
| Exit IP (เต็ม) | ซ้ำกับ IP group badge; อยู่ใน drawer ส่วน Routing (ยังค้นหาได้ด้วย search) |
| Uptime | drawer; ในแถวแสดงเฉพาะ last seen เมื่อ offline |
| Traffic ↑↓ | drawer (ค่าสะสมอ่านยาก) |
| ISP | รวมเป็น subtitle ของ Location |
| ปุ่ม Measure | drawer ส่วน Capacity (มี progress 0–45 s) และเมนู ⋯ |

**Device drawer** (คลิกแถว; มือถือ = full-screen sheet; URL `#/devices/<mac>`)
1. Header: name (แก้ inline), MAC, chip, health pill, kind, ปุ่ม "Update FW"
2. **Health**: online/last seen, uptime, `active_dials` (+`/max_streams` เมื่อ broker เปิด), dials ok/err, traffic ↑↓, FW + OTA history
3. **Capacity**: `capacity_mbps`, วัดเมื่อ, streams ขณะวัด (เตือน "lower bound" ถ้า >0), ปุ่ม Measure + progress, seats `n/max`, คำอธิบาย rule `min(3, floor(Mbps/5))` 1 บรรทัด
4. **Routing**: slot/port, kind (พร้อม helper text: dedicated = 1 ลูกค้า ไม่รับ pool), `slot_mismatch` → ปุ่ม "Re-push to broker", IP group (บอร์ดอื่นใน IP เดียวกัน)
5. **Location**: lat/lon + "ใช้ค่าจาก geo-IP (กรุงเทพ, AIS)" + ช่อง "วางลิงก์ Google Maps" + ปุ่ม "เปิดใน Google Maps" + ปุ่มลบพิกัด (ต้องเพิ่ม API รับ `null`)
6. **WiFi**: SSID (จากบอร์ด), password masked + Reveal 10 s, ตั้ง/ลบ
7. **Users bound**: รายการ + ปุ่ม "Assign user…" (picker) / unassign
8. Notes
9. **Danger zone** (แยกเส้น, สีแดง): Block / Forget (confirm แบบพิมพ์ชื่อหรือ dialog ที่อธิบายผล)

**Users** — ตาราง 7 คอลัมน์ + drawer

| คอลัมน์ | เนื้อหา |
|---|---|
| Username | `username` mono · `note` บรรทัด 2 (จาง) |
| Status | pill (active / disabled / expired / over quota) |
| Device | `name · :port` หรือ "pool :3128" (unbound) — คลิกไป device drawer |
| Usage | bar `usage_gb / quota_gb` + ตัวเลข; unlimited = bar จางพร้อม "∞" |
| Expires | วันที่ + "อีก N วัน" (amber ≤7, แดงหมด) หรือ "never" |
| Rate | `rate_mbps` หรือ "≤ capacity" เมื่อ 0 |
| ⋯ | Copy conn · Edit · Extend ▸ (7/30/90/กำหนดเอง) · Enable/Disable · ── · Delete |

**User drawer**: credentials (Copy conn เป็น toast ไม่ใช่ alert, Rotate แยกปุ่มชัด), plan fields (rate/quota/expiry พร้อมตั้ง never ได้), **Assign device picker**: รายการบอร์ด แสดง `name · online · 2/3 seats · 9.8 Mbps · :3133`, ตัวที่รับไม่ได้ **disabled พร้อมเหตุผลเดียวกับ `admit()`** ("shared IP full 3/3", "capacity not measured", "IP is dedicated to another user") — ย้าย rule มาแสดงก่อนกด แทนที่จะ error หลังพิมพ์

**Alerts** (หน้า + drawer จาก bell): รายการ attention พร้อมปุ่มแก้ตรงจุด (Re-push slot, Measure, Extend, เปิด device); Phase E: เก็บ event ลง DB, ส่ง email/LINE

**Settings**: URL ของ flasher/GOST UI/OTA (เดิม) + ภาษา (TH/EN) + ช่วง auto-refresh + (Phase C) Stripe/Resend status

**Phase C เพิ่ม**: Plans, Orders, Approvals (slip queue แบบ "Order Management" ของ mockup 3: ID, customer, amount, method, status pill, Review), Email log

---

## 4. UX risks ของสไตล์ mockup สำหรับผู้ใช้คนนี้

1. **Dark theme readability**
   - เฉด muted ของ mockup 2 (`#727C94` บน `#222631` ≈ 3.6:1) ไม่ผ่าน AA — ใช้กับ label/ค่าในตารางจะอ่านยากกลางแดดหน้าบ้านลูกค้า; ของเราปัจจุบัน (`#8c99ad` บน `#141924` ≈ 6.1:1) ดีกว่า อย่าถอยหลัง
   - row tint ตามสถานะ (mockup 1) ลด contrast ของตัวอักษรที่อยู่บน tint → ใช้เส้นซ้ายหรือ pill แทน
   - ตัวอักษร pill 11.8px และหัวตาราง 12px uppercase ของเราก็เล็กอยู่แล้ว; mockup 1 ใช้ตัวอักษรเล็กจางเยอะ (NO, header) — ตั้ง floor 12px/ตัวอักษรข้อมูล 14px
   - segment bar บางๆ และ donut ต้องมี **ตัวเลขกำกับเสมอ** และสถานะต้องมี icon/ข้อความ ไม่ใช่สีอย่างเดียว (red/green colour-blind)
2. **ภาษาไทย**
   - สระบน-ล่าง/วรรณยุกต์ของไทยสูงกว่า Latin: `line-height` ≥ 1.6, ห้ามใช้ `letter-spacing`/`text-transform: uppercase` กับ label ไทย (ของเดิมใช้กับ `th` และ `.stat .k`), ความสูงแถวต้องรองรับ
   - ฟอนต์: ปัจจุบัน `system-ui` → Windows ได้ Segoe UI + Leelawadee UI fallback, Android ได้ Noto Sans Thai — **อย่า**ใช้ Fira Sans/Inter เพียวๆ (ไม่มี glyph ไทย จะได้ fallback ปนกัน) → ใช้ `"IBM Plex Sans Thai"` หรือ `"Noto Sans Thai"` + `system-ui` และ mono สำหรับ MAC/IP/port; ข้อความไทยยาวกว่าอังกฤษ ~20–30% → ปุ่มใน ⋯ เมนู ไม่ใช่ 5 ปุ่มในแถว
3. **Density**
   - mockup 1 เป็น card ต่อแถว 5 แถวต่อจอ — fleet 30 slot ต้องมีโหมดหนาแน่น (ตารางปกติ) และใช้ card เฉพาะ Overview mini-table
   - 13 คอลัมน์ปัจจุบันคือปัญหาตรงข้าม → 7 คอลัมน์ + drawer ตามข้อ 3
   - KPI card 4 เฉดสี + sparkline กับ fleet 2–5 บอร์ด = ข้อมูลน้อยแต่ตกแต่งมาก (chart ควรมี ≥4 จุดก่อนวาด; ไม่งั้นแสดงเป็นตัวเลขอย่างเดียว)
4. **ใช้งานจากมือถือตอนติดตั้งบอร์ดหน้าบ้านลูกค้า** (scenario สำคัญสุดที่ mockup ทั้งสามไม่ได้ออกแบบมา)
   - งานที่ทำตอนนั้น: "บอร์ดใหม่โผล่มาหรือยัง → online ไหม → SSID ถูกไหม → ตั้งชื่อ/slot/kind → Measure → assign user → Copy conn ส่งลูกค้า" ต้องจบได้ในจอ 375px ด้วยนิ้ว: touch target ≥44px, auto-refresh (รอบอร์ด reboot/OTA), Measure มี progress 45 s, ไม่มี `prompt()` พิมพ์ MAC
   - WiFi reveal บนมือถือต่อหน้าลูกค้า = shoulder-surfing → reveal เฉพาะใน drawer, ยังคง 10 s, ไม่มีในแถว
   - สัญญาณมือถือไม่ดี: หน้าเดียวไฟล์เดียวแบบปัจจุบันเป็นข้อดี อย่าเพิ่ม chart library หนัก (วาด bar/donut ด้วย CSS/SVG inline)
   - ตารางต้องมี `overflow-x` wrapper หรือสลับเป็น card list ที่ <720px (ตอนนี้ทั้งหน้าเลื่อนแนวนอน)
5. **Auto-refresh vs. งานค้าง**: การ re-render ทั้งตารางจะปิด reveal, ดึง focus, ล้างข้อความ error — ต้อง patch ต่อแถวและหยุด poll ขณะ drawer/modal เปิด
6. **ความหมายของตัวเลข**: counters สะสมตั้งแต่ broker start (dials, traffic) ถ้าใส่ใน KPI card แบบ mockup จะดูเหมือน "วันนี้" → ต้องกำกับ window ชัด หรือเก็บ snapshot ก่อน
7. **Decoration ที่สื่อผิด**: 5 brand hues ของ mockup 2 (ชมพู/ม่วง/ส้ม) ชนกับ semantic (`--warn` ส้ม, `--bad` แดง) — ผู้ใช้จะอ่าน card สีส้มว่า "มีปัญหา"

---

## 5. Backlog (เรียงลำดับ, ≤12 รายการ)

| # | รายการ | Effort | คุณค่าต่อ operator (1 บรรทัด) |
|---|---|---|---|
| 1 | **Assign-device picker** แทน `prompt()`: รายการบอร์ด + online + seats + Mbps + port; ตัวที่รับไม่ได้ disabled พร้อม reason เดียวกับ `admit()` | M | ผูกลูกค้ากับบอร์ดจากมือถือได้ใน 2 แตะ ไม่ต้องพิมพ์ MAC ไม่เจอ error ทีหลัง |
| 2 | **Devices table ลดเหลือ 7 คอลัมน์ + ⋯ เมนู + `overflow-x` wrapper**; destructive แยกล่างเมนู | M | หน้าหลักอ่านได้บน laptop/มือถือ, ลดการกดผิด Forget/Delete |
| 3 | **Device drawer** (`#/devices/<mac>`): Health / Capacity+Measure / Routing / Location / WiFi / Users / Notes / Danger zone — ย้าย WiFi reveal, Measure, uptime, traffic มาที่นี่ | L | ข้อมูลครบโดยไม่อัดในแถว; ข้อมูลอ่อนไหวอยู่ใน detail เท่านั้น; deep link ได้ |
| 4 | **Health pill ตัวเดียว + Attention list** (offline มี user, slot mismatch, unmeasured+slot, OTA failed, expiring ≤3d, over quota) แสดงบน Overview + bell badge ทุกหน้า | M | เปิดแดชบอร์ดแล้วรู้ทันทีว่าต้องทำอะไร โดยไม่ต้องเพิ่ม API |
| 5 | **Overview KPI 4 ใบใหม่**: devices online+attention · seats sold/sellable (stacked bar) · active users + expiring ≤7d · GB เดือนนี้ + dial success %; ลบ Idle connections | S | เห็น inventory ที่ขายได้จริงและรายได้ที่กำลังจะหลุด (expiring) ในจอเดียว |
| 6 | **Auto-refresh 10 s + patch ต่อแถว + "updated Ns ago"**; หยุดเมื่อ tab ซ่อน/modal เปิด; OTA state เปลี่ยนเองโดยไม่กด Refresh | M | ตอนติดตั้ง/OTA ไม่ต้องกดรีเฟรชซ้ำ, เห็น rolled back ทันที |
| 7 | **Location ที่มีความหมาย + Google Maps เข้า-ออก**: แสดง city/จังหวัด · ISP เสมอ, icon เปิด Google Maps (lat/lon operator หรือ geo-IP พร้อมป้าย "โดยประมาณ"), ช่องวางลิงก์ Google Maps → parse พิกัด, ปุ่ม "ใช้ค่าจาก geo-IP", ลบพิกัดได้ (API รับ null); เพิ่ม `countryCode` ใน geo | S | รู้ว่าบอร์ดอยู่บ้านไหนและไปถึงได้; ใส่พิกัดใน 1 paste แทนพิมพ์ 2 ช่อง |
| 8 | **Users table**: usage bar, expires "อีก N วัน" สีเตือน, note บรรทัด 2, Extend ▸ 7/30/90/กำหนดเอง, ตั้ง expiry "never" ได้, Copy conn → toast + ช่องให้ copy ทีละบรรทัด | S | จัดการต่ออายุ/โควตาเร็วขึ้น, เลิกใช้ alert() |
| 9 | **Search กรองในหน้า** (users: username/note/device; devices: name/MAC/IP/SSID/city) + `/` shortcut + sort คอลัมน์หลัก | S | fleet 30 slot / ลูกค้าหลายสิบคนหาเจอใน 1 วินาที |
| 10 | **Mobile layout**: bottom tab bar ≤5, drawer เป็น full-screen sheet, touch target ≥44px, ปุ่มเมนู ⋯ แทน 4–5 ปุ่ม/แถว, line-height 1.6 สำหรับไทย, ฟอนต์ไทย (`IBM Plex Sans Thai`/`Noto Sans Thai` + mono) | M | ใช้งานจริงจากโทรศัพท์หน้าบ้านลูกค้า |
| 11 | **Activity feed (now-lite → audit)**: สังเคราะห์ 10 เหตุการณ์ล่าสุดจาก timestamp ที่มี (OTA, first/last seen, user created, measure); Phase C บันทึก `audit` table ใน admin-api (device state change, assign, extend, reveal-wifi, payment) | S → M | ย้อนดูได้ว่า "เมื่อคืนเกิดอะไรกับบอร์ด X" และเป็นฐานของ email/alert Phase E |
| 12 | **Trend data** (Phase E): snapshot รายชั่วโมง (online, active users, month GB, dials) + daily traffic rollup → ค่อยใส่ sparkline/delta/area chart; broker เปิด `max_streams` ใน `/nodes` เพื่อ gauge `active_dials/max_streams` | L | KPI มี context "เทียบเมื่อวาน" และ load จริงต่อบอร์ด — ทำหลังข้อ 1–10 เพราะข้อมูลต้องสะสมก่อน |

**ไม่ทำ**: NO numerals, 5 brand hues บน KPI card, Top products, avatar, row gradient tint, sparkline บน count ของ devices

---

## ภาคผนวก A — ช่องว่างข้อมูล (API/firmware ที่ต้องเพิ่มถ้าจะทำตาม mapping)

| ต้องการ | สถานะปัจจุบัน | ที่ต้องแก้ |
|---|---|---|
| ธงประเทศ | `geo.go` ขอ `country` (ชื่อ) ไม่ขอ `countryCode` | เพิ่ม field ใน query + struct |
| live load ต่อบอร์ด | `/nodes` มี `active_dials`, `mux_links` (admin-api `brokerNode` ไม่ได้ mirror `mux_links`) แต่ **ไม่มี `max_streams`** (อยู่ใน `muxSession`) | broker: ใส่ `max_streams` ใน registry node JSON |
| free heap / RSSI | HELLO ไม่มี, PING/PONG sid 0 ไม่มี payload | firmware ส่งใน PING หรือ HELLO periodic → broker → `/nodes` (Phase E) |
| trend / delta / sparkline | `/api/overview` ไม่มี history | ตาราง snapshot รายชั่วโมงใน SQLite (Phase E) |
| traffic รายวัน | observer เก็บ `usage_bytes` ต่อ user ต่อเดือน | daily rollup table |
| Activity feed จริง | มีแต่ log (reveal-wifi, measure) | `audit`/`events` table ตาม spec §7 |
| dial success แบบ window | counters สะสมตั้งแต่ start | snapshot หรือ reset รายวัน |
| ลบพิกัด | PUT รับเฉพาะตัวเลข; blank = keep | รับ `null` สำหรับ lat/lon |
| Aggregate Mbps live | ไม่มี | คำนวณ delta bytes ระหว่าง poll (client) หรือ observer rate (server) |
| MRR / orders / approvals | ไม่มี (Phase C ยังไม่สร้าง) | ตาม spec Phase C |

## ภาคผนวก B — เกณฑ์ที่ใช้ (จาก `ui-ux-pro-max`)

Accessibility contrast 4.5:1 · touch target 44×44 + 8px gap · table overflow-x หรือ card layout บนมือถือ ·
bottom nav ≤5 รายการ icon+label · destructive-emphasis + separation · confirmation + undo สำหรับ destructive ·
success feedback (toast) · loading skeleton >300 ms · colour-not-decorative-only (icon/text กำกับ) ·
number-tabular สำหรับคอลัมน์ตัวเลข · chart: gauge/bullet ต้องมีตัวเลขกำกับ, line chart ต้อง ≥4 จุด, empty-data state ·
no emoji as icons · deep-linking ทุกหน้าหลัก
