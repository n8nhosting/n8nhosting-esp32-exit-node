package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmailOutboxConfiguredOnlyRetryAndRestartDedupe(t *testing.T) {
	s, mux := portalTestServer(t)
	now := time.Now()
	secretBody := "private-token-and-credentials-never-log"
	if err := s.queueEmail(context.Background(), "order-notice", "buyer", "activated", "buyer@example.test", "Activated", secretBody); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	s.store.DB().QueryRow(`SELECT body_enc FROM email_outbox`).Scan(&encrypted)
	if strings.Contains(encrypted, secretBody) || decryptCred(encrypted) != secretBody {
		t.Fatal("outbox body not encrypted")
	}
	calls := 0
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.String() != "https://api.resend.com/emails" || r.Header.Get("Idempotency-Key") != "order-notice" || r.Header.Get("Authorization") != "Bearer re_secret_fixture" {
			t.Fatal("resend request contract")
		}
		var payload map[string]any
		json.NewDecoder(r.Body).Decode(&payload)
		if payload["text"] != secretBody {
			t.Fatal("email body changed on retry")
		}
		if calls == 1 {
			return portalTestResponse(429, `{"message":"do not log this secret"}`), nil
		}
		return portalTestResponse(200, `{"id":"email_fixture"}`), nil
	})
	if err := s.drainEmailOutbox(context.Background(), now); err != nil || calls != 0 {
		t.Fatal("unconfigured email sent")
	}
	configurePortalTest(t)
	if err := s.drainEmailOutbox(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var state, problem string
	var attempts int
	s.store.DB().QueryRow(`SELECT state,attempts,last_error FROM email_outbox`).Scan(&state, &attempts, &problem)
	if state != "pending" || attempts != 1 || problem != "email_provider_http_429" {
		t.Fatalf("retry record: %s %d %s", state, attempts, problem)
	}
	restarted := &server{store: s.store}
	if err := restarted.drainEmailOutbox(context.Background(), now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := restarted.drainEmailOutbox(context.Background(), now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var provider, sent string
	s.store.DB().QueryRow(`SELECT state,attempts,provider_id,body_enc,sent_at FROM email_outbox`).Scan(&state, &attempts, &provider, &encrypted, &sent)
	if calls != 2 || state != "sent" || attempts != 2 || provider != "email_fixture" || encrypted != "" || sent == "" {
		t.Fatalf("durable send: %d %s %d %s %s", calls, state, attempts, provider, sent)
	}
	req := httptest.NewRequest("GET", "/api/email-log", nil)
	req.Header.Set("Authorization", "Bearer admin-test")
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 || strings.Contains(rr.Body.String(), secretBody) || strings.Contains(rr.Body.String(), "do not log") || !strings.Contains(rr.Body.String(), "to_email") || !strings.Contains(rr.Body.String(), "template") {
		t.Fatalf("safe email log: %d %s", rr.Code, rr.Body.String())
	}
}

func TestEmailOutboxConcurrentClaimsAndUnknownOutcomeCutoff(t *testing.T) {
	s, _ := portalTestServer(t)
	configurePortalTest(t)
	now := time.Now()
	if err := s.queueEmail(context.Background(), "once", "buyer", "notice", "buyer@example.test", "Notice", "body"); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(10 * time.Millisecond)
		return portalTestResponse(200, `{"id":"email_once"}`), nil
	})
	var wg sync.WaitGroup
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errors <- s.drainEmailOutbox(context.Background(), now) }()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("claim duplicate sends %d", calls.Load())
	}
	if err := s.queueEmail(context.Background(), "uncertain", "buyer", "notice", "buyer@example.test", "Notice", "never resend beyond provider dedupe window"); err != nil {
		t.Fatal(err)
	}
	portalSQL(t, s, `UPDATE email_outbox SET state='sending',attempts=1,claim_token='crashed',claimed_until=?,first_attempt_at=? WHERE id='uncertain'`, portalTime(now.Add(-time.Hour)), portalTime(now.Add(-24*time.Hour)))
	if err := s.drainEmailOutbox(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var state string
	s.store.DB().QueryRow(`SELECT state FROM email_outbox WHERE id='uncertain'`).Scan(&state)
	if state != "needs_review" || calls.Load() != 1 {
		t.Fatalf("ambiguous resend: %s calls%d", state, calls.Load())
	}
}

