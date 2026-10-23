package main

import (
	"context"
	"time"
)

// overviewSales is the business row of the dashboard Overview. Months and days
// are UTC, like monthKey's usage months. Revenue is money received: every order
// with paid_at in the window, including one still waiting for a board.
// Timestamps are RFC3339 UTC (portalTime), so string comparison orders them.
func (s *server) overviewSales(ctx context.Context, now time.Time) (map[string]any, error) {
	now = now.UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	prevStart := monthStart.AddDate(0, -1, 0)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	firstDay := today.AddDate(0, 0, -29)
	db := s.store.db

	var revenue, paid, prevRevenue, holding, review, waiting, active int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_satang),0),COUNT(*) FROM orders WHERE paid_at<>'' AND paid_at>=? AND paid_at<=?`,
		portalTime(monthStart), portalTime(now)).Scan(&revenue, &paid); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(SUM(amount_satang),0) FROM orders WHERE paid_at<>'' AND paid_at>=? AND paid_at<?`,
		portalTime(prevStart), portalTime(monthStart)).Scan(&prevRevenue); err != nil {
		return nil, err
	}
	// open orders: holding a seat for payment, a slip waiting for review, or paid and waiting for a board
	if err := db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(status='pending' AND paid_at='' AND reservation_expires_at>? AND NOT EXISTS(SELECT 1 FROM slips WHERE slips.order_id=orders.id AND reviewed_at='')),0),
		COALESCE(SUM(status='pending' AND paid_at='' AND EXISTS(SELECT 1 FROM slips WHERE slips.order_id=orders.id AND reviewed_at='')),0),
		COALESCE(SUM(status='waiting_capacity'),0)
		FROM orders`, portalTime(now)).Scan(&holding, &review, &waiting); err != nil {
		return nil, err
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions WHERE status='active'`).Scan(&active); err != nil {
		return nil, err
	}

	daily := make([]int64, 30)
	rows, err := db.QueryContext(ctx, `SELECT substr(paid_at,1,10),SUM(amount_satang) FROM orders WHERE paid_at<>'' AND paid_at>=? GROUP BY 1`, portalTime(firstDay))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var day string
		var amount int64
		if err := rows.Scan(&day, &amount); err != nil {
			return nil, err
		}
		if t, err := time.Parse("2006-01-02", day); err == nil {
			if i := int(t.Sub(firstDay).Hours() / 24); i >= 0 && i < len(daily) {
				daily[i] += amount
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"month": monthStart.Format("2006-01"), "revenue_satang": revenue, "paid_orders": paid, "prev_revenue_satang": prevRevenue,
		"awaiting_payment": holding, "awaiting_review": review, "waiting_capacity": waiting, "active_subscriptions": active,
		"daily_revenue_satang": daily,
	}, nil
}
