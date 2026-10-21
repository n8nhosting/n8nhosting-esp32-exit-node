package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

func (s *server) scheduleCustomerEmails(ctx context.Context, now time.Time) error {
	if _, ok := portalBase(); !ok {
		return nil
	}
	// Materialise and close rows before the write transaction: SQLite deliberately
	// has one connection, so nesting a DB call while rows are open would deadlock.
	rows, err := s.store.DB().QueryContext(ctx, `SELECT s.id,s.customer_id,c.email,s.period_start,s.period_end,COALESCE(NULLIF(u.expires_at,''),s.period_end),s.quota_bytes,s.used_bytes,s.status FROM subscriptions s JOIN customers c ON c.id=s.customer_id LEFT JOIN users u ON u.username=s.username WHERE c.verified_at<>'' AND s.status IN ('active','expired')`)
	if err != nil {
		return err
	}
	type reminder struct {
		id, customer, to, start, end, paidThrough, status string
		quota, used                                       int64
	}
	all := []reminder{}
	for rows.Next() {
		var v reminder
		if err := rows.Scan(&v.id, &v.customer, &v.to, &v.start, &v.end, &v.paidThrough, &v.quota, &v.used, &v.status); err != nil {
			rows.Close()
			return err
		}
		all = append(all, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	return inTx(ctx, s.store.DB(), func(c *sql.Conn) error {
		for _, v := range all {
			end, err := time.Parse(time.RFC3339, v.paidThrough)
			if err != nil {
				continue
			}
			remaining := end.Sub(now)
			kinds := []string{}
			if remaining <= 0 && remaining >= -24*time.Hour {
				kinds = append(kinds, "expired")
			} else if remaining > 0 && v.status == "active" {
				switch {
				case remaining <= 24*time.Hour:
					kinds = append(kinds, "expiry_1")
				case remaining <= 3*24*time.Hour:
					kinds = append(kinds, "expiry_3")
				case remaining <= 7*24*time.Hour:
					kinds = append(kinds, "expiry_7")
				}
			}
			if v.status == "active" && remaining > 0 && v.quota > 0 {
				if v.used >= v.quota {
					kinds = append(kinds, "quota_100")
				} else if float64(v.used)/float64(v.quota) >= .8 {
					kinds = append(kinds, "quota_80")
				}
			}
			for _, kind := range kinds {
				subject, message := customerNoticeCopy(kind)
				text := fmt.Sprintf("%s\nใช้บริการได้ถึง / Paid through: %s\nรอบโควตาสิ้นสุด / Quota period ends: %s\nใช้ข้อมูล / Usage: %.2f / %.2f GiB\nดูรายละเอียดและต่ออายุ / Review or renew: %s/portal/\n", message, v.paidThrough, v.end, float64(v.used)/(1<<30), float64(v.quota)/(1<<30), portalOrigin())
				periodKey := v.paidThrough
				if strings.HasPrefix(kind, "quota_") {
					periodKey = v.start
				}
				id := "notice_" + portalHash(v.id+":"+periodKey+":"+kind)
				if err := queueEmailConn(ctx, c, id, v.customer, kind, v.to, subject, text); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func customerNoticeCopy(kind string) (string, string) {
	switch kind {
	case "expired":
		return "แพ็กเกจหมดอายุ / Subscription expired", "แพ็กเกจของคุณหมดอายุแล้ว กรุณาต่ออายุเพื่อใช้งานอีกครั้ง / Your subscription has expired. Renew to restore access."
	case "expiry_1":
		return "แพ็กเกจหมดอายุใน 1 วัน / Expires within 1 day", "แพ็กเกจของคุณจะหมดอายุภายใน 1 วัน / Your subscription expires within 1 day."
	case "expiry_3":
		return "แพ็กเกจหมดอายุใน 3 วัน / Expires within 3 days", "แพ็กเกจของคุณจะหมดอายุภายใน 3 วัน / Your subscription expires within 3 days."
	case "expiry_7":
		return "แพ็กเกจหมดอายุใน 7 วัน / Expires within 7 days", "แพ็กเกจของคุณจะหมดอายุภายใน 7 วัน / Your subscription expires within 7 days."
	case "quota_100":
		return "ใช้โควตาครบแล้ว / Data allowance reached", "แพ็กเกจของคุณใช้ข้อมูลครบโควตาแล้ว / You have reached this plan period's data allowance."
	default:
		return "ใช้โควตาใกล้ครบ / Data allowance warning", "แพ็กเกจของคุณใช้ข้อมูลแล้วอย่างน้อย 80% / You have used at least 80% of this plan period's data allowance."
	}
}

func portalOrigin() string {
	if u, ok := portalBase(); ok {
		return u.Scheme + "://" + u.Host
	}
	return ""
}
