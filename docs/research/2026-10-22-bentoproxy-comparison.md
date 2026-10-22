# เทียบกับ BentoProxy — 22 ต.ค. 2026

อ่านจากซอร์สสาธารณะสามรีโป (โคลนแบบอ่านอย่างเดียว ไม่ได้รันโค้ดของเขา):
[bentoproxy](https://github.com/bentoproxy/bentoproxy) (orchestrator, Rust, AGPL-3.0),
[bentoproxy-esp32](https://github.com/bentoproxy/bentoproxy-esp32) (firmware, Rust บน ESP-IDF 5.5,
Cargo ระบุ MIT แต่รีโปไม่มีไฟล์ LICENSE) และ
[bentoproxy-protocol](https://github.com/bentoproxy/bentoproxy-protocol)
commit ล่าสุด 22 ต.ค. 2026 สถานะ README คือ alpha

**ห้ามคัดลอกโค้ด orchestrator มาใช้** เพราะ AGPL บังคับให้เปิดซอร์สบริการทั้งตัว
ใช้ได้เฉพาะแนวคิด และ firmware ของเขาเป็น Rust ส่วนของเราเป็น C จึงยกโค้ดมาตรง ๆ ไม่ได้อยู่แล้ว

## สรุป

- **BentoProxy ไม่รองรับ ESP32-C3** ชิปที่รองรับคือ ESP32, ESP32-S3 (แนะนำ) และ ESP32-C5
  (เพิ่ม 21–22 ต.ค. 2026) ตัวที่เร็วกว่า C3 ของเราคือ **C5** ซึ่งเป็นชิปคนละรุ่น
  ที่บอร์ด Seeed XIAO ESP32-C5 มี PSRAM 8 MB และ WiFi 6 แบบ 2.4/5 GHz
- **S3 ของเราเร็วกว่าของเขาราวสองเท่า** และเร็วกว่า C5 ของเขาด้วย
- แนวคิดที่ทำให้ C5 ของเขาเร็วขึ้นหลายข้อใช้กับ C3 ของเราได้ ดูแผนทดลองด้านล่าง

## ตัวเลขที่วัดได้

ตัวเลขของ BentoProxy มาจาก commit message `23972bb` (8 transfer พร้อมกัน
ตั้งโหนดให้รับเฉพาะ traffic ทดสอบ) ตัวเลขของเรามาจาก bench broker และการทดสอบของเจ้าของ
ทั้งสองฝั่งวัดต่างเงื่อนไข ใช้เปรียบเทียบได้แค่ระดับตัวเลข

| บอร์ด | รวมหลาย stream | stream เดียว |
|---|---|---|
| BentoProxy ESP32-S3 | ~580 KB/s ≈ 4.6 Mbit/s | ~220 KB/s ≈ 1.8 Mbit/s |
| BentoProxy ESP32-C5 (PSRAM 8 MB) | ~856 KB/s ≈ 6.8 Mbit/s | ~700–780 KB/s ≈ 5.6–6.2 Mbit/s |
| **ของเรา ESP32-S3 N16R8** | **9–12 Mbit/s** | — |
| **ของเรา ESP32-C3 Super Mini** | **~2.6–2.7 Mbit/s** (4 stream) | ~1.3–1.7 Mbit/s |

README ของเขาเขียนว่า "~5-10 Mbps per stream" แต่ตัวเลขที่เขาวัดจริงใน commit ต่ำกว่านั้น
นอกจากนี้ orchestrator จำกัด 5 MB/min ต่ออุปกรณ์ (≈0.67 Mbit/s) และ 8 connection

## สถาปัตยกรรมต่างกันตรงไหน

| | BentoProxy | ของเรา |
|---|---|---|
| ลิงก์บอร์ด → server | WebSocket บน TLS (ฝั่ง client ต้อง mask ทุก frame) | TLS ตรง + mux1 (ไม่มีชั้น WebSocket) |
| Flow control ต่อ stream | ไม่มี มีแค่ ACK ตอนเปิด stream ทุก stream พึ่ง backpressure ของ TCP ลิงก์เดียว | มี credit window ต่อ stream ลูกค้าคนเดียวที่ช้าจึงไม่ถ่วงคนอื่น |
| Stream ต่อบอร์ด | S3 12 (route 8), C5 20 (route 16) | S3 40, C3 4 |
| TCP window | S3 5,760 B (จำกัด stream เดียวไว้ราว 265 KB/s), C5 32 KB | S3 32 KB+, C3 8 KB |
| โมเดลธุรกิจ | ใครก็ได้ติดตั้งโหนดที่บ้านแล้วรับเงินค่าแบนด์วิดท์ | เจ้าของวางบอร์ดเองและขาย seat ต่อบอร์ด |

## สิ่งที่เขามีและควรพิจารณา

Firmware:
- **OTA แบบเซ็นลายเซ็น:** image ต้องมี Ed25519 signature ของ SHA-256 จากคีย์ release
  และเวอร์ชันที่ rollback แล้วถูกจดว่าเสีย จะไม่ติดตั้งซ้ำ ของเรามี rollback และ `esp_https_ota`
  ที่เชื่อ HTTPS ของ flash.jbacteam.com ผ่าน CA bundle สาธารณะ แต่ไม่ได้ตรวจลายเซ็นของ image เอง
  ถ้าเว็บ flasher หรือ Cloudflare ถูกเจาะ จะส่ง firmware ปลอมให้บอร์ดได้
- **Secure Boot V2 + flash encryption profile:** บอร์ดของเราอยู่ในบ้านคนอื่นและเก็บ
  POOL_TOKEN ไว้ใน firmware จึงควรพิจารณาข้อนี้ (การเบิร์น eFuse ย้อนกลับไม่ได้ ต้องวางแผนก่อน)
- **ตัวนับเวลาใน relay loop:** แยกเวลาสร้าง frame / TLS / รอ socket / อ่าน และ CPU ต่อ task
  จาก FreeRTOS run-time stats เขาบันทึกว่า "เดาสาเหตุแล้วไม่ขยับ" จนกว่าจะวัดแบบนี้
- watchdog ของ relay loop, LED บอกสถานะ (รอ WiFi / รอ claim / กำลังต่อ / ปกติดับไฟ),
  captive portal ที่ iOS เปิดเองได้ (DHCP แจก DNS เป็นตัวบอร์ด), รหัส claim ที่พิมพ์ติดบอร์ด
- Port blocklist บนบอร์ด (SMTP, SMB, ฐานข้อมูล)

Server:
- เมื่อโหนดหยุด ACK stream ระบบจะ fail over ไปโหนดอื่น, route ตาม capacity ที่โหนดประกาศ,
  และเลือกโหนดที่มี connection น้อยที่สุด (least-connections)
- เก็บผล speed test และให้เจ้าของโหนดสั่งทดสอบเองได้

## แผนทดลองให้ C3 เร็วขึ้น

ตอนนี้ C3 ใช้ค่าที่เน้นประหยัด RAM/flash (`firmware/sdkconfig.defaults.c3mini`)
ซึ่งตรงข้ามกับสิ่งที่ทำให้ C5 ของเขาเร็ว แต่ละข้อเป็นสมมติฐาน
ให้วัดทีละอย่างบน bench broker ตามวิธีใน `AGENTS.md` โดยดู `cpu` และ `heap_min`
ใน STATS ทุกครั้ง (heap ต่ำสุดตอนนี้ ~65 KB ขณะรับโหลด)

1. **TX block-ack window 6 → 16 หรือ 32:** ตอนลูกค้าดาวน์โหลด บอร์ดต้องส่งข้อมูลออก
   (WiFi TX) ไปหา broker และ S3 ของเราใช้ 32 อยู่แล้ว
2. **เปิด `ESP_WIFI_IRAM_OPT`, `ESP_WIFI_RX_IRAM_OPT`, `LWIP_IRAM_OPTIMIZATION`:**
   ปิดไว้เพื่อประหยัด RAM แต่ทำให้ hot path ของ WiFi/lwIP รันจาก flash cache
3. **`COMPILER_OPTIMIZATION_PERF` (-O2) ทั้ง IDF แทน `-Os`:** image 2.4.25-c3 ใหญ่ 1.22 MB
   ใน slot 1.98 MB (เหลือราว 38%) จึงน่าจะพอ C3 เป็น core เดียว 160 MHz เหมือนที่เขาบอกว่า
   C5 "CPU-bound"
4. **ให้ ChaCha20-Poly1305 มาก่อน AES-GCM:** C3 มี AES hardware แต่ไม่มี GCM hardware
   ตอนนี้ firmware ไม่ได้กำหนด ciphersuite (`rproxy.c` `esp_tls_cfg_t`) ส่วน broker ที่เป็น Go
   จะเลือก ChaCha ถ้า client ใส่ไว้ก่อน
5. **`MBEDTLS_SSL_OUT_CONTENT_LEN` 4096 → 8192:** record น้อยลง แลกกับ heap เพิ่มราว 4 KB
6. **เพิ่มตัวนับเวลาแบบข้อ "ตัวนับเวลาใน relay loop" ลงใน STATS** ก่อนหรือคู่กับข้อ 1–5
   จะได้รู้ว่าเวลาหายไปที่ TLS, WiFi หรือการรอ socket
7. ฮาร์ดแวร์: C3 Super Mini มีเสาอากาศเล็ก ให้ดู RSSI ใน STATS ประกอบ ถ้าต้องการบอร์ด
   ราคาถูกที่เร็วกว่า **ESP32-C5 ที่มี PSRAM** น่าทดลองที่สุด เพราะ IDF 5.5 รองรับแล้ว
   และระบบเวอร์ชันของเราใส่ suffix ชิปได้ (`-c5`) แต่ต้อง port และทดสอบใหม่ทั้งหมด

แต่ละข้อเปลี่ยน firmware จึงต้องผ่านขั้นตอนเดิม: build ทั้งสองบอร์ด, archive เป็น testing,
ทดสอบบน bench, OTA ทีละบอร์ด และห้าม promote เป็น stable จนกว่าจะ soak ผ่าน
