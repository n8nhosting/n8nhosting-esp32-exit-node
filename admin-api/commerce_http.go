package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"
)

func registerCommerceRoutes(mux *http.ServeMux, s *server) {
	mux.HandleFunc("GET /api/plans", s.requireAdmin(s.handleCommercePlans))
	mux.HandleFunc("POST /api/plans", s.requireAdmin(s.handleCommercePlans))
	mux.HandleFunc("PUT /api/plans/{id}", s.requireAdmin(s.handleCommercePlan))
	mux.HandleFunc("DELETE /api/plans/{id}", s.requireAdmin(s.handleCommercePlan))
	mux.HandleFunc("GET /api/orders", s.requireAdmin(s.handleCommerceOrders))
	mux.HandleFunc("POST /api/orders", s.requireAdmin(s.handleCommerceOrders))
	mux.HandleFunc("POST /api/orders/{id}/retry", s.requireAdmin(s.handleCommerceRetry))
	mux.HandleFunc("POST /api/orders/{id}/cancel", s.requireAdmin(s.handleCommerceCancel))
	mux.HandleFunc("GET /api/orders/{id}/receipt", s.requireAdmin(s.handleCommerceReceipt))
	mux.HandleFunc("GET /api/subscriptions", s.requireAdmin(s.handleCommerceSubscriptions))
	mux.HandleFunc("GET /api/customers", s.requireAdmin(s.handleCommerceCustomers))
	mux.HandleFunc("POST /api/customers", s.requireAdmin(s.handleCommerceCustomers))
	mux.HandleFunc("GET /api/approvals", s.requireAdmin(s.handleCommerceApprovals))
	mux.HandleFunc("POST /api/approvals/{id}/approve", s.requireAdmin(s.handleCommerceReview))
	mux.HandleFunc("POST /api/approvals/{id}/reject", s.requireAdmin(s.handleCommerceReview))
	mux.HandleFunc("GET /portal/api/plans", s.handleCommercePublicPlans)
	mux.HandleFunc("GET /portal/api/orders", s.requirePortal(s.handlePortalCommerceOrders))
	mux.HandleFunc("POST /portal/api/orders", s.requirePortal(s.handlePortalCommerceOrders))
	mux.HandleFunc("GET /portal/api/orders/{id}/receipt", s.requirePortal(s.handleCommerceReceipt))
	mux.HandleFunc("GET /portal/api/subscriptions", s.requirePortal(s.handlePortalCommerceSubscriptions))
	mux.HandleFunc("GET /portal/api/subscriptions/{id}/credentials", s.requirePortal(s.handlePortalSubscriptionCredentials))
	mux.HandleFunc("POST /portal/api/subscriptions/{id}/rotate", s.requirePortal(s.handlePortalSubscriptionCredentials))
}
func writeCommerceError(w http.ResponseWriter, err error) {
	code := 500
	msg := "commerce unavailable"
	var e *commerceError
	if errors.As(err, &e) {
		code, msg = e.Code, e.Message
	} else if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotFound) {
		code, msg = 404, "not found"
	}
	writeJSON(w, code, map[string]string{"error": msg})
}
func (s *server) listCommercePlans(ctx context.Context, active bool) ([]Plan, error) {
	query := `SELECT ` + planCols + ` FROM plans`
	if active {
		query += ` WHERE active=1`
	}
	query += ` ORDER BY price_satang,id`
	rows, err := s.store.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		p, err := scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}
