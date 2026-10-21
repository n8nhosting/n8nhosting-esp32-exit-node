package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

func registerPaymentRoutes(mux *http.ServeMux, s *server) {
	mux.HandleFunc("POST /portal/api/orders/{id}/checkout", s.requirePortal(s.handleStripeCheckout))
	mux.HandleFunc("POST /portal/api/orders/{id}/slip", s.requirePortal(s.handleSlipUpload))
	mux.HandleFunc("POST /webhook/stripe", s.handleStripeWebhook)
	mux.HandleFunc("GET /api/approvals/{id}/file", s.requireAdmin(s.handleSlipFile))
}

func (s *server) handleStripeCheckout(w http.ResponseWriter, r *http.Request) {
	if !stripeConfigured() {
		portalError(w, 503, "Stripe is not configured")
		return
	}
	base, ok := portalBase()
	if !ok {
		portalError(w, 503, "portal URL is not configured")
		return
	}
	id := r.PathValue("id")
	order, err := s.commerceOrder(r.Context(), id)
	identity := portalCustomer(r)
	if err != nil || order.CustomerID != identity.ID {
		portalError(w, 404, "order not found")
		return
	}
	expiry, err := time.Parse(time.RFC3339, order.ReservationExpiresAt)
	if order.Method != "stripe" || order.Status != "pending" || err != nil || !expiry.After(time.Now()) || order.AmountSatang <= 0 {
		portalError(w, 409, "order reservation expired or not payable")
		return
	}
	if order.AmountSatang < 1000 || order.AmountSatang > 99999999 {
		portalError(w, 409, "Stripe amount must be between 10 and 999999.99 THB")
		return
	}
	if !s.portalRate(r.Context(), "checkout:"+identity.ID, 20, 15*time.Minute) {
		portalError(w, 429, "please try again later")
		return
	}
	// Preserve the complete request across retries, including expiry, so Stripe's
	// idempotency check never sees a changed payload after an ambiguous timeout.
	checkoutExpiry := time.Now().Add(31 * time.Minute).Unix()
	if _, err := s.store.DB().ExecContext(r.Context(), `INSERT INTO stripe_checkout_requests(order_id,expires_at) VALUES(?,?) ON CONFLICT(order_id) DO NOTHING`, id, checkoutExpiry); err != nil {
		portalError(w, 503, "checkout unavailable")
		return
	}
	if err := s.store.DB().QueryRowContext(r.Context(), `SELECT expires_at FROM stripe_checkout_requests WHERE order_id=?`, id).Scan(&checkoutExpiry); err != nil || checkoutExpiry <= time.Now().Unix() {
		portalError(w, 409, "Stripe checkout expired; create a new order")
		return
	}
	// The amount comes only from the immutable server-side order snapshot.
	root := base.Scheme + "://" + base.Host + "/portal/"
	form := url.Values{
		"mode":                                          {"payment"},
		"allowed_payment_method_types[0]":               {"card"},
		"allowed_payment_method_types[1]":               {"promptpay"},
		"line_items[0][quantity]":                       {"1"},
		"line_items[0][price_data][currency]":           {"thb"},
		"line_items[0][price_data][unit_amount]":        {strconv.FormatInt(order.AmountSatang, 10)},
		"line_items[0][price_data][product_data][name]": {"Residential proxy subscription"},
		"customer_email":                                {identity.Email},
		"client_reference_id":                           {id},
		"metadata[order_id]":                            {id},
		"metadata[customer_id]":                         {identity.ID},
		"success_url":                                   {root + "#/orders/" + url.PathEscape(id)},
		"cancel_url":                                    {root + "#/orders/" + url.PathEscape(id)},
		"locale":                                        {"th"},
		"expires_at":                                    {strconv.FormatInt(checkoutExpiry, 10)},
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		portalError(w, 503, "checkout unavailable")
		return
	}
	req.SetBasicAuth(os.Getenv("STRIPE_SECRET_KEY"), "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "checkout/"+id)
	// Dynamic payment methods are filtered to card/PromptPay. Both must be
	// enabled in the merchant's Stripe Dashboard; absent methods are not invented.
	req.Header.Set("Stripe-Version", "2026-09-30.endive")
	resp, err := portalHTTPClient.Do(req)
	if err != nil {
		portalError(w, 502, "payment provider unavailable")
		return
	}
	defer resp.Body.Close()
	var result struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	if resp.StatusCode/100 != 2 || json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&result) != nil || result.ID == "" {
		portalError(w, 502, "payment provider unavailable")
		return
	}
	checkout, err := url.Parse(result.URL)
	if err != nil || checkout.Scheme != "https" || checkout.Host != "checkout.stripe.com" || checkout.User != nil {
		portalError(w, 502, "payment provider invalid response")
		return
	}
	if err := s.linkCommercePaymentSession(r.Context(), id, result.ID); err != nil {
		portalError(w, 409, "checkout could not be linked; please retry")
		return
	}
	writeJSON(w, 200, map[string]string{"url": result.URL, "session_id": result.ID})
}

