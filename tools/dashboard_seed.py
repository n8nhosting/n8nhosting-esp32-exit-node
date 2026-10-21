"""Seed an isolated dashboard QA database. Never point this at production data."""
import datetime as dt
import math
import sqlite3
import sys
import time

db = sqlite3.connect(sys.argv[1])
now = int(time.time())
iso = lambda seconds: dt.datetime.fromtimestamp(seconds, dt.timezone.utc).isoformat().replace('+00:00', 'Z')
names = ['บ้านเชียงใหม่ · สำนักงานทดสอบ', 'C3 · ร้านกาแฟริมแม่น้ำ', 'บ้านลูกค้าออฟไลน์', 'บอร์ดระงับการใช้งาน', 'กำลังอัปเดตเฟิร์มแวร์', 'ย้อนกลับเวอร์ชันเดิม', 'บอร์ดเก่ายังไม่วัดความจุ', 'สาขากรุงเทพ · รายงานค้าง']
macs = [f'aa:bb:cc:dd:ee:{i:02x}' for i in range(1, 9)]
for i, (mac, name) in enumerate(zip(macs, names)):
    db.execute('INSERT OR REPLACE INTO devices(mac,name,kind,slot,capacity_mbps,capacity_measured_at,capacity_active_streams,lat,lon,notes) VALUES(?,?,?,?,?,?,?,?,?,?)',
               (mac, name, 'shared', i+1, 0 if i == 6 else (2.6 if i == 1 else 11.8), iso(now-86400), 0, 13.7563+i*.08, 100.5018+i*.03, 'ข้อมูลจำลองสำหรับตรวจรูปแบบหน้าจอเท่านั้น'))
users = [('customer-office',0,1,3,50,2), ('customer-backup',0,1,20,30,5), ('cafe-c3',1,1,2,10,1), ('customer-offline',2,1,8,50,4), ('expired-user',4,1,-2,20,3), ('quota-user',5,1,9,10,12), ('disabled-user',3,0,12,20,2)]
for username, device, enabled, days, quota, usage in users:
    db.execute('INSERT OR REPLACE INTO users(username,enabled,expires_at,rate_mbps,quota_gb,usage_bytes,usage_month,created_at,note,device_mac) VALUES(?,?,?,?,?,?,?,?,?,?)',
               (username, enabled, iso(now+days*86400), 2 if device == 1 else 5, quota, int(usage*1024**3), dt.datetime.now(dt.timezone.utc).strftime('%Y-%m'), iso(now-15*86400), 'ลูกค้าตัวอย่าง · ต่ออายุทาง LINE', macs[device]))
# Hourly history older than 48h; fine-grained recent history. Leave real holes.
for step, width, start, stop in [('1h',3600,now-30*86400,now-48*3600), ('5m',300,now-48*3600,now)]:
    for j, ts in enumerate(range(start//width*width, stop//width*width, width)):
        if (ts//width)%43 in (10,11,12):
            continue
        wave = .6 + .4*math.sin(j*.23)
        up, down = int((3+wave*6)*1024**2), int((10+wave*24)*1024**2)
        db.execute('INSERT OR REPLACE INTO metrics_fleet VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)', (ts,step,6,8,6,7,4,3,1,1,up,down,180,4))
        for i,mac in enumerate(macs):
            db.execute('INSERT OR REPLACE INTO metrics_node VALUES(?,?,?,?,?,?,?,?,?)', (ts,step,mac,0 if i==2 else 1,up//8,down//8,22,1 if i==1 else 0,2 if i==1 else 4))
        db.execute('INSERT OR REPLACE INTO metrics_user VALUES(?,?,?,?)', (ts,step,'customer-office',up+down))
for i,(kind,severity,subject,message) in enumerate([
    ('node.offline','warn',macs[2],'บ้านลูกค้าออฟไลน์ disconnected'),
    ('node.ota_failed','error',macs[5],'Firmware rolled back on ย้อนกลับเวอร์ชันเดิม'),
    ('device.measured','info',macs[1],'C3 measured at 2.6 Mbps'),
    ('user.extended','info','customer-office','Customer subscription extended'),
    ('device.updated','info',macs[0],'Saved board location'),
]):
    db.execute('INSERT INTO events(ts,kind,severity,subject_type,subject_id,actor,message,data) VALUES(?,?,?,?,?,?,?,?)', (iso(now-i*400),kind,severity,'user' if kind.startswith('user.') else 'node',subject,'fixture',message,'{}'))
db.commit()
db.close()
