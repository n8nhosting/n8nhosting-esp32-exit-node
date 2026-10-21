"""Synthetic local commerce QA only; refuses a path outside dashboard fixture DBs."""
import datetime as dt
import hashlib
import json
import pathlib
import sqlite3
import sys

database = pathlib.Path(sys.argv[1]).resolve()
if database.name != "fixture.db" or not database.parent.name.startswith("exitnode-dashboard-"):
    raise SystemExit("Refusing non-fixture database")
slip_dir = pathlib.Path(sys.argv[2]).resolve()
slip_dir.mkdir(parents=True, exist_ok=True)
db = sqlite3.connect(database, timeout=15)
assert db.execute("SELECT COUNT(*) FROM devices WHERE mac LIKE 'aa:bb:cc:dd:ee:%'").fetchone()[0] == 8
now = dt.datetime.now(dt.timezone.utc).replace(microsecond=0)
stamp = lambda value: value.isoformat().replace("+00:00", "Z")
plan = dict(id="qa_commerce_plan", name="LOCAL QA · แพ็กเกจตรวจคิว", description="Synthetic local fixture only", days=33, bonus_days=3, quota_gb=0, rate_mbps=0, kind="dedicated", price_satang=24900, active=False)
db.execute("INSERT OR IGNORE INTO plans(id,name,description,bonus_days,days,quota_gb,rate_mbps,kind,price_satang,active) VALUES(?,?,?,?,?,?,?,?,?,?)", tuple(plan[k] for k in ("id","name","description","bonus_days","days","quota_gb","rate_mbps","kind","price_satang","active")))
db.execute("INSERT OR IGNORE INTO customers(id,email,verified_at,aup_version,aup_accepted_at,created_at) VALUES('qa_customer','qa-commerce@example.test',?,'2026-10-22',?,?)", (stamp(now),stamp(now),stamp(now)))
db.execute("INSERT OR IGNORE INTO users(username,enabled,expires_at,rate_mbps,quota_gb,device_mac,subscription_id) VALUES('qa-managed',1,?,1,10,'aa:bb:cc:dd:ee:06','qa_managed_sub')", (stamp(now+dt.timedelta(days=33)),))
db.execute("INSERT OR IGNORE INTO subscriptions(id,customer_id,plan_id,username,device_mac,slot,period_start,period_end,quota_bytes,used_bytes,status) VALUES('qa_managed_sub','qa_customer',?,'qa-managed','aa:bb:cc:dd:ee:06',6,?,?,10737418240,0,'active')", (plan["id"],stamp(now),stamp(now+dt.timedelta(days=33))))
for order_id,method,paid in [("qa_order_cancel","stripe",False),("qa_order_paid_wait","stripe",True),("qa_order_slip","slip",False)]:
    status = "pending" if method == "slip" else "waiting_capacity"
    db.execute("INSERT OR IGNORE INTO orders(id,customer_id,plan_id,plan_snapshot,amount_satang,method,status,reservation_expires_at,created_at,paid_at) VALUES(?,'qa_customer',?,?,?,?,?,?,?,?)", (order_id,plan["id"],json.dumps(plan,ensure_ascii=False),24900,method,status,stamp(now+dt.timedelta(minutes=15)),stamp(now),stamp(now) if paid else ""))
data = b"%PDF-1.4\nLOCAL QA SYNTHETIC SLIP - NOT A PAYMENT\n%%EOF"
filename = "qa-commerce-slip.pdf"
(slip_dir/filename).write_bytes(data)
db.execute("INSERT OR IGNORE INTO slips(order_id,file,mime,size_bytes,sha256,verifier_result,created_at) VALUES('qa_order_slip',?,'application/pdf',?,?,?,?)", (filename,len(data),hashlib.sha256(data).hexdigest(),json.dumps({"status":"manual_required","fixture":"LOCAL QA only"}),stamp(now)))
db.execute("INSERT OR IGNORE INTO email_outbox(id,customer_id,kind,to_email,subject,body_enc,state,next_at,created_at,sent_at) VALUES('qa_mail','qa_customer','activated','qa-commerce@example.test','LOCAL QA ONLY','','sent',?,?,?)", (stamp(now),stamp(now),stamp(now)))
db.commit()
db.close()
print("Seeded synthetic local orders/slip/email; no provider activity")
