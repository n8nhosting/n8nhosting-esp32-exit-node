package main

// Commerce keeps payment state, seat reservations and provisioning in the same
// SQLite transaction. Provider HTTP and the broker snapshot happen beforehand.
import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Plan struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	BonusDays   int     `json:"bonus_days"`
	Days        int     `json:"days"`
	QuotaGB     float64 `json:"quota_gb"`
	RateMbps    float64 `json:"rate_mbps"`
	Kind        string  `json:"kind"`
	PriceSatang int64   `json:"price_satang"`
	Active      bool    `json:"active"`
}
type CommerceOrder struct {
	ID                   string `json:"id"`
	CustomerID           string `json:"customer_id"`
	SubscriptionID       string `json:"subscription_id"`
	PlanID               string `json:"plan_id"`
	PlanSnapshot         Plan   `json:"plan_snapshot"`
	AmountSatang         int64  `json:"amount_satang"`
	Method               string `json:"method"`
	Status               string `json:"status"`
	DeviceMAC            string `json:"device_mac,omitempty"`
	ReservationExpiresAt string `json:"reservation_expires_at"`
	CreatedAt            string `json:"created_at"`
	PaidAt               string `json:"paid_at"`
	StripeSessionID      string `json:"stripe_session_id,omitempty"`
}
type Subscription struct {
	ID           string `json:"id"`
	CustomerID   string `json:"customer_id"`
	PlanID       string `json:"plan_id"`
	Username     string `json:"username"`
	DeviceMAC    string `json:"device_mac,omitempty"`
	Slot         int    `json:"slot"`
	PeriodStart  string `json:"period_start"`
	PeriodEnd    string `json:"period_end"`
	PaidThrough  string `json:"paid_through,omitempty"`
	QuotaBytes   int64  `json:"quota_bytes"`
	UsedBytes    int64  `json:"used_bytes"`
	Status       string `json:"status"`
	LastNotified string `json:"-"`
}
type commerceError struct {
	Code    int
	Message string
}

func (e *commerceError) Error() string        { return e.Message }
func commerceFail(code int, msg string) error { return &commerceError{code, msg} }

const planCols = `id,name,description,bonus_days,days,quota_gb,rate_mbps,kind,price_satang,active`
const orderCols = `id,customer_id,subscription_id,plan_id,plan_snapshot,amount_satang,method,status,device_mac,reservation_expires_at,created_at,paid_at,stripe_session_id`
const subscriptionCols = `id,customer_id,plan_id,username,device_mac,slot,period_start,period_end,quota_bytes,used_bytes,status,last_notified`

type commerceScanner interface{ Scan(...any) error }

