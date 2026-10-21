package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func commerceFixture(t *testing.T, capacity float64) (*server, *http.ServeMux, *fakeBroker, Plan) {
	t.Helper()
	s, mux, fb := newTestServerWithBroker(t)
	registerCommerceRoutes(mux, s)
	fb.nodes = []brokerNode{{MAC: "c3", Slot: 1, Online: true, RemoteIP: "same-ip"}}
	if err := s.devices.Upsert("c3", func(d *Device) error { d.Kind = "shared"; d.CapacityMbps = capacity; d.Slot = 1; return nil }); err != nil {
		t.Fatal(err)
	}
	now := portalTime(time.Now())
	_, err := s.store.db.Exec(`INSERT INTO customers(id,email,verified_at,aup_version,aup_accepted_at,created_at,max_subscriptions) VALUES('customer','buyer@example.test',?,?,?, ?,30)`, now, portalAUPVersion, now, now)
	if err != nil {
		t.Fatal(err)
	}
	p := Plan{ID: "p", Name: "C3 low speed", Days: 45, QuotaGB: 1, RateMbps: 0.1, Kind: "shared", PriceSatang: 7900, Active: true}
	commerceInsertPlan(t, s, p)
	return s, mux, fb, p
}
func commerceInsertPlan(t *testing.T, s *server, p Plan) {
	t.Helper()
	_, err := s.store.db.Exec(`INSERT INTO plans (`+planCols+`) VALUES(?,?,?,?,?,?,?,?,?,?)`, p.ID, p.Name, p.Description, p.BonusDays, p.Days, p.QuotaGB, p.RateMbps, p.Kind, p.PriceSatang, p.Active)
	if err != nil {
		t.Fatal(err)
	}
}
func commerceCreate(t *testing.T, s *server, p, renew string) *CommerceOrder {
	t.Helper()
	o, err := s.createCommerceOrder(context.Background(), "customer", p, renew, "slip", "")
	if err != nil {
		t.Fatal(err)
	}
	return o
}
func commerceCount(t *testing.T, s *server, table string) int {
	t.Helper()
	var n int
	if err := s.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestCommerceConcurrentReservationsProtectLegacyAssign(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	var wg sync.WaitGroup
	orders := make(chan *CommerceOrder, 12)
	fail := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, e := s.createCommerceOrder(context.Background(), "customer", p.ID, "", "slip", "")
			if e != nil {
				fail <- e
				return
			}
			orders <- o
		}()
	}
	wg.Wait()
	close(orders)
	close(fail)
	for e := range fail {
		t.Error(e)
	}
	pending := 0
	for o := range orders {
		if o.Status == "pending" {
			pending++
			expiry, _ := time.Parse(time.RFC3339, o.ReservationExpiresAt)
			if left := time.Until(expiry); left < 14*time.Minute || left > 15*time.Minute {
				t.Fatalf("reservation duration %v", left)
			}
		}
	}
	if pending != 1 {
		t.Fatalf("reserved=%d want1", pending)
	}
	createUser(t, mux, map[string]any{"username": "legacy", "password": "pw"})
	rr, _ := doJSON(t, mux, "POST", "/api/users/legacy/assign", "admintok", map[string]any{"device_mac": "c3"})
	if rr.Code != 409 {
		t.Fatalf("legacy stole reserved seat: %d", rr.Code)
	}
	views := s.buildDeviceViews([]brokerNode{{MAC: "c3", Online: true, Slot: 1}})
	if len(views) != 1 || views[0].ActiveUsers != 0 || views[0].ReservedSeats != 1 || availableSeats(views) != 0 {
		t.Fatalf("views %+v", views)
	}
	_, err := s.store.db.Exec(`UPDATE orders SET reservation_expires_at=?`, portalTime(time.Now().Add(-time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	rr, _ = doJSON(t, mux, "POST", "/api/users/legacy/assign", "admintok", map[string]any{"device_mac": "c3"})
	if rr.Code != 200 {
		t.Fatalf("expired reservation held seat: %d", rr.Code)
	}
}
func TestCommercePaidIdempotencyAndImmutableSnapshot(t *testing.T) {
	s, _, _, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	_, err := s.store.db.Exec(`UPDATE plans SET price_satang=99999,days=1,quota_gb=99 WHERE id=?`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.fulfillCommerceOrder(context.Background(), o.ID, fmt.Sprintf("evt-%d", i))
			if e != nil {
				errs <- e
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	paid, err := s.commerceOrder(context.Background(), o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != "paid" || paid.SubscriptionID == "" || paid.AmountSatang != 7900 || paid.PlanSnapshot.Days != 45 || paid.PlanSnapshot.QuotaGB != 1 {
		t.Fatalf("paid %+v", paid)
	}
	for _, table := range []string{"users", "subscriptions", "subscription_periods", "email_outbox"} {
		if n := commerceCount(t, s, table); n != 1 {
			t.Fatalf("%s=%d", table, n)
		}
	}
	if commerceCount(t, s, "payment_events") != 12 {
		t.Fatal("distinct payment events lost")
	}
	// A fresh server has no in-memory payment state: dedupe is durable SQLite.
	restarted := &server{store: s.store, devices: s.devices}
	if _, err = restarted.fulfillCommerceOrder(context.Background(), o.ID, "evt-0"); err != nil {
		t.Fatal(err)
	}
	if commerceCount(t, s, "subscription_periods") != 1 || commerceCount(t, s, "payment_events") != 12 {
		t.Fatal("restart duplicate")
	}
	u := s.store.List()[0]
	pw := decryptCred(u.CredEnc)
	if pw == "" || !verifyPassword(u.PassHash, pw) {
		t.Fatal("credentials not correctly provisioned")
	}
	if strings.Contains(u.CredEnc, pw) {
		t.Fatal("plaintext credential")
	}
	var body string
	if err = s.store.db.QueryRow(`SELECT body_enc FROM email_outbox`).Scan(&body); err != nil || strings.Contains(body, pw) {
		t.Fatal("mail credential leak")
	}
}
func TestCommerceProvisionTransactionRollback(t *testing.T) {
	s, _, _, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	if _, err := s.store.db.Exec(`CREATE TRIGGER fail_provision BEFORE INSERT ON subscriptions BEGIN SELECT RAISE(ABORT,'injected'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fulfillCommerceOrder(context.Background(), o.ID, "evt"); err == nil {
		t.Fatal("expected rollback")
	}
	for _, table := range []string{"users", "subscriptions", "payment_events", "email_outbox"} {
		if n := commerceCount(t, s, table); n != 0 {
			t.Fatalf("%s leaked %d", table, n)
		}
	}
	o, _ = s.commerceOrder(context.Background(), o.ID)
	if o.PaidAt != "" || o.Status != "pending" {
		t.Fatalf("order not rolled back %+v", o)
	}
	s.store.db.Exec(`DROP TRIGGER fail_provision`)
	if _, err := s.fulfillCommerceOrder(context.Background(), o.ID, "evt"); err != nil {
		t.Fatal(err)
	}
}
func TestCommerceLatePaymentWaitsThenRecoversWithoutOverselling(t *testing.T) {
	s, mux, fb, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	s.store.db.Exec(`UPDATE orders SET reservation_expires_at=? WHERE id=?`, portalTime(time.Now().Add(-time.Second)), o.ID)
	createUser(t, mux, map[string]any{"username": "legacy", "password": "pw"})
	bindUser(t, s, "legacy", "c3")
	paid, err := s.fulfillCommerceOrder(context.Background(), o.ID, "evt")
	if err != nil || paid.Status != "waiting_capacity" || paid.PaidAt == "" {
		t.Fatalf("late paid %+v %v", paid, err)
	}
	if commerceCount(t, s, "subscriptions") != 0 {
		t.Fatal("oversold full board")
	}
	s.store.Update("legacy", func(u *User) error { u.Enabled = false; return nil })
	if err = s.retryPaidCommerce(context.Background(), fb.nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	paid, _ = s.commerceOrder(context.Background(), o.ID)
	if paid.Status != "paid" || paid.SubscriptionID == "" {
		t.Fatalf("recovery %+v", paid)
	}
	if err = s.retryPaidCommerce(context.Background(), fb.nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	if commerceCount(t, s, "subscriptions") != 1 {
		t.Fatal("duplicate recovery")
	}
}
func TestCommerceRenewalPreservesPeriodQuotaAndLegacyCalendar(t *testing.T) {
	s, _, _, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	paid, err := s.fulfillCommerceOrder(context.Background(), o.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	sub := subs[0]
	s.store.AddUsage(sub.Username, 123, time.Now())
	p2 := p
	p2.ID = "p2"
	p2.QuotaGB = 2
	p2.RateMbps = 0.2
	p2.Days = 30
	commerceInsertPlan(t, s, p2)
	renew := commerceCreate(t, s, p2.ID, paid.SubscriptionID)
	if reserved := s.reservedCommerceSeats(time.Now())["c3"]; reserved != 0 {
		t.Fatalf("renew consumed extra seat %d", reserved)
	}
	if _, err = s.fulfillCommerceOrder(context.Background(), renew.ID, "renew"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.store.Get(sub.Username)
	if u.UsageBytes != 123 || u.QuotaGB != 1 || u.RateMbps != 0.1 {
		t.Fatalf("early reset %+v", u)
	}
	nextMonth := time.Now().UTC().AddDate(0, 1, 0)
	s.store.AddUsage(sub.Username, 1, nextMonth)
	u, _ = s.store.Get(sub.Username)
	if u.UsageBytes != 124 {
		t.Fatalf("calendar reset subscription %d", u.UsageBytes)
	}
	end, _ := time.Parse(time.RFC3339, sub.PeriodEnd)
	if !takesSeat(u, end.Add(time.Second)) {
		t.Fatal("prepaid seat released before maintenance")
	}
	if err = s.maintainCommerce(context.Background(), end.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	u, _ = s.store.Get(sub.Username)
	if u.UsageBytes != 0 || u.QuotaGB != 2 || u.RateMbps != 0.2 {
		t.Fatalf("period activation %+v", u)
	}
	subs, _ = s.customerSubscriptions(context.Background(), "customer")
	last, _ := time.Parse(time.RFC3339, subs[0].PeriodEnd)
	if err = s.maintainCommerce(context.Background(), last.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	subs, _ = s.customerSubscriptions(context.Background(), "customer")
	u, _ = s.store.Get(sub.Username)
	if subs[0].Status != "expired" || takesSeat(u, last.Add(time.Second)) {
		t.Fatal("expired seat not released")
	}
	legacy := &User{Username: "calendar", Enabled: true, UsageBytes: 99, UsageMonth: monthKey(time.Now())}
	if err = s.store.Create(legacy); err != nil {
		t.Fatal(err)
	}
	s.store.AddUsage("calendar", 5, nextMonth)
	legacy, _ = s.store.Get("calendar")
	if legacy.UsageBytes != 5 {
		t.Fatalf("legacy rollover %d", legacy.UsageBytes)
	}
}
func TestCommercePerBoardKindRateAndCustomerLimits(t *testing.T) {
	s, _, fb, p := commerceFixture(t, 0.5)
	if err := s.devices.Upsert("other", func(d *Device) error { d.Kind = "dedicated"; d.CapacityMbps = 0.3; d.Slot = 2; return nil }); err != nil {
		t.Fatal(err)
	}
	fb.nodes = append(fb.nodes, brokerNode{MAC: "other", Slot: 2, Online: true, RemoteIP: "same-ip"})
	o := commerceCreate(t, s, p.ID, "")
	ded := p
	ded.ID = "ded"
	ded.Kind = "dedicated"
	ded.RateMbps = 0
	commerceInsertPlan(t, s, ded)
	d := commerceCreate(t, s, ded.ID, "")
	if o.DeviceMAC != "c3" || d.DeviceMAC != "other" {
		t.Fatalf("independent kind selection %+v %+v", o, d)
	}
	fast := p
	fast.ID = "fast"
	fast.RateMbps = 1
	commerceInsertPlan(t, s, fast)
	f := commerceCreate(t, s, fast.ID, "")
	if f.Status != "waiting_capacity" {
		t.Fatal("promised speed above capacity")
	}
	s.store.db.Exec(`UPDATE customers SET max_subscriptions=2`)
	if _, err := s.createCommerceOrder(context.Background(), "customer", p.ID, "", "slip", ""); err == nil {
		t.Fatal("limit not enforced against pending reservations")
	}
	s.store.db.Exec(`UPDATE customers SET verified_at=''`)
	if _, err := s.createCommerceOrder(context.Background(), "customer", p.ID, "", "slip", ""); err == nil {
		t.Fatal("unverified purchase")
	}
}
func TestCommerceSlipReviewAtomicDedupeAndReceipt(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	sha := strings.Repeat("a", 64)
	if err := s.recordCommerceSlip(context.Background(), "other", o.ID, "f", "image/png", 100, sha, `{}`); err == nil {
		t.Fatal("foreign upload")
	}
	if err := s.recordCommerceSlip(context.Background(), "customer", o.ID, "f", "image/png", 100, sha, `{"status":"manual_required"}`); err != nil {
		t.Fatal(err)
	}
	other := commerceCreate(t, s, p.ID, "")
	if err := s.recordCommerceSlip(context.Background(), "customer", other.ID, "f2", "image/png", 100, sha, `{}`); err == nil {
		t.Fatal("recycled slip")
	}
	paid, err := s.reviewCommerceOrder(context.Background(), o.ID, true, "checked")
	if err != nil || paid.Status != "paid" {
		t.Fatalf("approve %+v %v", paid, err)
	}
	if _, err = s.reviewCommerceOrder(context.Background(), o.ID, true, "repeat"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.reviewCommerceOrder(context.Background(), o.ID, false, "changed mind"); err == nil {
		t.Fatal("conflicting review")
	}
	rr, out := doJSON(t, mux, "GET", "/api/orders/"+o.ID+"/receipt", "admintok", nil)
	if rr.Code != 200 || out["amount_satang"] != float64(7900) || strings.Contains(rr.Body.String(), "password") || strings.Contains(rr.Body.String(), "device_mac") {
		t.Fatalf("receipt %d %s", rr.Code, rr.Body.String())
	}
}
func TestCommerceMigrationV6AndBrokerOutsideTransaction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migrations[:6] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	db.Exec(`CREATE TABLE schema_version(version INTEGER PRIMARY KEY);INSERT INTO schema_version VALUES(6);INSERT INTO users(username,enabled) VALUES('legacy',1)`)
	db.Close()
	db, err = openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version int
	db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version)
	if version != 7 {
		t.Fatalf("version %d", version)
	}
	u, err := scanUser(db.QueryRow(selectUser, "legacy"))
	if err != nil || u.SubscriptionID != "" || !u.Enabled {
		t.Fatalf("migration user %+v %v", u, err)
	}
	s, _, _, p := commerceFixture(t, 0.5)
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		var count int
		if err := s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&count); err != nil {
			t.Error("broker called with database held:", err)
			http.Error(w, "db held", 500)
			return
		}
		json.NewEncoder(w).Encode([]brokerNode{{MAC: "c3", Slot: 1, Online: true}})
	}))
	defer broker.Close()
	t.Setenv("BROKER_STATUS_URL", broker.URL)
	o := commerceCreate(t, s, p.ID, "")
	if _, err = s.fulfillCommerceOrder(context.Background(), o.ID, "evt"); err != nil {
		t.Fatal(err)
	}
}

func TestCommerceMigrationFailureRollsBackWholeV7(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, m := range migrations[:6] {
		if _, err = db.Exec(m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`CREATE TABLE schema_version(version INTEGER PRIMARY KEY);INSERT INTO schema_version VALUES(6);CREATE TABLE plans(conflict TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err = migrate(db); err == nil {
		t.Fatal("conflicting schema should fail")
	}
	var version, added, customers int
	db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version)
	db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('users') WHERE name='subscription_id'`).Scan(&added)
	db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='customers'`).Scan(&customers)
	if version != 6 || added != 0 || customers != 0 {
		t.Fatalf("partial migration version=%d column=%d table=%d", version, added, customers)
	}
}

func TestCommerceReservationsBlockLegacyReactivation(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	commerceCreate(t, s, p.ID, "")
	enabled := false
	for i, action := range []string{"enable", "extend", "put"} {
		name := fmt.Sprintf("old%d", i)
		createUser(t, mux, map[string]any{"username": name, "password": "pw", "enabled": enabled})
		bindUser(t, s, name, "c3")
		method, path, body := "POST", "/api/users/"+name+"/"+action, map[string]any{}
		if action == "put" {
			method = "PUT"
			path = "/api/users/" + name
			body["enabled"] = true
		}
		rr, _ := doJSON(t, mux, method, path, "admintok", body)
		if rr.Code != 409 {
			t.Fatalf("%s bypassed reserved seat: %d %s", action, rr.Code, rr.Body.String())
		}
	}
}

func TestCommercePortalSubscriptionOwnershipAndQuotaAuth(t *testing.T) {
	s, _, _, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	paid, err := s.fulfillCommerceOrder(context.Background(), o.ID, "evt")
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	sub := subs[0]
	u, _ := s.store.Get(sub.Username)
	pw := decryptCred(u.CredEnc)
	quota := int64(p.QuotaGB * 1024 * 1024 * 1024)
	s.store.AddUsage(u.Username, quota, time.Now())
	if ok, why := s.store.Authorize(u.Username, pw, time.Now()); ok || why != "quota exceeded" {
		t.Fatalf("quota auth %v %q", ok, why)
	}
	var used int64
	s.store.db.QueryRow(`SELECT used_bytes FROM subscriptions WHERE id=?`, sub.ID).Scan(&used)
	if used != quota {
		t.Fatalf("period usage %d", used)
	}
	if next := commerceCreate(t, s, p.ID, ""); next.Status != "waiting_capacity" {
		t.Fatal("over-quota user lost paid-period seat")
	}
	request := func(customer, path string, handler http.HandlerFunc) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(context.WithValue(r.Context(), portalIdentityKey{}, portalIdentity{ID: customer, Email: "buyer@example.test"}))
		rr := httptest.NewRecorder()
		handler(rr, r)
		return rr
	}
	rr := request("customer", "/portal/api/subscriptions", s.handlePortalCommerceSubscriptions)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "device_mac") || strings.Contains(rr.Body.String(), `"slot"`) || strings.Contains(rr.Body.String(), pw) || !strings.Contains(rr.Body.String(), `"port":3131`) {
		t.Fatalf("portal DTO %d %s", rr.Code, rr.Body.String())
	}
	foreign := httptest.NewRequest("GET", "/portal/api/subscriptions/"+paid.SubscriptionID+"/credentials", nil)
	foreign.SetPathValue("id", paid.SubscriptionID)
	foreign = foreign.WithContext(context.WithValue(foreign.Context(), portalIdentityKey{}, portalIdentity{ID: "foreign"}))
	rr = httptest.NewRecorder()
	s.handlePortalSubscriptionCredentials(rr, foreign)
	if rr.Code != 404 {
		t.Fatalf("foreign credentials %d", rr.Code)
	}
}

func TestCommerceSlipStorageCapAndLateCancelledStripePayment(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	t.Setenv("SLIP_STORAGE_MAX_BYTES", "1048576")
	first := commerceCreate(t, s, p.ID, "")
	if err := s.recordCommerceSlip(context.Background(), "customer", first.ID, "slip_one.png", "image/png", 700000, strings.Repeat("a", 64), `{}`); err != nil {
		t.Fatal(err)
	}
	second := commerceCreate(t, s, p.ID, "")
	if err := s.recordCommerceSlip(context.Background(), "customer", second.ID, "slip_two.png", "image/png", 500000, strings.Repeat("b", 64), `{}`); err == nil {
		t.Fatal("storage cap bypassed")
	}
	if commerceCount(t, s, "slips") != 1 {
		t.Fatal("rejected slip metadata persisted")
	}
	s.store.db.Exec(`UPDATE orders SET status='cancelled',reservation_expires_at='' WHERE id=?`, first.ID)
	stripe, err := s.createCommerceOrder(context.Background(), "customer", p.ID, "", "stripe", "")
	if err != nil {
		t.Fatal(err)
	}
	rr, _ := doJSON(t, mux, "POST", "/api/orders/"+stripe.ID+"/cancel", "admintok", nil)
	if rr.Code != 200 {
		t.Fatalf("cancel %d", rr.Code)
	}
	createUser(t, mux, map[string]any{"username": "legacy", "password": "pw"})
	bindUser(t, s, "legacy", "c3")
	paid, err := s.fulfillCommerceOrder(context.Background(), stripe.ID, "late_verified_payment")
	if err != nil || paid.PaidAt == "" || paid.Status != "waiting_capacity" {
		t.Fatalf("late funds %+v %v", paid, err)
	}
	if commerceCount(t, s, "subscriptions") != 0 || commerceCount(t, s, "payment_events") != 1 {
		t.Fatal("late payment lost or oversold")
	}
}

func TestCommerceLegacyPaymentCannotTakeReservedSeat(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	commerceCreate(t, s, p.ID, "")
	createUser(t, mux, map[string]any{"username": "old", "password": "pw", "enabled": false})
	bindUser(t, s, "old", "c3")
	req := httptest.NewRequest("POST", "/webhook/payment", strings.NewReader(`{"username":"old","days":30}`))
	req.Header.Set("X-Webhook-Token", "paytok")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 409 {
		t.Fatalf("legacy payment stole seat %d %s", rr.Code, rr.Body.String())
	}
	u, _ := s.store.Get("old")
	if u.Enabled {
		t.Fatal("failed payment activated user")
	}
}

func TestCommerceRenewalAtUnmaintainedBoundaryPreservesPrepaidPeriods(t *testing.T) {
	s, _, _, p := commerceFixture(t, 0.5)
	s.store.db.Exec(`UPDATE plans SET days=1 WHERE id=?`, p.ID)
	first := commerceCreate(t, s, p.ID, "")
	paid, err := s.fulfillCommerceOrder(context.Background(), first.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	next := p
	next.ID = "next"
	next.Days = 2
	next.QuotaGB = 2
	commerceInsertPlan(t, s, next)
	second := commerceCreate(t, s, next.ID, paid.SubscriptionID)
	if _, err = s.fulfillCommerceOrder(context.Background(), second.ID, "second"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	boundary := portalTime(now.Add(-time.Second))
	secondEnd := portalTime(now.Add(48 * time.Hour))
	subID := paid.SubscriptionID
	s.store.db.Exec(`UPDATE subscription_periods SET period_start=?,period_end=? WHERE order_id=?`, portalTime(now.Add(-24*time.Hour)), boundary, first.ID)
	s.store.db.Exec(`UPDATE subscription_periods SET period_start=?,period_end=? WHERE order_id=?`, boundary, secondEnd, second.ID)
	s.store.db.Exec(`UPDATE subscriptions SET period_end=?,used_bytes=55 WHERE id=?`, boundary, subID)
	s.store.db.Exec(`UPDATE users SET expires_at=?,usage_bytes=55 WHERE subscription_id=?`, secondEnd, subID)
	thirdPlan := p
	thirdPlan.ID = "third"
	thirdPlan.Days = 3
	thirdPlan.QuotaGB = 3
	commerceInsertPlan(t, s, thirdPlan)
	third := commerceCreate(t, s, thirdPlan.ID, subID)
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	if subs[0].PlanID != next.ID || subs[0].PeriodEnd != secondEnd || subs[0].UsedBytes != 0 {
		t.Fatalf("due period was not activated atomically %+v", subs[0])
	}
	s.store.AddUsage(subs[0].Username, 222, time.Now())
	if _, err = s.fulfillCommerceOrder(context.Background(), third.ID, "third"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.store.Get(subs[0].Username)
	expectedStart, _ := time.Parse(time.RFC3339, secondEnd)
	if u.UsageBytes != 222 || u.QuotaGB != 2 || u.ExpiresAt != portalTime(expectedStart.AddDate(0, 0, 3)) {
		t.Fatalf("overlap/reset prepaid service %+v", u)
	}
	var start string
	s.store.db.QueryRow(`SELECT period_start FROM subscription_periods WHERE order_id=?`, third.ID).Scan(&start)
	if start != secondEnd {
		t.Fatalf("third period starts %s wanted %s", start, secondEnd)
	}
}

func TestCommerceManagedDeletionAndLiveSlotRouting(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	t.Setenv("PROXY_PUBLIC_HOST", "proxy.example.test")
	o := commerceCreate(t, s, p.ID, "")
	paid, err := s.fulfillCommerceOrder(context.Background(), o.ID, "evt")
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	u, _ := s.store.Get(subs[0].Username)
	rr, _ := doJSON(t, mux, "DELETE", "/api/users/"+u.Username, "admintok", nil)
	if rr.Code != 409 {
		t.Fatalf("managed deletion %d", rr.Code)
	}
	if _, err = s.store.Get(u.Username); err != nil {
		t.Fatal("managed user deleted")
	}
	credential := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/portal/api/subscriptions/"+paid.SubscriptionID+"/credentials", nil)
		r.SetPathValue("id", paid.SubscriptionID)
		r = r.WithContext(context.WithValue(r.Context(), portalIdentityKey{}, portalIdentity{ID: "customer"}))
		out := httptest.NewRecorder()
		s.handlePortalSubscriptionCredentials(out, r)
		return out
	}
	s.devices.Upsert("c3", func(d *Device) error { d.Slot = 2; return nil })
	rr = credential()
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"port":3132`) {
		t.Fatalf("stale port %d %s", rr.Code, rr.Body.String())
	}
	subs, _ = s.customerSubscriptions(context.Background(), "customer")
	if subs[0].Slot != 2 {
		t.Fatalf("stale list slot %d", subs[0].Slot)
	}
	s.devices.Upsert("c3", func(d *Device) error { d.Slot = 0; return nil })
	rr = credential()
	if rr.Code != 409 || strings.Contains(rr.Body.String(), "password") {
		t.Fatalf("unslotted credentials %d %s", rr.Code, rr.Body.String())
	}
	s.store.db.Exec(`DELETE FROM users WHERE username=?`, u.Username)
	if _, err = s.createCommerceOrder(context.Background(), "customer", p.ID, paid.SubscriptionID, "slip", ""); err == nil {
		t.Fatal("orphan renewal accepted")
	}
}

func TestCommerceManagedFinancialGuardsAllowSafeEdits(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	o := commerceCreate(t, s, p.ID, "")
	if _, err := s.fulfillCommerceOrder(context.Background(), o.ID, "paid"); err != nil {
		t.Fatal(err)
	}
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	u, _ := s.store.Get(subs[0].Username)
	before := *u
	for _, body := range []map[string]any{{"rate_mbps": 0.4}, {"quota_gb": 2}, {"expires_at": portalTime(time.Now().AddDate(0, 0, 90))}} {
		rr, _ := doJSON(t, mux, "PUT", "/api/users/"+u.Username, "admintok", body)
		if rr.Code != 409 {
			t.Fatalf("managed financial change %v: %d %s", body, rr.Code, rr.Body.String())
		}
	}
	rr, _ := doJSON(t, mux, "POST", "/api/users/"+u.Username+"/extend", "admintok", map[string]any{"days": 30})
	if rr.Code != 409 {
		t.Fatalf("legacy extend %d", rr.Code)
	}
	r := httptest.NewRequest("POST", "/webhook/payment", strings.NewReader(fmt.Sprintf(`{"username":%q,"days":30}`, u.Username)))
	r.Header.Set("X-Webhook-Token", "paytok")
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, r)
	if rr.Code != 409 {
		t.Fatalf("legacy managed payment %d", rr.Code)
	}
	u, _ = s.store.Get(u.Username)
	if u.RateMbps != before.RateMbps || u.QuotaGB != before.QuotaGB || u.ExpiresAt != before.ExpiresAt {
		t.Fatal("financial guard mutated account")
	}
	parsed, _ := time.Parse(time.RFC3339, u.ExpiresAt)
	rr, _ = doJSON(t, mux, "PUT", "/api/users/"+u.Username, "admintok", map[string]any{"expires_at": parsed.Format("2006-01-02T15:04:05.000Z"), "rate_mbps": u.RateMbps, "quota_gb": u.QuotaGB, "note": "editable", "password": "rotated"})
	if rr.Code != 200 {
		t.Fatalf("same financial values should permit ordinary form edits %d %s", rr.Code, rr.Body.String())
	}
	u, _ = s.store.Get(u.Username)
	if u.Note != "editable" || !verifyPassword(u.PassHash, "rotated") {
		t.Fatal("safe managed edits blocked")
	}
	s.store.AddUsage(u.Username, 99, time.Now())
	rr, _ = doJSON(t, mux, "POST", "/api/users/"+u.Username+"/reset-quota", "admintok", nil)
	if rr.Code != 200 {
		t.Fatalf("explicit quota reset %d", rr.Code)
	}
	u, _ = s.store.Get(u.Username)
	var monthly, used int64
	s.store.db.QueryRow(`SELECT bytes FROM usage_monthly WHERE username=? AND month=?`, u.Username, monthKey(time.Now())).Scan(&monthly)
	s.store.db.QueryRow(`SELECT used_bytes FROM subscriptions WHERE id=?`, u.SubscriptionID).Scan(&used)
	if u.UsageBytes != 0 || used != 0 || monthly != 99 {
		t.Fatalf("quota reset corrupted counters user=%d sub=%d month=%d", u.UsageBytes, used, monthly)
	}
}

func TestCommerceActivationAndRenewalEmailsAfterWaiting(t *testing.T) {
	s, _, fb, p := commerceFixture(t, 0.5)
	t.Setenv("PORTAL_BASE_URL", "https://portal.example.test")
	fb.nodes[0].Online = false
	o := commerceCreate(t, s, p.ID, "")
	if _, err := s.fulfillCommerceOrder(context.Background(), o.ID, "paid"); err != nil {
		t.Fatal(err)
	}
	fb.nodes[0].Online = true
	if err := s.retryPaidCommerce(context.Background(), fb.nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	paid, _ := s.commerceOrder(context.Background(), o.ID)
	renew := commerceCreate(t, s, p.ID, paid.SubscriptionID)
	if _, err := s.fulfillCommerceOrder(context.Background(), renew.ID, "renew"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ order, kind string }{{o.ID, "payment_received"}, {o.ID, "activation"}, {renew.ID, "renewal"}} {
		var encrypted string
		if err := s.store.db.QueryRow(`SELECT body_enc FROM email_outbox WHERE id=?`, tc.kind+":"+tc.order).Scan(&encrypted); err != nil {
			t.Fatal(err)
		}
		body := decryptCred(encrypted)
		if tc.kind != "payment_received" {
			var start, end string
			s.store.db.QueryRow(`SELECT period_start,period_end FROM subscription_periods WHERE order_id=?`, tc.order).Scan(&start, &end)
			if !strings.Contains(body, start) || !strings.Contains(body, end) || !strings.Contains(body, "https://portal.example.test/portal/") {
				t.Fatalf("email missing actual service period/portal %q", body)
			}
			if tc.kind == "renewal" && !strings.Contains(body, "scheduled") {
				t.Fatal("prepaid renewal email claims immediate change")
			}
		}
	}
	u := s.store.List()[0]
	pw := decryptCred(u.CredEnc)
	rows, _ := s.store.db.Query(`SELECT body_enc FROM email_outbox`)
	for rows.Next() {
		var encrypted string
		rows.Scan(&encrypted)
		if strings.Contains(decryptCred(encrypted), pw) {
			rows.Close()
			t.Fatal("email password leak")
		}
	}
	rows.Close()
	if _, err := s.fulfillCommerceOrder(context.Background(), o.ID, "paid"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.fulfillCommerceOrder(context.Background(), renew.ID, "renew"); err != nil {
		t.Fatal(err)
	}
	if commerceCount(t, s, "email_outbox") != 3 {
		t.Fatal("replayed activation/renewal email")
	}
}

func TestCommerceThaiPlanCharacterLimits(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	p.Name = strings.Repeat("ก", 100)
	p.Description = strings.Repeat("ข", 2000)
	if err := validatePlan(&p); err != nil {
		t.Fatalf("valid Thai text rejected: %v", err)
	}
	rr, out := doJSON(t, mux, "POST", "/api/plans", "admintok", p)
	if rr.Code != 201 || out["name"] != p.Name {
		t.Fatalf("Thai plan create %d %s", rr.Code, rr.Body.String())
	}
	for _, tc := range []struct {
		name string
		edit func(*Plan)
	}{{"name101", func(v *Plan) { v.Name += "ค" }}, {"description2001", func(v *Plan) { v.Description += "ค" }}, {"invalidname", func(v *Plan) { v.Name = string([]byte{0xff}) }}, {"invaliddescription", func(v *Plan) { v.Description = string([]byte{0xff}) }}} {
		t.Run(tc.name, func(t *testing.T) {
			v := p
			tc.edit(&v)
			if validatePlan(&v) == nil {
				t.Fatal("invalid text accepted")
			}
		})
	}
	_ = s
}

func TestCommerceMonthlyTrafficSurvivesQuotaPeriodsAndResets(t *testing.T) {
	s, mux, _, p := commerceFixture(t, 0.5)
	first := commerceCreate(t, s, p.ID, "")
	paid, err := s.fulfillCommerceOrder(context.Background(), first.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	renew := commerceCreate(t, s, p.ID, paid.SubscriptionID)
	if _, err = s.fulfillCommerceOrder(context.Background(), renew.ID, "renew"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	base := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	boundary := base.AddDate(0, 0, 10)
	nextMonth := base.AddDate(0, 1, 0)
	end := nextMonth.AddDate(0, 0, 20)
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	name := subs[0].Username
	s.store.db.Exec(`UPDATE subscription_periods SET period_start=?,period_end=? WHERE order_id=?`, portalTime(base), portalTime(boundary), first.ID)
	s.store.db.Exec(`UPDATE subscription_periods SET period_start=?,period_end=? WHERE order_id=?`, portalTime(boundary), portalTime(end), renew.ID)
	s.store.db.Exec(`UPDATE subscriptions SET period_start=?,period_end=? WHERE id=?`, portalTime(base), portalTime(boundary), paid.SubscriptionID)
	s.store.db.Exec(`UPDATE users SET expires_at=? WHERE username=?`, portalTime(end), name)
	s.store.AddUsage(name, 100, base.AddDate(0, 0, 5))
	s.store.AddUsage(name, 200, base.AddDate(0, 0, 15))
	u, _ := s.store.Get(name)
	if u.UsageBytes != 200 {
		t.Fatalf("new period usage %d", u.UsageBytes)
	}
	s.store.Update(name, func(u *User) error { u.UsageBytes = 0; return nil })
	s.store.AddUsage(name, 25, base.AddDate(0, 0, 16))
	s.store.AddUsage(name, 50, nextMonth.AddDate(0, 0, 2))
	u, _ = s.store.Get(name)
	if u.UsageBytes != 75 {
		t.Fatalf("period quota reset at calendar boundary %d", u.UsageBytes)
	}
	var current, next int64
	s.store.db.QueryRow(`SELECT SUM(bytes) FROM usage_monthly WHERE month=?`, monthKey(base)).Scan(&current)
	s.store.db.QueryRow(`SELECT SUM(bytes) FROM usage_monthly WHERE month=?`, monthKey(nextMonth)).Scan(&next)
	if current != 325 || next != 50 {
		t.Fatalf("calendar totals current=%d next=%d", current, next)
	}
	rr, out := doJSON(t, mux, "GET", "/api/overview", "admintok", nil)
	if rr.Code != 200 || out["month_gb"] != float64(325)/(1024*1024*1024) {
		t.Fatalf("overview mixed quota periods: %d %v", rr.Code, out)
	}
}

func TestCommerceMonthlyUsageImportAndAtomicFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.json")
	body, _ := json.Marshal([]*User{{Username: "legacy", Enabled: true, UsageBytes: 42, UsageMonth: monthKey(time.Now())}})
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.db.Close()
	var bytes int64
	store.db.QueryRow(`SELECT bytes FROM usage_monthly WHERE username='legacy'`).Scan(&bytes)
	if bytes != 42 {
		t.Fatalf("legacy import month total %d", bytes)
	}
	store.Update("legacy", func(u *User) error { u.UsageBytes = 0; return nil })
	store.db.QueryRow(`SELECT bytes FROM usage_monthly WHERE username='legacy'`).Scan(&bytes)
	if bytes != 42 {
		t.Fatal("quota reset deleted historical traffic")
	}
	if _, err = store.db.Exec(`CREATE TRIGGER fail_monthly BEFORE UPDATE ON usage_monthly BEGIN SELECT RAISE(ABORT,'injected');END`); err != nil {
		t.Fatal(err)
	}
	store.AddUsage("legacy", 10, time.Now())
	u, _ := store.Get("legacy")
	store.db.QueryRow(`SELECT bytes FROM usage_monthly WHERE username='legacy'`).Scan(&bytes)
	if u.UsageBytes != 0 || bytes != 42 {
		t.Fatalf("non-atomic monthly/quota update %d %d", u.UsageBytes, bytes)
	}
}

func TestCommercePaidRetryFairAcrossRestart(t *testing.T) {
	s, _, fb, p := commerceFixture(t, 0.5)
	fast := p
	fast.ID = "unavailable"
	fast.RateMbps = 10
	commerceInsertPlan(t, s, fast)
	for i := range 25 {
		o := commerceCreate(t, s, fast.ID, "")
		if _, err := s.fulfillCommerceOrder(context.Background(), o.ID, fmt.Sprintf("unavailable%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	fb.nodes[0].Online = false
	eligible := commerceCreate(t, s, p.ID, "")
	if _, err := s.fulfillCommerceOrder(context.Background(), eligible.ID, "eligible"); err != nil {
		t.Fatal(err)
	}
	fb.nodes[0].Online = true
	if _, err := s.store.db.Exec(`UPDATE orders SET last_attempt_at='2000-01-01T00:00:00Z',paid_at='2000-01-01T00:00:00Z' WHERE id<>?`, eligible.ID); err != nil {
		t.Fatal(err)
	}
	s.store.db.Exec(`UPDATE orders SET last_attempt_at='2001-01-01T00:00:00Z',paid_at='2001-01-01T00:00:00Z' WHERE id=?`, eligible.ID)
	if err := s.retryPaidCommerce(context.Background(), fb.nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	if commerceCount(t, s, "subscriptions") != 0 {
		t.Fatal("batch exceeded20 order bound")
	}
	fresh := &server{store: s.store, devices: s.devices}
	if err := fresh.retryPaidCommerce(context.Background(), fb.nodes, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	o, _ := s.commerceOrder(context.Background(), eligible.ID)
	if o.Status != "paid" || commerceCount(t, s, "subscriptions") != 1 {
		t.Fatalf("blocked orders starved eligible %+v", o)
	}
}

func TestCommerceSuspensionDuringCheckoutPreservesPayment(t *testing.T) {
	s, _, fb, p := commerceFixture(t, 0.5)
	initial := commerceCreate(t, s, p.ID, "")
	paid, err := s.fulfillCommerceOrder(context.Background(), initial.ID, "first")
	if err != nil {
		t.Fatal(err)
	}
	renew, err := s.createCommerceOrder(context.Background(), "customer", p.ID, paid.SubscriptionID, "stripe", "")
	if err != nil {
		t.Fatal(err)
	}
	subs, _ := s.customerSubscriptions(context.Background(), "customer")
	name := subs[0].Username
	if err = s.store.Update(name, func(u *User) error { u.Enabled = false; return nil }); err != nil {
		t.Fatal(err)
	}
	late, err := s.fulfillCommerceOrder(context.Background(), renew.ID, "late")
	if err != nil || late.PaidAt == "" || late.Status != "waiting_capacity" {
		t.Fatalf("suspended payment %+v %v", late, err)
	}
	if err = s.retryPaidCommerce(context.Background(), fb.nodes, time.Now()); err != nil {
		t.Fatal(err)
	}
	u, _ := s.store.Get(name)
	subs, _ = s.customerSubscriptions(context.Background(), "customer")
	if u.Enabled || subs[0].Status != "suspended" || commerceCount(t, s, "subscription_periods") != 1 {
		t.Fatal("paid checkout bypassed suspension")
	}
}
