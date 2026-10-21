package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func opsEmailServer(t *testing.T) *server {
	t.Helper()
	s, _ := portalTestServer(t)
	t.Setenv("OPS_ALERT_EMAIL", "")
	return s
}

func enableOpsEmail(t *testing.T, s *server, now time.Time) {
	t.Helper()
	configurePortalTest(t)
	t.Setenv("OPS_ALERT_EMAIL", "operations@example.test")
	if err := s.scheduleOperationalEmails(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

func opsEmailEvent(t *testing.T, s *server, at time.Time, kind, key string) {
	t.Helper()
	if !s.emitAt(at, kind, "warn", "alert", key, "poller", "SECRET customer-name password private-note", map[string]any{"users": []string{"SECRET-customer"}, "password": "SECRET-password"}) {
		t.Fatal("durable event insert failed")
	}
}

func opsEmailCount(t *testing.T, s *server) int {
	t.Helper()
	var count int
	if err := s.store.DB().QueryRow(`SELECT COUNT(*) FROM email_outbox WHERE kind LIKE 'ops_alert_%'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestOperationalEmailDisabledValidationAndForwardOnlyEnable(t *testing.T) {
	s := opsEmailServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	opsEmailEvent(t, s, now.Add(-time.Minute), "alert.raised", "system.broker_unreachable")
	configurePortalTest(t)
	portalTestClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("disabled operational email performed network request")
		return nil, errors.New("unreachable")
	})
	for _, recipient := range []string{"", "invalid", "Operator <ops@example.test>", "a@example.test,b@example.test", "ops@example.test\r\nBcc:other@example.test"} {
		t.Setenv("OPS_ALERT_EMAIL", recipient)
		if err := s.runPortalMaintenance(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		var cursor int64
		s.store.DB().QueryRow(`SELECT last_event_id FROM ops_email_cursor WHERE id=1`).Scan(&cursor)
		if cursor != -1 || opsEmailCount(t, s) != 0 {
			t.Fatalf("invalid recipient activated channel: %q cursor%d", recipient, cursor)
		}
	}
	enableOpsEmail(t, s, now)
	if opsEmailCount(t, s) != 0 {
		t.Fatal("first enable replayed historical alerts")
	}
	opsEmailEvent(t, s, now, "alert.cleared", "system.broker_unreachable")
	if err := s.scheduleOperationalEmails(context.Background(), now); err != nil || opsEmailCount(t, s) != 1 {
		t.Fatalf("new transition missing: %v", err)
	}
}

func TestOperationalEmailPrivacyTransitionsAndSharedOutbox(t *testing.T) {
	s := opsEmailServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	enableOpsEmail(t, s, now)
	opsEmailEvent(t, s, now, "alert.raised", "node.offline:AA:BB:CC:DD:EE:01")
	opsEmailEvent(t, s, now, "alert.cleared", "node.offline:aa:bb:cc:dd:ee:01")
	for _, key := range []string{"user.over_quota:SECRET-customer", "user.expiring:SECRET-customer", "node.offline:SECRET-customer", "system.broker_unreachable:SECRET-customer", "future.unknown:SECRET"} {
		opsEmailEvent(t, s, now, "alert.raised", key)
	}
	if err := s.scheduleOperationalEmails(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if opsEmailCount(t, s) != 2 {
		t.Fatal("missing transition or customer alerts entered operator channel")
	}
	var calls int
	portalTestClient(t, func(r *http.Request) (*http.Response, error) {
		calls++
		var payload struct {
			To      []string `json:"to"`
			Subject string   `json:"subject"`
			Text    string   `json:"text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.To) != 1 || payload.To[0] != "operations@example.test" || strings.Contains(payload.Text+payload.Subject, "SECRET") || !strings.Contains(payload.Text, "aa:bb:cc:dd:ee:01") || !strings.Contains(payload.Text, "does not assert the current live state") || !strings.HasPrefix(r.Header.Get("Idempotency-Key"), "ops_event_") {
			t.Fatalf("unsafe operational copy: %+v", payload)
		}
		if calls == 1 && !strings.Contains(payload.Subject, "Raised") || calls == 2 && !strings.Contains(payload.Subject, "Cleared") {
			t.Fatal("transition labels incorrect")
		}
		return portalTestResponse(200, fmt.Sprintf(`{"id":"ops_%d"}`, calls)), nil
	})
	if err := s.drainEmailOutbox(context.Background(), now.Add(time.Minute)); err != nil || calls != 2 {
		t.Fatalf("shared outbox did not deliver both: %d %v", calls, err)
	}
	if err := (&server{store: s.store}).scheduleOperationalEmails(context.Background(), now.Add(time.Minute)); err != nil || opsEmailCount(t, s) != 2 {
		t.Fatalf("restart duplicated transitions: %v", err)
	}
}