func scanPlan(row commerceScanner) (*Plan, error) {
	p := &Plan{}
	err := row.Scan(&p.ID, &p.Name, &p.Description, &p.BonusDays, &p.Days, &p.QuotaGB, &p.RateMbps, &p.Kind, &p.PriceSatang, &p.Active)
	return p, err
}
func scanCommerceOrder(row commerceScanner) (*CommerceOrder, error) {
	o := &CommerceOrder{}
	var raw string
	err := row.Scan(&o.ID, &o.CustomerID, &o.SubscriptionID, &o.PlanID, &raw, &o.AmountSatang, &o.Method, &o.Status, &o.DeviceMAC, &o.ReservationExpiresAt, &o.CreatedAt, &o.PaidAt, &o.StripeSessionID)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &o.PlanSnapshot)
	}
	return o, err
}
func scanSubscription(row commerceScanner) (*Subscription, error) {
	s := &Subscription{}
	err := row.Scan(&s.ID, &s.CustomerID, &s.PlanID, &s.Username, &s.DeviceMAC, &s.Slot, &s.PeriodStart, &s.PeriodEnd, &s.QuotaBytes, &s.UsedBytes, &s.Status, &s.LastNotified)
	return s, err
}
func commerceSubscriptionConn(ctx context.Context, c *sql.Conn, id, customer string, now time.Time) (*Subscription, error) {
	sub, err := scanSubscription(c.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=? AND customer_id=?`, id, customer))
	if err != nil {
		return nil, err
	}
	var exists int
	if err = c.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username=? AND subscription_id=?`, sub.Username, id).Scan(&exists); err != nil {
		return nil, err
	}
	if exists != 1 {
		return nil, commerceFail(409, "subscription credentials unavailable")
	}
	if err = refreshCommerceUser(ctx, c, sub.Username, now); err != nil {
		return nil, err
	}
	return scanSubscription(c.QueryRowContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=? AND customer_id=?`, id, customer))
}
func validatePlan(p *Plan) error {
	if !utf8.ValidString(p.Name) || !utf8.ValidString(p.Description) || strings.TrimSpace(p.Name) == "" || utf8.RuneCountInString(p.Name) > 100 || utf8.RuneCountInString(p.Description) > 2000 || p.Days < 1 || p.Days > 3660 || p.BonusDays < 0 || p.BonusDays >= p.Days || p.PriceSatang < 100 || p.PriceSatang > 100_000_000 || math.IsNaN(p.QuotaGB) || math.IsInf(p.QuotaGB, 0) || p.QuotaGB < 0 || p.QuotaGB > 1_000_000 || math.IsNaN(p.RateMbps) || math.IsInf(p.RateMbps, 0) || p.RateMbps < 0 || p.RateMbps > 100_000 || (p.Kind != "shared" && p.Kind != "dedicated") {
		return commerceFail(400, "invalid plan")
	}
	return nil
}
func (s *server) commerceOrder(ctx context.Context, id string) (*CommerceOrder, error) {
	return scanCommerceOrder(s.store.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id=?`, id))
}
func (s *server) customerSubscriptions(ctx context.Context, customerID string) ([]Subscription, error) {
	rows, err := s.store.db.QueryContext(ctx, `SELECT `+subscriptionCols+` FROM subscriptions WHERE customer_id=? ORDER BY period_start DESC`, customerID)
	if err != nil {
		return nil, err
	}
	out := []Subscription{}
	for rows.Next() {
		v, e := scanSubscription(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, *v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range out {
		if err = s.resolveSubscriptionRouting(ctx, &out[i]); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if out[i].PaidThrough == "" {
			out[i].PaidThrough = out[i].PeriodEnd
		}
	}
	return out, nil
}
func (s *server) resolveSubscriptionRouting(ctx context.Context, sub *Subscription) error {
	err := s.store.db.QueryRowContext(ctx, `SELECT u.expires_at,u.device_mac,COALESCE(d.slot,0) FROM users u LEFT JOIN devices d ON d.mac=u.device_mac WHERE u.username=?`, sub.Username).Scan(&sub.PaidThrough, &sub.DeviceMAC, &sub.Slot)
	if errors.Is(err, sql.ErrNoRows) {
		sub.Slot = 0
	}
	return err
}
func (s *server) linkCommercePaymentSession(ctx context.Context, id, session string) error {
	if session == "" {
		return commerceFail(400, "session required")
	}
	res, err := s.store.db.ExecContext(ctx, `UPDATE orders SET stripe_session_id=? WHERE id=? AND method='stripe' AND paid_at='' AND status='pending' AND (stripe_session_id='' OR stripe_session_id=?)`, session, id, session)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return commerceFail(409, "order no longer payable")
	}
	return nil
}

// A snapshot never grants seats by itself: devices, occupants and reservations
// are re-read under BEGIN IMMEDIATE before every reservation/provision.
func (s *server) commerceNodes() map[string]brokerNode {
	var nodes []brokerNode
	if s.brokerGET("/nodes", &nodes) != nil {
		return nil
	}
	out := map[string]brokerNode{}
	for _, n := range nodes {
		out[n.MAC] = n
	}
	return out
}

type commerceBoard struct {
	mac, kind string
	slot      int
	capacity  float64
	free      int
}

func chooseCommerceBoard(ctx context.Context, c *sql.Conn, nodes map[string]brokerNode, p Plan, preferred, excludeOrder, self string, now time.Time) (commerceBoard, error) {
	rows, err := c.QueryContext(ctx, `SELECT mac,kind,COALESCE(slot,0),capacity_mbps FROM devices ORDER BY mac`)
	if err != nil {
		return commerceBoard{}, err
	}
	boards := []commerceBoard{}
	for rows.Next() {
		var b commerceBoard
		if err := rows.Scan(&b.mac, &b.kind, &b.slot, &b.capacity); err != nil {
			rows.Close()
			return b, err
		}
		boards = append(boards, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return commerceBoard{}, err
	}
	candidates := []commerceBoard{}
	stamp := portalTime(now)
	for _, b := range boards {
		n, ok := nodes[b.mac]
		if !ok || !n.Online || n.Blocked || b.slot <= 0 || n.Slot != b.slot || (n.Pool != nil && *n.Pool != poolFlag(b.kind)) || n.OTAStatus == "queued" || n.OTAStatus == "sent" || b.kind != p.Kind || b.capacity <= 0 || (p.RateMbps > 0 && b.capacity < p.RateMbps) || (preferred != "" && preferred != b.mac) {
			continue
		}
		var occupied, reserved int
		if err = c.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE device_mac=? AND enabled=1 AND username<>? AND substr(username,1,9)<>'_measure-' AND (expires_at='' OR julianday(expires_at) IS NULL OR julianday(expires_at)>=julianday(?))`, b.mac, self, stamp).Scan(&occupied); err != nil {
			return b, err
		}
		if err = c.QueryRowContext(ctx, `SELECT COUNT(*) FROM orders WHERE device_mac=? AND id<>? AND paid_at='' AND status='pending' AND reservation_expires_at>? AND (subscription_id='' OR subscription_id NOT IN (SELECT s.id FROM subscriptions s JOIN users u ON u.username=s.username WHERE s.status='active' AND u.expires_at>?))`, b.mac, excludeOrder, stamp, stamp).Scan(&reserved); err != nil {
			return b, err
		}
		b.free = maxUsers(b.kind, b.capacity) - occupied - reserved
		if b.free > 0 {
			candidates = append(candidates, b)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].free != candidates[j].free {
			return candidates[i].free > candidates[j].free
		}
		return candidates[i].mac < candidates[j].mac
	})
	if len(candidates) == 0 {
		return commerceBoard{}, nil
	}
	return candidates[0], nil
}
func (s *server) createCommerceOrder(ctx context.Context, customerID, planID, renewalID, method, preferredMAC string) (*CommerceOrder, error) {
	if method != "stripe" && method != "slip" {
		return nil, commerceFail(400, "invalid payment method")
	}
	nodes := s.commerceNodes()
	now := time.Now().UTC()
	o := &CommerceOrder{ID: "ord_" + randToken(12), CustomerID: customerID, PlanID: planID, SubscriptionID: renewalID, Method: method, Status: "waiting_capacity", CreatedAt: portalTime(now)}
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	err := inTx(ctx, s.store.db, func(c *sql.Conn) error {
		var verified, aup, accepted string
		var limit int
		if err := c.QueryRowContext(ctx, `SELECT verified_at,aup_version,aup_accepted_at,max_subscriptions FROM customers WHERE id=?`, customerID).Scan(&verified, &aup, &accepted, &limit); err != nil {
			return commerceFail(403, "verified customer required")
		}
		if verified == "" || aup != portalAUPVersion || accepted == "" {
			return commerceFail(403, "verified email and current AUP acceptance required")
		}
		p, err := scanPlan(c.QueryRowContext(ctx, `SELECT `+planCols+` FROM plans WHERE id=? AND active=1`, planID))
		if errors.Is(err, sql.ErrNoRows) {
			return commerceFail(404, "plan unavailable")
		}
		if err != nil {
			return err
		}
		o.PlanSnapshot = *p
		o.AmountSatang = p.PriceSatang
		self := ""
		if renewalID != "" {
			sub, err := commerceSubscriptionConn(ctx, c, renewalID, customerID, now)
			if err != nil {
				return commerceFail(404, "subscription not found")
			}
			self = sub.Username
			if sub.Status == "suspended" {
				return commerceFail(409, "subscription suspended")
			}
			if sub.Status == "active" && sub.PeriodEnd > portalTime(now) {
				preferredMAC = sub.DeviceMAC
			}
		}
		if renewalID == "" {
			var count int
			err = c.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM subscriptions s JOIN users u ON u.username=s.username WHERE s.customer_id=? AND s.status IN ('active','suspended','waiting_capacity') AND u.expires_at>?) + (SELECT COUNT(*) FROM orders WHERE customer_id=? AND subscription_id='' AND (paid_at<>'' OR (status='pending' AND reservation_expires_at>?)))`, customerID, portalTime(now), customerID, portalTime(now)).Scan(&count)
			if err != nil {
				return err
			}
			if count >= limit {
				return commerceFail(409, "subscription limit reached")
			}
		}
		b, err := chooseCommerceBoard(ctx, c, nodes, *p, preferredMAC, "", self, now)
		if err != nil {
			return err
		}
		if b.mac != "" {
			o.Status = "pending"
			o.DeviceMAC = b.mac
			o.ReservationExpiresAt = portalTime(now.Add(15 * time.Minute))
		}
		raw, _ := json.Marshal(p)
		_, err = c.ExecContext(ctx, `INSERT INTO orders (`+orderCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, o.ID, o.CustomerID, o.SubscriptionID, o.PlanID, string(raw), o.AmountSatang, o.Method, o.Status, o.DeviceMAC, o.ReservationExpiresAt, o.CreatedAt, "", "")
		return err
	})
	return o, err
}