func (s *server) handleCommercePublicPlans(w http.ResponseWriter, r *http.Request) {
	p, err := s.listCommercePlans(r.Context(), true)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"plans": p})
}
func (s *server) handleCommercePlans(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		p, err := s.listCommercePlans(r.Context(), false)
		if err != nil {
			writeCommerceError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"plans": p})
		return
	}
	var p Plan
	if portalJSON(r, &p) != nil {
		writeCommerceError(w, commerceFail(400, "invalid plan body"))
		return
	}
	p.ID = "plan_" + randToken(10)
	if err := validatePlan(&p); err != nil {
		writeCommerceError(w, err)
		return
	}
	_, err := s.store.db.ExecContext(r.Context(), `INSERT INTO plans (`+planCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.BonusDays, p.Days, p.QuotaGB, p.RateMbps, p.Kind, p.PriceSatang, p.Active)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	s.emit("plan.created", "info", "plan", p.ID, "admin", "Plan created", nil)
	writeJSON(w, 201, p)
}
func (s *server) handleCommercePlan(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if r.Method == "DELETE" {
		res, err := s.store.db.ExecContext(r.Context(), `UPDATE plans SET active=0 WHERE id=?`, id)
		if err != nil {
			writeCommerceError(w, err)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			writeCommerceError(w, sql.ErrNoRows)
			return
		}
		s.emit("plan.deactivated", "info", "plan", id, "admin", "Plan deactivated", nil)
		w.WriteHeader(204)
		return
	}
	var p Plan
	if portalJSON(r, &p) != nil {
		writeCommerceError(w, commerceFail(400, "invalid plan body"))
		return
	}
	p.ID = id
	if err := validatePlan(&p); err != nil {
		writeCommerceError(w, err)
		return
	}
	res, err := s.store.db.ExecContext(r.Context(), `UPDATE plans SET name=?,description=?,bonus_days=?,days=?,quota_gb=?,rate_mbps=?,kind=?,price_satang=?,active=? WHERE id=?`, p.Name, p.Description, p.BonusDays, p.Days, p.QuotaGB, p.RateMbps, p.Kind, p.PriceSatang, p.Active, id)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeCommerceError(w, sql.ErrNoRows)
		return
	}
	s.emit("plan.updated", "info", "plan", id, "admin", "Plan updated", nil)
	writeJSON(w, 200, p)
}

type commerceOrderInput struct {
	CustomerID     string `json:"customer_id"`
	PlanID         string `json:"plan_id"`
	SubscriptionID string `json:"subscription_id"`
	Method         string `json:"method"`
	PreferredMAC   string `json:"preferred_device_mac"`
}

