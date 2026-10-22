package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
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

func portalFixtureOrder(t *testing.T, s *server, id, customer, method string) {
	t.Helper()
	p := Plan{ID: "p_fixture", Name: "Shared", Days: 30, Kind: "shared", PriceSatang: 4900, Active: true}
	raw, _ := json.Marshal(p)
	portalSQL(t, s, `INSERT INTO orders(id,customer_id,plan_id,plan_snapshot,amount_satang,method,status,reservation_expires_at,created_at,stripe_session_id) VALUES(?,?,?,?,?,?,'pending',?,?,?)`, id, customer, p.ID, string(raw), p.PriceSatang, method, portalTime(time.Now().Add(15*time.Minute)), portalTime(time.Now()), "")
}
func stripeTestSignature(raw []byte, secret string, now time.Time) string {
	ts := fmt.Sprint(now.Unix())
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "."))
	mac.Write(raw)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
func stripeFixtureEvent(orderID, customerID string) map[string]any {
	return map[string]any{"id": "evt_fixture", "type": "checkout.session.completed", "livemode": false, "data": map[string]any{"object": map[string]any{"id": "cs_fixture", "client_reference_id": orderID, "amount_total": 4900, "currency": "thb", "payment_status": "paid", "metadata": map[string]any{"order_id": orderID, "customer_id": customerID}}}}
}
func stripeWebhookRequest(mux http.Handler, raw []byte, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "https://portal.test/webhook/stripe", strings.NewReader(string(raw)))
	req.Header.Set("Stripe-Signature", signature)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

func TestStripeSignatureRawBodyTimestampAndRotation(t *testing.T) {
	now := time.Now()
	raw := []byte(`{"a":1}`)
	secret := "whsec_fixture"
	signed := stripeTestSignature(raw, secret, now)
	if !verifyStripeSignature(raw, signed, secret, now) {
		t.Fatal("valid signature rejected")
	}
	if !verifyStripeSignature(raw, strings.Replace(signed, ",v1=", ",v1=00,v1=", 1), secret, now) {
		t.Fatal("rotating signatures rejected")
	}
	for _, tc := range []struct {
		body           []byte
		signature, key string
	}{{[]byte(`{"a": 1}`), signed, secret}, {raw, stripeTestSignature(raw, secret, now.Add(-301*time.Second)), secret}, {raw, stripeTestSignature(raw, secret, now.Add(301*time.Second)), secret}, {raw, signed + ",t=1", secret}, {raw, signed, "wrong"}, {raw, "v1=abc", secret}, {raw, signed, ""}} {
		if verifyStripeSignature(tc.body, tc.signature, tc.key, now) {
			t.Fatal("invalid signature accepted")
		}
	}
}

func TestStripeCheckoutServerAmountOwnershipAndStableRetry(t *testing.T) {
	s, mux := portalTestServer(t)
	cookie, csrf := portalFixtureIdentity(t, s, "buyer")
	otherCookie, otherCSRF := portalFixtureIdentity(t, s, "other")
	portalFixtureOrder(t, s, "ord_fixture", "buyer", "stripe")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_fixture")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_fixture")
	calls := 0
	firstBody := ""
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.stripe.com/v1/checkout/sessions" {
			t.Fatalf("unexpectedURL %s", r.URL)
		}
		if r.Header.Get("Idempotency-Key") != "checkout/ord_fixture" {
			t.Fatal("missing idempotency")
		}
		r.ParseForm()
		encoded := r.Form.Encode()
		if firstBody == "" {
			firstBody = encoded
		} else if encoded != firstBody {
			t.Fatal("retry changed immutable Stripe payload")
		}
		if r.Header.Get("Stripe-Version") != "2026-09-30.endive" || r.Form.Get("payment_method_types[0]") != "" || r.Form.Get("allowed_payment_method_types[0]") != "card" || r.Form.Get("line_items[0][price_data][unit_amount]") != "4900" || r.Form.Get("line_items[0][price_data][currency]") != "thb" || r.Form.Get("allowed_payment_method_types[1]") != "promptpay" || r.Form.Get("metadata[customer_id]") != "buyer" {
			t.Fatalf("checkout contract %v", r.Form)
		}
		return portalTestResponse(200, `{"id":"cs_fixture","url":"https://checkout.stripe.com/c/pay/cs_fixture"}`), nil
	})
	rr := portalRequest(mux, "POST", "/portal/api/orders/ord_fixture/checkout", `{"amount_satang":1}`, otherCookie, otherCSRF)
	if rr.Code != 404 || calls != 0 {
		t.Fatal("crosscustomer checkout")
	}
	for i := 0; i < 2; i++ {
		rr = portalRequest(mux, "POST", "/portal/api/orders/ord_fixture/checkout", `{}`, cookie, csrf)
		if rr.Code != 200 {
			t.Fatalf("checkout: %d %s", rr.Code, rr.Body.String())
		}
	}
	var session string
	s.store.DB().QueryRow(`SELECT stripe_session_id FROM orders WHERE id='ord_fixture'`).Scan(&session)
	if session != "cs_fixture" {
		t.Fatal("provider session not persisted")
	}
	portalSQL(t, s, `UPDATE orders SET reservation_expires_at=? WHERE id='ord_fixture'`, portalTime(time.Now().Add(-time.Second)))
	rr = portalRequest(mux, "POST", "/portal/api/orders/ord_fixture/checkout", `{}`, cookie, csrf)
	if rr.Code != 409 || calls != 2 {
		t.Fatal("expired reservation charged")
	}
}