func (s *server) fulfillCommerceOrder(ctx context.Context, id, eventID string) (*CommerceOrder, error) {
	if eventID == "" {
		return nil, commerceFail(400, "payment event required")
	}
	nodes := s.commerceNodes()
	now := time.Now().UTC()
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	var out *CommerceOrder
	changed := false
	err := inTx(ctx, s.store.db, func(c *sql.Conn) error {
		var existing string
		err := c.QueryRowContext(ctx, `SELECT order_id FROM payment_events WHERE id=?`, eventID).Scan(&existing)
		if err == nil && existing != id {
			return commerceFail(409, "payment event belongs to another order")
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		out, err = scanCommerceOrder(c.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id=?`, id))
		if err != nil {
			return err
		}
		if out.PaidAt != "" {
			_, err = c.ExecContext(ctx, `INSERT OR IGNORE INTO payment_events(id,order_id,created_at) VALUES(?,?,?)`, eventID, id, portalTime(now))
			return err
		}
		// Checkout may finish after an operator cancellation. Verified Stripe
		// funds must still be recorded; provisioning always rechecks capacity.
		if (out.Status == "cancelled" && out.Method != "stripe") || out.Status == "rejected" {
			return commerceFail(409, "order cannot be paid")
		}
		if _, err = c.ExecContext(ctx, `INSERT OR IGNORE INTO payment_events(id,order_id,created_at) VALUES(?,?,?)`, eventID, id, portalTime(now)); err != nil {
			return err
		}
		out.PaidAt = portalTime(now)
		changed = true
		return s.provisionCommerceConn(ctx, c, out, nodes, now)
	})
	if err == nil && changed {
		s.emit("order.paid", "info", "order", id, "payment", "Payment recorded", map[string]any{"status": out.Status, "amount_satang": out.AmountSatang})
	}
	return out, err
}
func (s *server) provisionCommerceConn(ctx context.Context, c *sql.Conn, o *CommerceOrder, nodes map[string]brokerNode, now time.Time) error {
	isRenewal := o.SubscriptionID != ""
	if _, err := c.ExecContext(ctx, `UPDATE orders SET last_attempt_at=? WHERE id=?`, portalTime(now), o.ID); err != nil {
		return err
	}
	p := o.PlanSnapshot
	self := ""
	var sub *Subscription
	if o.SubscriptionID != "" {
		var err error
		sub, err = commerceSubscriptionConn(ctx, c, o.SubscriptionID, o.CustomerID, now)
		if err != nil {
			return err
		}
		self = sub.Username
		if sub.Status == "suspended" {
			o.Status = "waiting_capacity"
			o.DeviceMAC = ""
			o.ReservationExpiresAt = ""
			if err = saveCommerceOrderConn(ctx, c, o); err != nil {
				return err
			}
			return commerceMail(ctx, c, o, "payment_received", "รับชำระเงินแล้ว / Payment received", "รับชำระเงินแล้ว กรุณาติดต่อผู้ดูแลเพื่อตรวจสอบบริการ / Payment received. Please contact support to review your service.")
		}
	}
	if sub == nil {
		var limit, count int
		if err := c.QueryRowContext(ctx, `SELECT max_subscriptions FROM customers WHERE id=?`, o.CustomerID).Scan(&limit); err != nil {
			return err
		}
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM subscriptions s JOIN users u ON u.username=s.username WHERE s.customer_id=? AND s.status IN ('active','suspended') AND u.expires_at>?`, o.CustomerID, portalTime(now)).Scan(&count); err != nil {
			return err
		}
		if count >= limit {
			o.Status = "waiting_capacity"
			o.DeviceMAC = ""
			o.ReservationExpiresAt = ""
			if err := saveCommerceOrderConn(ctx, c, o); err != nil {
				return err
			}
			return commerceMail(ctx, c, o, "payment_received", "รับชำระเงินแล้ว / Payment received", "รับชำระเงินแล้ว กำลังรอความพร้อมของบริการ โปรดดูสถานะในพอร์ทัล / Payment received; your order is waiting for capacity. View its status in your portal.")
		}
	}
	preferred := ""
	if o.ReservationExpiresAt > portalTime(now) {
		preferred = o.DeviceMAC
	}
	if sub != nil && sub.Status == "active" && sub.PeriodEnd > portalTime(now) {
		preferred = sub.DeviceMAC
	}
	b, err := chooseCommerceBoard(ctx, c, nodes, p, preferred, o.ID, self, now)
	if err != nil {
		return err
	}
	if b.mac == "" && preferred != "" && (sub == nil || sub.Status != "active" || sub.PeriodEnd <= portalTime(now)) {
		b, err = chooseCommerceBoard(ctx, c, nodes, p, "", o.ID, self, now)
		if err != nil {
			return err
		}
	}
	if b.mac == "" {
		o.Status = "waiting_capacity"
		o.DeviceMAC = ""
		o.ReservationExpiresAt = ""
		if err := saveCommerceOrderConn(ctx, c, o); err != nil {
			return err
		}
		return commerceMail(ctx, c, o, "payment_received", "รับชำระเงินแล้ว / Payment received", "รับชำระเงินแล้ว กำลังรอความพร้อมของบริการ โปรดดูสถานะในพอร์ทัล / Payment received; your order is waiting for capacity. View its status in your portal.")
	}
	start := now
	activeRenew := sub != nil && sub.Status == "active" && sub.PeriodEnd > portalTime(now)
	if activeRenew {
		var end string
		if err = c.QueryRowContext(ctx, `SELECT MAX(period_end) FROM subscription_periods WHERE subscription_id=?`, sub.ID).Scan(&end); err != nil {
			return err
		}
		start, _ = time.Parse(time.RFC3339, end)
	}
	end := start.AddDate(0, 0, p.Days)
	quota := int64(p.QuotaGB * 1024 * 1024 * 1024)
	if sub == nil {
		pw := randToken(16)
		u := &User{Username: "u-" + randToken(6), PassHash: hashPassword(pw), CredEnc: encryptCred(pw), Enabled: true, ExpiresAt: portalTime(end), RateMbps: p.RateMbps, QuotaGB: p.QuotaGB, CreatedAt: portalTime(now), DeviceMAC: b.mac, SubscriptionID: "sub_" + randToken(12)}
		res, insertErr := c.ExecContext(ctx, insertUser, userArgs(u)...)
		if insertErr != nil {
			return insertErr
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if inserted != 1 {
			return commerceFail(503, "credential allocation unavailable; retry payment processing")
		}
		sub = &Subscription{ID: u.SubscriptionID, CustomerID: o.CustomerID, PlanID: p.ID, Username: u.Username, DeviceMAC: b.mac, Slot: b.slot, PeriodStart: portalTime(start), PeriodEnd: portalTime(end), QuotaBytes: quota, Status: "active", LastNotified: "{}"}
		if _, err = c.ExecContext(ctx, `INSERT INTO subscriptions (`+subscriptionCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, sub.ID, sub.CustomerID, sub.PlanID, sub.Username, sub.DeviceMAC, sub.Slot, sub.PeriodStart, sub.PeriodEnd, sub.QuotaBytes, 0, sub.Status, sub.LastNotified); err != nil {
			return err
		}
		o.SubscriptionID = sub.ID
	} else if !activeRenew {
		if _, err = c.ExecContext(ctx, `UPDATE users SET enabled=1,expires_at=?,rate_mbps=?,quota_gb=?,usage_bytes=0,device_mac=? WHERE username=?`, portalTime(end), p.RateMbps, p.QuotaGB, b.mac, sub.Username); err != nil {
			return err
		}
		if _, err = c.ExecContext(ctx, `UPDATE subscriptions SET plan_id=?,device_mac=?,slot=?,period_start=?,period_end=?,quota_bytes=?,used_bytes=0,status='active',last_notified='{}' WHERE id=?`, p.ID, b.mac, b.slot, portalTime(start), portalTime(end), quota, sub.ID); err != nil {
			return err
		}
	}
	periodStatus := "active"
	if activeRenew {
		periodStatus = "scheduled"
	}
	if _, err = c.ExecContext(ctx, `INSERT INTO subscription_periods(order_id,subscription_id,plan_id,period_start,period_end,quota_bytes,rate_mbps,status) VALUES(?,?,?,?,?,?,?,?)`, o.ID, sub.ID, p.ID, portalTime(start), portalTime(end), quota, p.RateMbps, periodStatus); err != nil {
		return err
	}
	// A contiguous prepaid period holds the same seat across the boundary,
	// including the interval before the next maintenance/auth request.
	if activeRenew {
		if _, err = c.ExecContext(ctx, `UPDATE users SET expires_at=? WHERE username=?`, portalTime(end), sub.Username); err != nil {
			return err
		}
	}
	o.DeviceMAC = b.mac
	o.ReservationExpiresAt = ""
	o.Status = "paid"
	if err = saveCommerceOrderConn(ctx, c, o); err != nil {
		return err
	}
	kind, subject, action := "activation", "เปิดใช้งานบริการแล้ว / Service activated", "เปิดใช้งานบริการแล้ว / Your service is active."
	if isRenewal {
		kind, subject, action = "renewal", "ต่ออายุบริการแล้ว / Service renewed", "ต่ออายุและเปิดใช้งานรอบใหม่แล้ว / Your renewed service period is active."
		if activeRenew {
			action = "บันทึกการต่ออายุล่วงหน้าแล้ว รอบใหม่เริ่มตามวันที่ด้านล่าง / Renewal is scheduled; your current period remains unchanged until the start below."
		}
	}
	link := ""
	if base, ok := portalBase(); ok {
		link = base.String()
	}
	body := fmt.Sprintf("%s\nแพ็กเกจ / Plan: %s\nเริ่มรอบ / Period start (UTC): %s\nสิ้นสุดรอบ / Period end (UTC): %s\nดูรายละเอียดและข้อมูลการเชื่อมต่อในพอร์ทัล / Sign in to your portal for service details and credentials.\n%s", action, p.Name, portalTime(start), portalTime(end), link)
	return commerceMail(ctx, c, o, kind, subject, body)
}
func saveCommerceOrderConn(ctx context.Context, c *sql.Conn, o *CommerceOrder) error {
	_, err := c.ExecContext(ctx, `UPDATE orders SET subscription_id=?,status=?,device_mac=?,reservation_expires_at=?,paid_at=? WHERE id=?`, o.SubscriptionID, o.Status, o.DeviceMAC, o.ReservationExpiresAt, o.PaidAt, o.ID)
	return err
}
func commerceMail(ctx context.Context, c *sql.Conn, o *CommerceOrder, kind, subject, body string) error {
	var email string
	if err := c.QueryRowContext(ctx, `SELECT email FROM customers WHERE id=?`, o.CustomerID).Scan(&email); err != nil {
		return err
	}
	return queueEmailConn(ctx, c, kind+":"+o.ID, o.CustomerID, kind, email, subject, body)
}

// Activate prepaid periods at the actual boundary; buying early never resets
// the current quota. Legacy users continue to reset at the calendar month.
func refreshCommerceUser(ctx context.Context, c *sql.Conn, username string, now time.Time) error {
	u, err := scanUser(c.QueryRowContext(ctx, selectUser, username))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || u.SubscriptionID == "" {
		return err
	}
	var status, end string
	if err = c.QueryRowContext(ctx, `SELECT status,period_end FROM subscriptions WHERE id=?`, u.SubscriptionID).Scan(&status, &end); err != nil {
		return err
	}
	if status == "suspended" {
		return nil
	}
	stamp := portalTime(now)
	if end > stamp {
		return nil
	}
	var orderID, planID, start, nextEnd string
	var quota int64
	var rate float64
	err = c.QueryRowContext(ctx, `SELECT order_id,plan_id,period_start,period_end,quota_bytes,rate_mbps FROM subscription_periods WHERE subscription_id=? AND status='scheduled' AND period_start<=? AND period_end>? ORDER BY period_start LIMIT 1`, u.SubscriptionID, stamp, stamp).Scan(&orderID, &planID, &start, &nextEnd, &quota, &rate)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = c.ExecContext(ctx, `UPDATE subscriptions SET status='expired' WHERE id=? AND status='active'`, u.SubscriptionID)
		return err
	}
	if err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, `UPDATE subscription_periods SET status='expired' WHERE subscription_id=? AND period_end<=?`, u.SubscriptionID, stamp); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, `UPDATE subscription_periods SET status='active' WHERE order_id=?`, orderID); err != nil {
		return err
	}
	if _, err = c.ExecContext(ctx, `UPDATE subscriptions SET plan_id=?,period_start=?,period_end=?,quota_bytes=?,used_bytes=0,status='active',last_notified='{}' WHERE id=?`, planID, start, nextEnd, quota, u.SubscriptionID); err != nil {
		return err
	}
	var paidThrough string
	if err = c.QueryRowContext(ctx, `SELECT MAX(period_end) FROM subscription_periods WHERE subscription_id=?`, u.SubscriptionID).Scan(&paidThrough); err != nil {
		return err
	}
	_, err = c.ExecContext(ctx, `UPDATE users SET expires_at=?,rate_mbps=?,quota_gb=?,usage_bytes=0 WHERE username=?`, paidThrough, rate, float64(quota)/(1024*1024*1024), username)
	return err
}
func (s *server) maintainCommerce(ctx context.Context, now time.Time) error {
	return inTx(ctx, s.store.db, func(c *sql.Conn) error {
		rows, err := c.QueryContext(ctx, `SELECT username FROM users WHERE subscription_id<>''`)
		if err != nil {
			return err
		}
		names := []string{}
		for rows.Next() {
			var n string
			if err = rows.Scan(&n); err != nil {
				rows.Close()
				return err
			}
			names = append(names, n)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, n := range names {
			if err = refreshCommerceUser(ctx, c, n, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *server) recordCommerceSlip(ctx context.Context, customerID, id, file, mime string, size int64, sha, result string) error {
	_, hashErr := hex.DecodeString(sha)
	if size <= 0 || size > 5*1024*1024 || len(sha) != 64 || hashErr != nil || sha != strings.ToLower(sha) || file == "" || len(file) > 128 || strings.ContainsAny(file, "/\\") || (mime != "image/jpeg" && mime != "image/png" && mime != "application/pdf") || len(result) > 16*1024 || !strings.HasPrefix(strings.TrimSpace(result), "{") || !json.Valid([]byte(result)) {
		return commerceFail(400, "invalid slip")
	}
	return inTx(ctx, s.store.db, func(c *sql.Conn) error {
		o, err := scanCommerceOrder(c.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id=? AND customer_id=?`, id, customerID))
		if err != nil {
			return commerceFail(404, "order not found")
		}
		if o.Method != "slip" || o.PaidAt != "" || (o.Status != "pending" && o.Status != "waiting_capacity") {
			return commerceFail(409, "order does not accept a slip")
		}
		var exists int
		if err = c.QueryRowContext(ctx, `SELECT COUNT(*) FROM slips WHERE order_id=? OR sha256=?`, id, sha).Scan(&exists); err != nil {
			return err
		}
		if exists != 0 {
			return commerceFail(409, "slip already submitted")
		}
		storageLimit, err := strconv.ParseInt(envOr("SLIP_STORAGE_MAX_BYTES", "536870912"), 10, 64)
		if err != nil || storageLimit < 1024*1024 || storageLimit > 8*1024*1024*1024 {
			storageLimit = 512 * 1024 * 1024
		}
		var stored int64
		if err = c.QueryRowContext(ctx, `SELECT COALESCE(SUM(size_bytes),0) FROM slips`).Scan(&stored); err != nil {
			return err
		}
		if stored > storageLimit-size {
			return commerceFail(503, "slip storage limit reached")
		}
		_, err = c.ExecContext(ctx, `INSERT INTO slips(order_id,file,mime,size_bytes,sha256,verifier_result,created_at) VALUES(?,?,?,?,?,?,?)`, id, file, mime, size, sha, result, portalTime(time.Now()))
		return err
	})
}

func (s *server) reviewCommerceOrder(ctx context.Context, id string, approve bool, reason string) (*CommerceOrder, error) {
	if !utf8.ValidString(reason) || utf8.RuneCountInString(reason) > 1000 {
		return nil, commerceFail(400, "reason too long")
	}
	nodes := s.commerceNodes()
	now := time.Now().UTC()
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	var o *CommerceOrder
	changed := false
	err := inTx(ctx, s.store.db, func(c *sql.Conn) error {
		var err error
		o, err = scanCommerceOrder(c.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id=? AND method='slip'`, id))
		if err != nil {
			return err
		}
		var reviewed string
		if err = c.QueryRowContext(ctx, `SELECT reviewed_at FROM slips WHERE order_id=?`, id).Scan(&reviewed); err != nil {
			return commerceFail(404, "slip not found")
		}
		if reviewed != "" {
			if (approve && o.PaidAt != "") || (!approve && o.Status == "rejected") {
				return nil
			}
			return commerceFail(409, "slip already reviewed")
		}
		if o.PaidAt != "" || o.Status == "cancelled" {
			return commerceFail(409, "order already closed")
		}
		if _, err = c.ExecContext(ctx, `UPDATE slips SET reviewed_by='admin',reviewed_at=?,reason=? WHERE order_id=?`, portalTime(now), reason, id); err != nil {
			return err
		}
		changed = true
		if !approve {
			o.Status = "rejected"
			o.ReservationExpiresAt = ""
			if err = saveCommerceOrderConn(ctx, c, o); err != nil {
				return err
			}
			return commerceMail(ctx, c, o, "payment_rejected", "ตรวจสอบการชำระเงิน / Payment review", "กรุณาตรวจสอบการชำระเงินในพอร์ทัล / Please review your payment in the portal.")
		}
		o.PaidAt = portalTime(now)
		if _, err = c.ExecContext(ctx, `INSERT OR IGNORE INTO payment_events(id,order_id,created_at) VALUES(?,?,?)`, "slip:"+id, id, portalTime(now)); err != nil {
			return err
		}
		return s.provisionCommerceConn(ctx, c, o, nodes, now)
	})
	if err == nil && changed {
		kind := "order.rejected"
		if approve {
			kind = "order.approved"
		}
		s.emit(kind, "info", "order", id, "admin", "Payment reviewed", map[string]any{"status": o.Status})
	}
	return o, err
}

func (s *server) reservedCommerceSeats(now time.Time) map[string]int {
	out := map[string]int{}
	rows, err := s.store.db.Query(`SELECT device_mac,COUNT(*) FROM orders WHERE paid_at='' AND status='pending' AND reservation_expires_at>? AND (subscription_id='' OR subscription_id NOT IN (SELECT s.id FROM subscriptions s JOIN users u ON u.username=s.username WHERE s.status='active' AND u.expires_at>?)) GROUP BY device_mac`, portalTime(now), portalTime(now))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var mac string
		var n int
		if rows.Scan(&mac, &n) == nil {
			out[mac] = n
		}
	}
	return out
}

func (s *server) retryCommerceOrder(ctx context.Context, id string) (*CommerceOrder, error) {
	nodes := s.commerceNodes()
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	var o *CommerceOrder
	err := inTx(ctx, s.store.db, func(c *sql.Conn) error {
		var err error
		o, err = scanCommerceOrder(c.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id=?`, id))
		if err != nil {
			return err
		}
		if o.Status != "waiting_capacity" && o.Status != "pending" {
			return commerceFail(409, "order not waiting for capacity")
		}
		now := time.Now().UTC()
		if o.PaidAt != "" {
			return s.provisionCommerceConn(ctx, c, o, nodes, now)
		}
		if o.Method == "stripe" {
			var expiry int64
			err := c.QueryRowContext(ctx, `SELECT expires_at FROM stripe_checkout_requests WHERE order_id=?`, o.ID).Scan(&expiry)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err == nil && expiry <= now.Unix() {
				return commerceFail(409, "Stripe checkout expired; create a new order")
			}
		}
		self := ""
		preferred := ""
		if o.SubscriptionID != "" {
			sub, e := commerceSubscriptionConn(ctx, c, o.SubscriptionID, o.CustomerID, now)
			err = e
			if err != nil {
				return err
			}
			if sub.Status == "suspended" {
				return commerceFail(409, "subscription suspended")
			}
			self, preferred = sub.Username, sub.DeviceMAC
			if sub.Status != "active" || sub.PeriodEnd <= portalTime(now) {
				preferred = ""
			}
		}
		b, err := chooseCommerceBoard(ctx, c, nodes, o.PlanSnapshot, preferred, o.ID, self, now)
		if err != nil {
			return err
		}
		if b.mac == "" {
			o.Status = "waiting_capacity"
			o.DeviceMAC = ""
			o.ReservationExpiresAt = ""
		} else {
			o.Status = "pending"
			o.DeviceMAC = b.mac
			o.ReservationExpiresAt = portalTime(now.Add(15 * time.Minute))
		}
		return saveCommerceOrderConn(ctx, c, o)
	})
	return o, err
}

func (s *server) retryPaidCommerce(ctx context.Context, nodes []brokerNode, now time.Time) error {
	snapshot := map[string]brokerNode{}
	for _, n := range nodes {
		snapshot[n.MAC] = n
	}
	s.admitMu.Lock()
	defer s.admitMu.Unlock()
	rows, err := s.store.db.QueryContext(ctx, `SELECT id FROM orders WHERE status='waiting_capacity' AND paid_at<>'' ORDER BY last_attempt_at,paid_at,id LIMIT 20`)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var firstErr error
	for _, id := range ids {
		activated := false
		err = inTx(ctx, s.store.db, func(c *sql.Conn) error {
			o, err := scanCommerceOrder(c.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id=? AND status='waiting_capacity' AND paid_at<>''`, id))
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			if _, err = c.ExecContext(ctx, `UPDATE orders SET last_attempt_at=? WHERE id=?`, portalTime(now), id); err != nil {
				return err
			}
			if err = s.provisionCommerceConn(ctx, c, o, snapshot, now); err != nil {
				return err
			}
			activated = o.Status == "paid"
			return nil
		})
		if err != nil {
			// A business/data error must not pin this order to the head of every
			// future batch, nor roll back another customer's successful service.
			_, stampErr := s.store.db.ExecContext(ctx, `UPDATE orders SET last_attempt_at=? WHERE id=?`, portalTime(now), id)
			if firstErr == nil {
				firstErr = err
			}
			if stampErr != nil {
				return stampErr
			}
			continue
		}
		if activated {
			s.emit("order.provisioned", "info", "order", id, "system", "Paid order provisioned", nil)
		}
	}
	return firstErr
}