func (s *server) listCommerceOrders(ctx context.Context, customer string) ([]CommerceOrder, error) {
	query := `SELECT ` + orderCols + ` FROM orders`
	args := []any{}
	if customer != "" {
		query += ` WHERE customer_id=?`
		args = append(args, customer)
	}
	query += ` ORDER BY created_at DESC,id DESC LIMIT 500`
	rows, err := s.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CommerceOrder{}
	for rows.Next() {
		o, e := scanCommerceOrder(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}
func (s *server) handleCommerceOrders(w http.ResponseWriter, r *http.Request) {
	s.commerceOrdersHTTP(w, r, false)
}
func (s *server) handlePortalCommerceOrders(w http.ResponseWriter, r *http.Request) {
	s.commerceOrdersHTTP(w, r, true)
}
func (s *server) commerceOrdersHTTP(w http.ResponseWriter, r *http.Request, portal bool) {
	customer := ""
	if portal {
		customer = portalCustomer(r).ID
	}
	if r.Method == "GET" {
		list, err := s.listCommerceOrders(r.Context(), customer)
		if err != nil {
			writeCommerceError(w, err)
			return
		}
		if portal {
			for i := range list {
				list[i].DeviceMAC = ""
				list[i].StripeSessionID = ""
			}
		}
		writeJSON(w, 200, map[string]any{"orders": list})
		return
	}
	var in commerceOrderInput
	if portalJSON(r, &in) != nil {
		writeCommerceError(w, commerceFail(400, "invalid order body"))
		return
	}
	if portal {
		if in.PreferredMAC != "" {
			writeCommerceError(w, commerceFail(400, "operator allocation required"))
			return
		}
		in.CustomerID = customer
	}
	if (in.Method == "stripe" && !stripeConfigured()) || (in.Method == "slip" && !manualConfigured()) {
		writeCommerceError(w, commerceFail(503, "payment method not configured"))
		return
	}
	o, err := s.createCommerceOrder(r.Context(), in.CustomerID, in.PlanID, in.SubscriptionID, in.Method, in.PreferredMAC)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	s.emit("order.created", "info", "order", o.ID, "customer", "Order created", map[string]any{"status": o.Status, "amount_satang": o.AmountSatang})
	if portal {
		o.DeviceMAC = ""
		o.StripeSessionID = ""
	}
	writeJSON(w, 201, o)
}
func (s *server) handleCommerceSubscriptions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT `+subscriptionCols+` FROM subscriptions ORDER BY period_start DESC LIMIT 500`)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	list := []Subscription{}
	for rows.Next() {
		v, e := scanSubscription(rows)
		if e != nil {
			rows.Close()
			writeCommerceError(w, e)
			return
		}
		list = append(list, *v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	for i := range list {
		if err := s.resolveSubscriptionRouting(r.Context(), &list[i]); err != nil {
			list[i].PaidThrough = list[i].PeriodEnd
		}
	}
	writeJSON(w, 200, map[string]any{"subscriptions": list})
}
func (s *server) handlePortalCommerceSubscriptions(w http.ResponseWriter, r *http.Request) {
	if err := s.maintainCommerce(r.Context(), time.Now()); err != nil {
		writeCommerceError(w, err)
		return
	}
	list, err := s.customerSubscriptions(r.Context(), portalCustomer(r).ID)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	out := []map[string]any{}
	for _, v := range list {
		var p Plan
		row, _ := scanPlan(s.store.db.QueryRowContext(r.Context(), `SELECT `+planCols+` FROM plans WHERE id=?`, v.PlanID))
		if row != nil {
			p = *row
		}
		u, _ := s.store.Get(v.Username)
		rate := p.RateMbps
		if u != nil {
			rate = u.RateMbps
		}
		out = append(out, map[string]any{"id": v.ID, "plan_id": v.PlanID, "plan": p, "username": v.Username, "period_start": v.PeriodStart, "period_end": v.PeriodEnd, "paid_through": v.PaidThrough, "quota_bytes": v.QuotaBytes, "used_bytes": v.UsedBytes, "status": v.Status, "host": envOr("PROXY_PUBLIC_HOST", ""), "port": slotPort(v.Slot), "rate_mbps": rate})
	}
	writeJSON(w, 200, map[string]any{"subscriptions": out})
}
func (s *server) handleCommerceReceipt(w http.ResponseWriter, r *http.Request) {
	o, err := s.commerceOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	customer := portalCustomer(r).ID
	if strings.HasPrefix(r.URL.Path, "/portal/") && o.CustomerID != customer {
		writeCommerceError(w, sql.ErrNoRows)
		return
	}
	if o.PaidAt == "" {
		writeCommerceError(w, commerceFail(409, "receipt available after payment"))
		return
	}
	var email, name string
	if err = s.store.db.QueryRowContext(r.Context(), `SELECT email,name FROM customers WHERE id=?`, o.CustomerID).Scan(&email, &name); err != nil {
		writeCommerceError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"receipt_number": o.ID, "order_id": o.ID, "customer_name": name, "customer_email": email, "paid_at": o.PaidAt, "currency": "THB", "amount_satang": o.AmountSatang, "method": o.Method, "plan_snapshot": o.PlanSnapshot, "status": o.Status})
}

type CommerceSlip struct {
	MIME           string          `json:"mime"`
	SizeBytes      int64           `json:"size_bytes"`
	SHA256         string          `json:"sha256"`
	VerifierResult json.RawMessage `json:"verifier_result"`
	ReviewedBy     string          `json:"reviewed_by"`
	ReviewedAt     string          `json:"reviewed_at"`
	Reason         string          `json:"reason"`
	CreatedAt      string          `json:"created_at"`
}

func (s *server) handleCommerceApprovals(w http.ResponseWriter, r *http.Request) {
	list, err := s.listCommerceOrders(r.Context(), "")
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	out := []map[string]any{}
	for _, o := range list {
		if o.Method != "slip" {
			continue
		}
		var slip CommerceSlip
		var raw string
		err = s.store.db.QueryRowContext(r.Context(), `SELECT mime,size_bytes,sha256,verifier_result,reviewed_by,reviewed_at,reason,created_at FROM slips WHERE order_id=?`, o.ID).Scan(&slip.MIME, &slip.SizeBytes, &slip.SHA256, &raw, &slip.ReviewedBy, &slip.ReviewedAt, &slip.Reason, &slip.CreatedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			writeCommerceError(w, err)
			return
		}
		if !json.Valid([]byte(raw)) {
			raw = `{}`
		}
		slip.VerifierResult = json.RawMessage(raw)
		out = append(out, map[string]any{"order": o, "slip": slip})
	}
	writeJSON(w, 200, map[string]any{"approvals": out})
}
func (s *server) handleCommerceReview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if portalJSON(r, &in) != nil {
		writeCommerceError(w, commerceFail(400, "invalid review body"))
		return
	}
	approve := strings.HasSuffix(r.URL.Path, "/approve")
	o, err := s.reviewCommerceOrder(r.Context(), r.PathValue("id"), approve, in.Reason)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	writeJSON(w, 200, o)
}
func (s *server) handleCommerceRetry(w http.ResponseWriter, r *http.Request) {
	o, err := s.retryCommerceOrder(r.Context(), r.PathValue("id"))
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	writeJSON(w, 200, o)
}
func (s *server) handleCommerceCancel(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.db.ExecContext(r.Context(), `UPDATE orders SET status='cancelled',reservation_expires_at='' WHERE id=? AND paid_at='' AND status IN ('pending','waiting_capacity')`, r.PathValue("id"))
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeCommerceError(w, commerceFail(409, "order cannot be cancelled"))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "cancelled"})
}
func (s *server) handleCommerceCustomers(w http.ResponseWriter, r *http.Request) {
	if r.Method == "POST" {
		var in struct {
			Email string `json:"email"`
			Name  string `json:"name"`
		}
		decodeErr := portalJSON(r, &in)
		in.Email = strings.ToLower(strings.TrimSpace(in.Email))
		if decodeErr != nil || !validCommerceEmail(in.Email) || !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) > 200 {
			writeCommerceError(w, commerceFail(400, "invalid customer"))
			return
		}
		id := "cus_" + randToken(12)
		_, err := s.store.db.ExecContext(r.Context(), `INSERT INTO customers(id,email,name,created_at) VALUES(?,?,?,?)`, id, in.Email, in.Name, portalTime(time.Now()))
		if err != nil {
			writeCommerceError(w, commerceFail(409, "customer already exists"))
			return
		}
		writeJSON(w, 201, map[string]any{"id": id, "email": in.Email, "name": in.Name, "verified_at": ""})
		return
	}
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT id,email,name,verified_at,aup_version,aup_accepted_at,max_subscriptions,created_at FROM customers ORDER BY created_at DESC LIMIT 500`)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, email, name, verified, aup, accepted, created string
		var max int
		if err = rows.Scan(&id, &email, &name, &verified, &aup, &accepted, &max, &created); err != nil {
			writeCommerceError(w, err)
			return
		}
		out = append(out, map[string]any{"id": id, "email": email, "name": name, "verified_at": verified, "aup_version": aup, "aup_accepted_at": accepted, "max_subscriptions": max, "created_at": created})
	}
	writeJSON(w, 200, map[string]any{"customers": out})
}
func validCommerceEmail(email string) bool {
	return len(email) <= 254 && strings.Count(email, "@") == 1 && !strings.ContainsAny(email, "\r\n <>") && strings.IndexByte(email, '@') > 0
}
func (s *server) handlePortalSubscriptionCredentials(w http.ResponseWriter, r *http.Request) {
	sub, err := scanSubscription(s.store.db.QueryRowContext(r.Context(), `SELECT `+subscriptionCols+` FROM subscriptions WHERE id=? AND customer_id=?`, r.PathValue("id"), portalCustomer(r).ID))
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	if err = s.resolveSubscriptionRouting(r.Context(), sub); err != nil {
		writeCommerceError(w, err)
		return
	}
	if sub.Slot <= 0 {
		writeCommerceError(w, commerceFail(409, "subscription routing unavailable"))
		return
	}
	if envOr("PROXY_PUBLIC_HOST", "") == "" {
		writeCommerceError(w, commerceFail(503, "proxy endpoint unavailable"))
		return
	}
	u, err := s.store.Get(sub.Username)
	if err != nil {
		writeCommerceError(w, err)
		return
	}
	if r.Method == "POST" {
		pw := randToken(16)
		if err = s.store.Update(u.Username, func(v *User) error { v.PassHash = hashPassword(pw); v.CredEnc = encryptCred(pw); return nil }); err != nil {
			writeCommerceError(w, err)
			return
		}
		u.CredEnc = encryptCred(pw)
		s.emit("user.credentials_rotated", "info", "user", u.Username, "customer", "Customer rotated credentials", nil)
	}
	pw := decryptCred(u.CredEnc)
	if pw == "" {
		writeCommerceError(w, commerceFail(409, "rotate credentials to create a new password"))
		return
	}
	host := envOr("PROXY_PUBLIC_HOST", "")
	port := slotPort(sub.Slot)
	conn := fmt.Sprintf("%s:%s@%s:%d", u.Username, pw, host, port)
	writeJSON(w, 200, map[string]any{"username": u.Username, "password": pw, "host": host, "port": port, "connection": conn, "http_url": "http://" + conn, "socks5_url": "socks5h://" + conn, "expires_at": u.ExpiresAt})
}
