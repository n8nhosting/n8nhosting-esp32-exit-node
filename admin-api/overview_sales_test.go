package main

import (
	"testing"
	"time"
)

// The Overview's business row: money actually received this (UTC) month and
// last month, and where open orders wait. Revenue counts paid_at, so a paid
// order still waiting for a board is revenue; a cancelled or unpaid one never is.
func TestOverviewSummarizesSales(t *testing.T) {
	s, mux, _ := newTestServerWithBroker(t)
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	order := func(id, method, status, paidAt, reservedUntil string, amount int64) {
		t.Helper()
		if _, err := s.store.db.Exec(`INSERT INTO orders (id,customer_id,plan_id,plan_snapshot,amount_satang,method,status,reservation_expires_at,created_at,paid_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			id, "customer", "plan", "{}", amount, method, status, reservedUntil, portalTime(now), paidAt); err != nil {
			t.Fatal(err)
		}
	}
	order("paid-now", "stripe", "paid", portalTime(now), "", 29900)
	order("paid-month-start", "slip", "paid", portalTime(monthStart), "", 59000)
	order("paid-waiting-board", "slip", "waiting_capacity", portalTime(monthStart), "", 10000)
	order("paid-last-month", "stripe", "paid", portalTime(monthStart.Add(-24*time.Hour)), "", 129000)
	order("holding-seat", "stripe", "pending", "", portalTime(now.Add(10*time.Minute)), 29900)
	order("reservation-lapsed", "stripe", "pending", "", portalTime(now.Add(-time.Hour)), 29900)
	order("slip-in-review", "slip", "pending", "", portalTime(now.Add(10*time.Minute)), 29900)
	order("cancelled", "stripe", "cancelled", "", "", 5000)
	if _, err := s.store.db.Exec(`INSERT INTO slips (order_id,file,mime,size_bytes,sha256,created_at) VALUES('slip-in-review','f','image/png',1,'x',?)`, portalTime(now)); err != nil {
		t.Fatal(err)
	}
	for i, status := range []string{"active", "active", "expired"} {
		if _, err := s.store.db.Exec(`INSERT INTO subscriptions (id,customer_id,plan_id,username,device_mac,slot,period_start,period_end,quota_bytes,status) VALUES(?,?,?,?,?,?,?,?,?,?)`,
			"sub"+string(rune('a'+i)), "customer", "plan", "user"+string(rune('a'+i)), "", 0, portalTime(now), portalTime(now.Add(720*time.Hour)), 0, status); err != nil {
			t.Fatal(err)
		}
	}

	rr, out := doJSON(t, mux, "GET", "/api/overview", "admintok", nil)
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body)
	}
	sales, ok := out["sales"].(map[string]any)
	if !ok {
		t.Fatalf("overview has no sales summary: %v", out["sales"])
	}
	want := map[string]float64{
		"revenue_satang": 98900, "paid_orders": 3, "prev_revenue_satang": 129000,
		"awaiting_payment": 1, "awaiting_review": 1, "waiting_capacity": 1, "active_subscriptions": 2,
	}
	for key, v := range want {
		if sales[key] != v {
			t.Errorf("sales[%s] = %v, want %v", key, sales[key], v)
		}
	}
	if sales["month"] != now.Format("2006-01") {
		t.Errorf("sales month = %v", sales["month"])
	}
	daily, ok := sales["daily_revenue_satang"].([]any)
	if !ok || len(daily) != 30 || daily[29].(float64) < 29900 {
		t.Fatalf("daily revenue must hold 30 UTC days ending today: %v", sales["daily_revenue_satang"])
	}
}
