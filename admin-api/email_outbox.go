package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

// The deterministic ID is also the provider's idempotency key. Sensitive link
// text is encrypted at rest and excluded from logs and administrative responses.
func queueEmailConn(ctx context.Context, c *sql.Conn, id, customerID, kind, to, subject, text string) error {
	now := portalTime(time.Now())
	_, err := c.ExecContext(ctx, `INSERT INTO email_outbox(id,customer_id,kind,to_email,subject,body_enc,state,attempts,next_at,created_at) VALUES(?,?,?,?,?,?,'pending',0,?,?) ON CONFLICT(id) DO NOTHING`, id, customerID, kind, to, subject, encryptCred(text), now, now)
	return err
}

func (s *server) queueEmail(ctx context.Context, id, customerID, kind, to, subject, text string) error {
	return inTx(ctx, s.store.DB(), func(c *sql.Conn) error { return queueEmailConn(ctx, c, id, customerID, kind, to, subject, text) })
}

type outboxMessage struct {
	ID, CustomerID, Kind, To, Subject, Body, Claim, Created, FirstAttempt string
	Attempts                                                              int
}

func (s *server) claimEmail(ctx context.Context, now time.Time) (outboxMessage, bool, error) {
	var m outboxMessage
	claimed := false
	err := inTx(ctx, s.store.DB(), func(c *sql.Conn) error {
		// A login link has a short deadline; bulk reminders must not consume its
		// entire TTL while it waits behind the bounded per-sample delivery budget.
		err := c.QueryRowContext(ctx, `SELECT id,customer_id,kind,to_email,subject,body_enc,attempts,created_at,first_attempt_at FROM email_outbox WHERE (state='pending' AND next_at<=?) OR (state='sending' AND claimed_until<=?) ORDER BY CASE WHEN kind='magic_link' AND created_at>? THEN 0 ELSE 1 END,created_at,id LIMIT 1`, portalTime(now), portalTime(now), portalTime(now.Add(-15*time.Minute))).Scan(&m.ID, &m.CustomerID, &m.Kind, &m.To, &m.Subject, &m.Body, &m.Attempts, &m.Created, &m.FirstAttempt)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		created, _ := time.Parse(time.RFC3339, m.Created)
		first, _ := time.Parse(time.RFC3339, m.FirstAttempt)
		state := ""
		if m.Kind == "magic_link" && !created.Add(15*time.Minute).After(now) {
			state = "expired"
		}
		if m.Attempts >= 8 || (!first.IsZero() && !first.Add(23*time.Hour).After(now)) {
			state = "needs_review"
		}
		if state != "" {
			_, err = c.ExecContext(ctx, `UPDATE email_outbox SET state=?,body_enc='',claim_token='',claimed_until='' WHERE id=?`, state, m.ID)
			return err
		}
		m.Claim = randomPortalID("claim_")
		m.Attempts++
		if m.FirstAttempt == "" {
			m.FirstAttempt = portalTime(now)
		}
		_, err = c.ExecContext(ctx, `UPDATE email_outbox SET state='sending',attempts=?,claim_token=?,claimed_until=?,first_attempt_at=? WHERE id=?`, m.Attempts, m.Claim, portalTime(now.Add(2*time.Minute)), m.FirstAttempt, m.ID)
		claimed = err == nil
		return err
	})
	return m, claimed, err
}

func sendResend(ctx context.Context, m outboxMessage) (string, error) {
	body := decryptCred(m.Body)
	if body == "" {
		return "", errors.New("email_body_unavailable")
	}
	payload, _ := json.Marshal(map[string]any{"from": os.Getenv("EMAIL_FROM"), "to": []string{m.To}, "subject": m.Subject, "text": body})
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("email_request_failed")
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("RESEND_API_KEY"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", m.ID)
	resp, err := portalHTTPClient.Do(req)
	if err != nil {
		return "", errors.New("email_transport_failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("email_provider_http_%d", resp.StatusCode)
	}
	var result struct {
		ID string `json:"id"`
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&result) != nil || result.ID == "" {
		return "", errors.New("email_provider_invalid_response")
	}
	return result.ID, nil
}

func (s *server) drainEmailOutbox(ctx context.Context, now time.Time) error {
	if !emailConfigured() {
		return nil
	}
	if _, ok := portalBase(); !ok {
		return nil
	}
	for i := 0; i < 2; i++ {
		m, ok, err := s.claimEmail(ctx, now)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		providerID, sendErr := sendResend(ctx, m)
		state, lastError, body := "sent", "", ""
		next := now
		if sendErr != nil {
			state = "pending"
			lastError = sendErr.Error()
			body = m.Body
			next = now.Add(time.Minute * time.Duration(1<<min(m.Attempts-1, 7)))
			if m.Attempts >= 8 {
				state = "needs_review"
			}
		}
		// Use a fresh bounded context to persist the outcome after request cancellation.
		ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sentAt := ""
		if sendErr == nil {
			sentAt = portalTime(now)
		}
		res, err := s.store.DB().ExecContext(ackCtx, `UPDATE email_outbox SET state=?,provider_id=?,last_error=?,body_enc=?,next_at=?,claim_token='',claimed_until='',sent_at=? WHERE id=? AND claim_token=?`, state, providerID, lastError, body, portalTime(next), sentAt, m.ID, m.Claim)
		cancel()
		if err != nil {
			return err
		}
		count, _ := res.RowsAffected()
		if count == 1 {
			s.emit("email."+state, "info", "customer", m.CustomerID, "system", "Customer email "+state, map[string]string{"kind": m.Kind, "email_id": m.ID})
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

func (s *server) handleEmailLog(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	rows, err := s.store.DB().QueryContext(r.Context(), `SELECT id,customer_id,kind,to_email,subject,state,attempts,provider_id,last_error,created_at,next_at,sent_at FROM email_outbox ORDER BY created_at DESC,id DESC LIMIT ?`, limit)
	if err != nil {
		portalError(w, 503, "email log unavailable")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, customer, kind, to, subject, state, provider, problem, created, next, sent string
		var attempts int
		if err := rows.Scan(&id, &customer, &kind, &to, &subject, &state, &attempts, &provider, &problem, &created, &next, &sent); err != nil {
			portalError(w, 503, "email log unavailable")
			return
		}
		out = append(out, map[string]any{"id": id, "customer_id": customer, "template": kind, "to_email": to, "subject": subject, "status": state, "attempts": attempts, "provider_id": provider, "last_error": problem, "created_at": created, "next_at": next, "sent_at": sent})
	}
	if rows.Err() != nil {
		portalError(w, 503, "email log unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"emails": out, "configured": emailConfigured()})
}

// Invoked by the one existing sampler; no additional ticker is created.
func (s *server) runPortalMaintenance(ctx context.Context, now time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.prunePortalAuth(ctx, now); err != nil {
		return err
	}
	if err := s.scheduleCustomerEmails(ctx, now); err != nil {
		return err
	}
	if err := s.scheduleOperationalEmails(ctx, now); err != nil {
		return err
	}
	return s.drainEmailOutbox(ctx, now)
}