func TestEmailExpiredMagicLinkAndClaimOwnership(t *testing.T) {
	s, _ := portalTestServer(t)
	configurePortalTest(t)
	now := time.Now()
	if err := s.queueEmail(context.Background(), "expired-link", "buyer", "magic_link", "buyer@example.test", "Sign in", "secret link"); err != nil {
		t.Fatal(err)
	}
	portalSQL(t, s, `UPDATE email_outbox SET created_at=?`, portalTime(now.Add(-16*time.Minute)))
	portalTestClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("sent an expired magic link")
		return nil, fmt.Errorf("unreachable")
	})
	if err := s.drainEmailOutbox(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var state, body string
	s.store.DB().QueryRow(`SELECT state,body_enc FROM email_outbox`).Scan(&state, &body)
	if state != "expired" || body != "" {
		t.Fatalf("expired link retained: %s", state)
	}
	if err := s.queueEmail(context.Background(), "reclaimed", "buyer", "notice", "buyer@example.test", "Notice", "private"); err != nil {
		t.Fatal(err)
	}
	first, ok, err := s.claimEmail(context.Background(), now)
	if err != nil || !ok {
		t.Fatal(err)
	}
	second, ok, err := s.claimEmail(context.Background(), now.Add(3*time.Minute))
	if err != nil || !ok || first.Claim == second.Claim {
		t.Fatalf("reclaim: %+v %+v %v", first, second, err)
	}
	res, err := s.store.DB().Exec(`UPDATE email_outbox SET state='sent' WHERE id=? AND claim_token=?`, first.ID, first.Claim)
	if err != nil {
		t.Fatal(err)
	}
	affected, _ := res.RowsAffected()
	if affected != 0 {
		t.Fatal("old worker acknowledged new claim")
	}
}