func TestStripeWebhookMismatchReplayAndPaidWaitingCapacity(t *testing.T) {
	s, mux := portalTestServer(t)
	portalFixtureIdentity(t, s, "buyer")
	portalFixtureOrder(t, s, "ord_fixture", "buyer", "stripe")
	portalSQL(t, s, `UPDATE orders SET stripe_session_id='cs_fixture'`)
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_fixture")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_fixture")
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
	defer broker.Close()
	t.Setenv("BROKER_STATUS_URL", broker.URL)
	for _, field := range []string{"amount_total", "currency", "id", "client_reference_id", "customer_id", "livemode"} {
		t.Run(field, func(t *testing.T) {
			event := stripeFixtureEvent("ord_fixture", "buyer")
			obj := event["data"].(map[string]any)["object"].(map[string]any)
			switch field {
			case "amount_total":
				obj[field] = 1
			case "customer_id":
				obj["metadata"].(map[string]any)[field] = "other"
			case "livemode":
				event[field] = true
			default:
				obj[field] = "mismatch"
			}
			raw, _ := json.Marshal(event)
			rr := stripeWebhookRequest(mux, raw, stripeTestSignature(raw, "whsec_fixture", time.Now()))
			if rr.Code != 400 {
				t.Fatalf("mismatch %s got %d %s", field, rr.Code, rr.Body.String())
			}
		})
	}
	var n int
	s.store.DB().QueryRow(`SELECT count(*) FROM payment_events`).Scan(&n)
	if n != 0 {
		t.Fatal("invalid payment persisted")
	}
	event := stripeFixtureEvent("ord_fixture", "buyer")
	raw, _ := json.Marshal(event)
	tampered := append([]byte(" "), raw...)
	if rr := stripeWebhookRequest(mux, tampered, stripeTestSignature(raw, "whsec_fixture", time.Now())); rr.Code != 400 {
		t.Fatal("raw signature verification bypassed")
	}
	var callbacks sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 4)
	for i := 0; i < 4; i++ {
		callbacks.Add(1)
		go func() {
			defer callbacks.Done()
			results <- stripeWebhookRequest(mux, raw, stripeTestSignature(raw, "whsec_fixture", time.Now()))
		}()
	}
	callbacks.Wait()
	close(results)
	for rr := range results {
		if rr.Code != 200 {
			t.Fatalf("paid webhook: %d %s", rr.Code, rr.Body.String())
		}
	}
	order, err := s.commerceOrder(httptest.NewRequest("GET", "/", nil).Context(), "ord_fixture")
	if err != nil || order.PaidAt == "" || order.Status != "waiting_capacity" {
		t.Fatalf("lost funded order: %+v %v", order, err)
	}
	s.store.DB().QueryRow(`SELECT count(*) FROM payment_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("replay not idempotent %d", n)
	}
	s.store.DB().QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	if n != 0 {
		t.Fatal("provisioned without capacity")
	}
}

func configureManualTest(t *testing.T) {
	t.Helper()
	t.Setenv("PAYMENT_BANK_NAME", "Fixture Bank")
	t.Setenv("PAYMENT_ACCOUNT_NUMBER", "000-000-0000")
	t.Setenv("PAYMENT_ACCOUNT_NAME", "Fixture Merchant")
}

func TestSlipUploadPrivateBoundedAndTenantOwned(t *testing.T) {
	s, mux := portalTestServer(t)
	configureManualTest(t)
	cookie, csrf := portalFixtureIdentity(t, s, "buyer")
	otherCookie, otherCSRF := portalFixtureIdentity(t, s, "other")
	portalFixtureOrder(t, s, "ord_slip", "buyer", "slip")
	portalFixtureOrder(t, s, "ord_duplicate", "buyer", "slip")
	body := "%PDF-1.4\nfixture payment slip\n%%EOF"
	rr := portalRequest(mux, "POST", "/portal/api/orders/ord_slip/slip", body, otherCookie, otherCSRF)
	if rr.Code != 404 {
		t.Fatal("crosscustomer slip")
	}
	rr = portalRequest(mux, "POST", "/portal/api/orders/ord_slip/slip", "<html>bad</html>", cookie, csrf)
	if rr.Code != 400 {
		t.Fatal("HTML slip accepted")
	}
	rr = portalRequest(mux, "POST", "/portal/api/orders/ord_slip/slip", strings.Repeat("a", slipMaxBytes+1), cookie, csrf)
	if rr.Code != 400 {
		t.Fatal("oversize slip accepted")
	}
	rr = portalRequest(mux, "POST", "/portal/api/orders/ord_slip/slip", body, cookie, csrf)
	if rr.Code != 201 || !strings.Contains(rr.Body.String(), "manual_required") {
		t.Fatalf("slip: %d %s", rr.Code, rr.Body.String())
	}
	var file string
	s.store.DB().QueryRow(`SELECT file FROM slips WHERE order_id='ord_slip'`).Scan(&file)
	if file == "" || strings.Contains(rr.Body.String(), file) {
		t.Fatal("private path exposed")
	}
	stored, err := os.ReadFile(filepath.Join(slipStorageDir(), file))
	if err != nil || string(stored) != body {
		t.Fatal("private attachment lost")
	}
	rr = portalRequest(mux, "POST", "/portal/api/orders/ord_duplicate/slip", body, cookie, csrf)
	if rr.Code != 409 {
		t.Fatalf("same slip reused: %d %s", rr.Code, rr.Body.String())
	}
	entries, _ := os.ReadDir(slipStorageDir())
	if len(entries) != 1 {
		t.Fatal("failed metadata upload leaked file")
	}
	rr = portalRequest(mux, "GET", "/api/approvals/ord_slip/file", "", cookie, "")
	if rr.Code != 401 {
		t.Fatal("customer downloaded admin slip")
	}
	req := httptest.NewRequest("GET", "/api/approvals/ord_slip/file", nil)
	req.Header.Set("Authorization", "Bearer admin-test")
	download := httptest.NewRecorder()
	mux.ServeHTTP(download, req)
	if download.Code != 200 || download.Body.String() != body || download.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(download.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("admin download %d %v", download.Code, download.Header())
	}
	if _, err := slipPath("../../escape.pdf"); err == nil {
		t.Fatal("path traversal accepted")
	}
}

// A submitted slip must be visible in the customer's order list, or the
// portal keeps offering the upload and the customer may transfer again. The
// order itself stays pending so its seat reservation is unchanged.
func TestPortalOrderListShowsSubmittedSlipWhileSeatStaysReserved(t *testing.T) {
	s, mux := portalTestServer(t)
	configureManualTest(t)
	cookie, csrf := portalFixtureIdentity(t, s, "buyer")
	portalFixtureOrder(t, s, "ord_slip", "buyer", "slip")
	portalFixtureOrder(t, s, "ord_open", "buyer", "slip")
	listed := func() map[string]CommerceOrder {
		t.Helper()
		rr := portalRequest(mux, "GET", "/portal/api/orders", "", cookie, "")
		var body struct {
			Orders []CommerceOrder `json:"orders"`
		}
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &body) != nil {
			t.Fatalf("orders: %d %s", rr.Code, rr.Body.String())
		}
		out := map[string]CommerceOrder{}
		for _, o := range body.Orders {
			out[o.ID] = o
		}
		return out
	}
	if o := listed()["ord_slip"]; o.SlipStatus != "" {
		t.Fatalf("slip state before upload: %q", o.SlipStatus)
	}
	rr := portalRequest(mux, "POST", "/portal/api/orders/ord_slip/slip", "%PDF-1.4\nfixture payment slip\n%%EOF", cookie, csrf)
	if rr.Code != 201 {
		t.Fatalf("slip: %d %s", rr.Code, rr.Body.String())
	}
	got := listed()
	if o := got["ord_slip"]; o.SlipStatus != "awaiting_review" || o.Status != "pending" || o.PaidAt != "" {
		t.Fatalf("submitted slip not listed or seat released: %+v", o)
	}
	if o := got["ord_open"]; o.SlipStatus != "" {
		t.Fatalf("slip state leaked to another order: %q", o.SlipStatus)
	}
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
	defer broker.Close()
	t.Setenv("BROKER_STATUS_URL", broker.URL)
	if _, err := s.reviewCommerceOrder(context.Background(), "ord_slip", false, "fixture"); err != nil {
		t.Fatal(err)
	}
	if o := listed()["ord_slip"]; o.SlipStatus != "reviewed" || o.Status != "rejected" {
		t.Fatalf("reviewed slip: %+v", o)
	}
}

func TestStripeExpiredCheckoutCannotRenewReservationButLatePaymentIsHonored(t *testing.T) {
	s, mux := portalTestServer(t)
	portalFixtureIdentity(t, s, "buyer")
	portalFixtureOrder(t, s, "ord_expired", "buyer", "stripe")
	t.Setenv("STRIPE_SECRET_KEY", "sk_test_fixture")
	t.Setenv("STRIPE_WEBHOOK_SECRET", "whsec_fixture")
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`[]`)) }))
	defer broker.Close()
	t.Setenv("BROKER_STATUS_URL", broker.URL)
	oldReservation := portalTime(time.Now().Add(-20 * time.Minute))
	portalSQL(t, s, `UPDATE orders SET status='waiting_capacity',stripe_session_id='cs_fixture',reservation_expires_at=? WHERE id='ord_expired'`, oldReservation)
	portalSQL(t, s, `INSERT INTO stripe_checkout_requests(order_id,expires_at) VALUES('ord_expired',?)`, time.Now().Add(-time.Minute).Unix())
	request := func(action string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/orders/ord_expired/"+action, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer admin-test")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}
	rr := request("retry")
	if rr.Code != 409 || !strings.Contains(rr.Body.String(), "create a new order") {
		t.Fatalf("expired checkout was made payable again: %d %s", rr.Code, rr.Body.String())
	}
	order, err := s.commerceOrder(context.Background(), "ord_expired")
	if err != nil || order.ReservationExpiresAt != oldReservation || order.StripeSessionID != "cs_fixture" || order.PaidAt != "" {
		t.Fatalf("retry changed payment evidence: %+v %v", order, err)
	}
	rr = request("cancel")
	if rr.Code != 200 {
		t.Fatalf("cancel: %d %s", rr.Code, rr.Body.String())
	}
	event := stripeFixtureEvent("ord_expired", "buyer")
	raw, _ := json.Marshal(event)
	rr = stripeWebhookRequest(mux, raw, stripeTestSignature(raw, "whsec_fixture", time.Now()))
	if rr.Code != 200 {
		t.Fatalf("late confirmed funds rejected: %d %s", rr.Code, rr.Body.String())
	}
	order, err = s.commerceOrder(context.Background(), "ord_expired")
	if err != nil || order.PaidAt == "" || order.Status != "waiting_capacity" || order.StripeSessionID != "cs_fixture" {
		t.Fatalf("late paid order lost: %+v %v", order, err)
	}
}