func TestOperationalEmailCursorAtomicFailureAndConcurrentRestart(t *testing.T) {
	s := opsEmailServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	enableOpsEmail(t, s, now)
	opsEmailEvent(t, s, now, "alert.raised", "system.broker_unreachable")
	portalSQL(t, s, `CREATE TRIGGER reject_ops_email BEFORE INSERT ON email_outbox WHEN NEW.kind LIKE 'ops_alert_%' BEGIN SELECT RAISE(ABORT,'injected outbox failure'); END`)
	if err := s.scheduleOperationalEmails(context.Background(), now); err == nil {
		t.Fatal("injected outbox failure ignored")
	}
	var cursor int64
	s.store.DB().QueryRow(`SELECT last_event_id FROM ops_email_cursor WHERE id=1`).Scan(&cursor)
	if cursor != 0 || opsEmailCount(t, s) != 0 {
		t.Fatalf("failed queue advanced cursor: %d", cursor)
	}
	portalSQL(t, s, `DROP TRIGGER reject_ops_email`)
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- (&server{store: s.store}).scheduleOperationalEmails(context.Background(), now)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if opsEmailCount(t, s) != 1 {
		t.Fatal("concurrent restart lost or duplicated event")
	}
}

func TestOperationalEmailKeysetBacklogFreshnessAndDurableEventsOnly(t *testing.T) {
	s := opsEmailServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	enableOpsEmail(t, s, now)
	if err := inTx(context.Background(), s.store.DB(), func(c *sql.Conn) error {
		_, err := c.ExecContext(context.Background(), `INSERT INTO events(ts,kind,severity,subject_type,subject_id,actor,message,data) VALUES(?,'alert.raised','critical','alert','system.broker_unreachable','poller','SECRET','{}')`, portalTime(now))
		if err != nil {
			return err
		}
		return errors.New("roll back alert mutation")
	}); err == nil {
		t.Fatal("rollback fixture failed")
	}
	if err := s.scheduleOperationalEmails(context.Background(), now); err != nil || opsEmailCount(t, s) != 0 {
		t.Fatal("uncommitted alert generated email")
	}
	opsEmailEvent(t, s, now.Add(-25*time.Hour), "alert.raised", "system.broker_unreachable")
	for i := 0; i < 205; i++ {
		kind := "alert.raised"
		if i%2 != 0 {
			kind = "alert.cleared"
		}
		opsEmailEvent(t, s, now.Add(-time.Minute), kind, "system.broker_unreachable")
	}
	for i := 0; i < 3; i++ {
		if err := (&server{store: s.store}).scheduleOperationalEmails(context.Background(), now); err != nil {
			t.Fatal(err)
		}
	}
	if opsEmailCount(t, s) != 205 {
		t.Fatalf("backlog lost or stale event queued: %d", opsEmailCount(t, s))
	}
	var cursor, tail int64
	s.store.DB().QueryRow(`SELECT last_event_id FROM ops_email_cursor WHERE id=1`).Scan(&cursor)
	s.store.DB().QueryRow(`SELECT MAX(id) FROM events`).Scan(&tail)
	if cursor != tail {
		t.Fatalf("cursor not at tail: %d/%d", cursor, tail)
	}
}