func TestEmailNotificationsDurablePerPeriodDedupe(t *testing.T) {
	s, _ := portalTestServer(t)
	portalFixtureIdentity(t, s, "buyer")
	now := time.Now()
	start := portalTime(now.Add(-29 * 24 * time.Hour))
	end := portalTime(now.Add(12 * time.Hour))
	portalSQL(t, s, `INSERT INTO subscriptions(id,customer_id,plan_id,username,device_mac,slot,period_start,period_end,quota_bytes,used_bytes,status) VALUES('sub_fixture','buyer','plan','proxy-user','private-mac',1,?,?,1000,800,'active')`, start, end)
	for i := 0; i < 2; i++ {
		if err := s.scheduleCustomerEmails(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	s.store.DB().QueryRow(`SELECT count(*) FROM email_outbox`).Scan(&n)
	if n != 2 {
		t.Fatalf("reminder replay: %d", n)
	}
	portalSQL(t, s, `UPDATE subscriptions SET used_bytes=1000`)
	if err := s.scheduleCustomerEmails(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s.store.DB().QueryRow(`SELECT count(*) FROM email_outbox`).Scan(&n)
	if n != 3 {
		t.Fatalf("quota100 notice missing %d", n)
	}
	portalSQL(t, s, `UPDATE subscriptions SET period_start=?,period_end=?,used_bytes=800`, portalTime(now), portalTime(now.Add(12*time.Hour)))
	if err := s.scheduleCustomerEmails(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	s.store.DB().QueryRow(`SELECT count(*) FROM email_outbox`).Scan(&n)
	if n != 4 {
		t.Fatalf("new plan period reminders suppressed %d", n)
	}
	rows, err := s.store.DB().Query(`SELECT body_enc FROM email_outbox`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		rows.Scan(&body)
		if strings.Contains(decryptCred(body), "private-mac") {
			t.Fatal("fleet identity in customer notification")
		}
	}
}

func TestEmailPrepaidRenewalDoesNotClaimServiceIsExpiring(t *testing.T) {
	s, _ := portalTestServer(t)
	portalFixtureIdentity(t, s, "buyer")
	now := time.Now()
	currentEnd := portalTime(now.Add(12 * time.Hour))
	paidThrough := portalTime(now.Add(30 * 24 * time.Hour))
	portalSQL(t, s, `INSERT INTO users(username,enabled,expires_at,subscription_id) VALUES('prepaid-user',1,?,'sub_prepaid')`, paidThrough)
	portalSQL(t, s, `INSERT INTO subscriptions(id,customer_id,plan_id,username,device_mac,slot,period_start,period_end,quota_bytes,used_bytes,status) VALUES('sub_prepaid','buyer','plan','prepaid-user','private-mac',1,?,?,1000,800,'active')`, portalTime(now.Add(-29*24*time.Hour)), currentEnd)
	if err := s.scheduleCustomerEmails(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var count int
	s.store.DB().QueryRow(`SELECT count(*) FROM email_outbox WHERE kind LIKE 'expiry_%' OR kind='expired'`).Scan(&count)
	if count != 0 {
		t.Fatal("current quota boundary incorrectly announced as service expiry")
	}
	var kind, body string
	if err := s.store.DB().QueryRow(`SELECT kind,body_enc FROM email_outbox`).Scan(&kind, &body); err != nil {
		t.Fatal(err)
	}
	if kind != "quota_80" || !strings.Contains(decryptCred(body), paidThrough) || !strings.Contains(decryptCred(body), currentEnd) {
		t.Fatal("quota and prepaid service dates conflated")
	}
}

func TestEmailCancellationKeepsOutboxAndReleasesClaim(t *testing.T) {
	s, _ := portalTestServer(t)
	configurePortalTest(t)
	if err := s.queueEmail(context.Background(), "cancel-notice", "buyer", "notice", "buyer@example.test", "Notice", "preserve encrypted payload"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		close(entered)
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.drainEmailOutbox(ctx, time.Now()) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("email worker did not stop")
	}
	var state, claim, body string
	var attempts int
	if err := s.store.DB().QueryRow(`SELECT state,claim_token,body_enc,attempts FROM email_outbox WHERE id='cancel-notice'`).Scan(&state, &claim, &body, &attempts); err != nil {
		t.Fatal(err)
	}
	if state != "pending" || claim != "" || attempts != 1 || decryptCred(body) != "preserve encrypted payload" {
		t.Fatalf("cancel dropped message: %s claim%s attempts%d", state, claim, attempts)
	}
}

func TestEmailMagicLinkPriorityAheadOfReminderBacklog(t *testing.T) {
	s, _ := portalTestServer(t)
	now := time.Now()
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("old-reminder-%02d", i)
		if err := s.queueEmail(context.Background(), id, "buyer", "expiry_7", "buyer@example.test", "Reminder", "old reminder"); err != nil {
			t.Fatal(err)
		}
		portalSQL(t, s, `UPDATE email_outbox SET created_at=?,next_at=? WHERE id=?`, portalTime(now.Add(-time.Hour)), portalTime(now.Add(-time.Hour)), id)
	}
	if err := s.queueEmail(context.Background(), "fresh-login", "buyer", "magic_link", "buyer@example.test", "Sign in", "unexpired link"); err != nil {
		t.Fatal(err)
	}
	// Enqueue timestamps use the real clock. Claim after all inserts so the
	// fresh link is due even when populating the backlog crosses a second.
	message, ok, err := s.claimEmail(context.Background(), time.Now())
	if err != nil || !ok || message.ID != "fresh-login" {
		t.Fatalf("short-lived login starved behind reminders: %+v %v", message, err)
	}
}