// Stripe signs exact raw bytes, not parsed/re-encoded JSON. Accept all v1
// signatures during key rotation, but reject replay outside a five-minute window.
func verifyStripeSignature(raw []byte, header, secret string, now time.Time) bool {
	if secret == "" {
		return false
	}
	var timestamp string
	signatures := []string{}
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if k == "t" {
			if timestamp != "" {
				return false
			}
			timestamp = v
		}
		if k == "v1" {
			signatures = append(signatures, v)
		}
	}
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || sec < now.Add(-5*time.Minute).Unix() || sec > now.Add(5*time.Minute).Unix() {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + "."))
	mac.Write(raw)
	expected := mac.Sum(nil)
	for _, candidate := range signatures {
		decoded, err := hex.DecodeString(candidate)
		if err == nil && hmac.Equal(expected, decoded) {
			return true
		}
	}
	return false
}

type stripeCheckoutEvent struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	LiveMode bool   `json:"livemode"`
	Data     struct {
		Object struct {
			ID                string `json:"id"`
			ClientReferenceID string `json:"client_reference_id"`
			AmountTotal       int64  `json:"amount_total"`
			Currency          string `json:"currency"`
			PaymentStatus     string `json:"payment_status"`
			Metadata          struct {
				OrderID    string `json:"order_id"`
				CustomerID string `json:"customer_id"`
			} `json:"metadata"`
		} `json:"object"`
	} `json:"data"`
}

func (s *server) handleStripeWebhook(w http.ResponseWriter, r *http.Request) {
	if !stripeConfigured() {
		portalError(w, 503, "Stripe is not configured")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		portalError(w, 400, "invalid webhook")
		return
	}
	if !verifyStripeSignature(raw, r.Header.Get("Stripe-Signature"), os.Getenv("STRIPE_WEBHOOK_SECRET"), time.Now()) {
		portalError(w, 400, "invalid webhook signature")
		return
	}
	var event stripeCheckoutEvent
	if json.Unmarshal(raw, &event) != nil || event.ID == "" {
		portalError(w, 400, "invalid webhook")
		return
	}
	if event.Type != "checkout.session.completed" && event.Type != "checkout.session.async_payment_succeeded" {
		writeJSON(w, 200, map[string]bool{"received": true})
		return
	}
	obj := event.Data.Object
	if obj.PaymentStatus != "paid" {
		writeJSON(w, 200, map[string]bool{"received": true})
		return
	}
	// Test and live credentials must never settle each other's orders.
	live := stripeLiveMode()
	if live != event.LiveMode {
		portalError(w, 400, "payment mode mismatch")
		return
	}
	order, err := s.commerceOrder(r.Context(), obj.Metadata.OrderID)
	if err != nil || order.Method != "stripe" || order.CustomerID != obj.Metadata.CustomerID || order.ID != obj.ClientReferenceID || order.AmountSatang != obj.AmountTotal || obj.Currency != "thb" || obj.ID == "" {
		portalError(w, 400, "payment does not match order")
		return
	}
	var linked string
	if err := s.store.DB().QueryRowContext(r.Context(), `SELECT stripe_session_id FROM orders WHERE id=?`, order.ID).Scan(&linked); err != nil || linked == "" || linked != obj.ID {
		portalError(w, 400, "payment session does not match order")
		return
	}
	// Core atomically records the unique event and provisions/rechecks capacity.
	if _, err := s.fulfillCommerceOrder(r.Context(), order.ID, "stripe:"+event.ID); err != nil {
		portalError(w, 503, "payment processing unavailable")
		return
	}
	writeJSON(w, 200, map[string]bool{"received": true})
}
