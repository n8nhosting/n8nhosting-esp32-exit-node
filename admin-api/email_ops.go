package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

// Setting this operator-controlled mailbox opts in to operational email. It is
// deliberately separate from customer addresses and never inferred from them.
func operationalEmailRecipient() string {
	raw := strings.TrimSpace(os.Getenv("OPS_ALERT_EMAIL"))
	address, err := mail.ParseAddress(raw)
	if err != nil || len(raw) > 254 || address.Address != raw || strings.ContainsAny(raw, "\r\n") {
		return ""
	}
	return raw
}

var operationalAlertTitles = map[string]string{
	"system.broker_unreachable": "Broker unavailable",
	"system.slot_dials_refused": "Connections refused for an unavailable board",
	"node.offline":              "Board offline",
	"node.ota_failed":           "Firmware update failed",
	"node.ota_stalled":          "Firmware update not confirmed",
	"node.ip_changed":           "Board public IP changed",
	"device.slot_mismatch":      "Board routing differs from saved settings",
	"device.unmeasured":         "Board capacity needs measurement",
}

type operationalEmailEvent struct {
	id                      int64
	ts, kind, severity, key string
}

// Do not copy event messages or JSON payloads: node events may contain customer
// usernames, notes or user-provided board names. Only known operational kinds
// and validated fleet identifiers enter this notification channel.
func operationalEventCopy(e operationalEmailEvent, now time.Time) (subject, body string, ok bool) {
	alertKind, identifier, _ := strings.Cut(e.key, ":")
	title, known := operationalAlertTitles[alertKind]
	if !known {
		return "", "", false
	}
	board := ""
	if strings.HasPrefix(alertKind, "system.") {
		if identifier != "" {
			return "", "", false
		}
	} else {
		mac, err := net.ParseMAC(identifier)
		if err != nil || len(mac) != 6 {
			return "", "", false
		}
		board = "\nBoard MAC: " + mac.String()
	}
	when, err := time.Parse(time.RFC3339, e.ts)
	if err != nil || when.Before(now.Add(-24*time.Hour)) || when.After(now) {
		return "", "", false
	}
	severity := e.severity
	if severity != "critical" && severity != "warn" && severity != "info" {
		severity = "info"
	}
	transition := "Raised"
	if e.kind == "alert.cleared" {
		transition = "Cleared"
	}
	subject = "[Exit node] " + transition + ": " + title
	body = fmt.Sprintf("Operational alert %s: %s\nSeverity: %s%s\nEvent: %d\nEvent time: %s\nQueued at: %s\n\nThis records the event at the stated time; it does not assert the current live state.\nYou receive operational notifications because the operator configured OPS_ALERT_EMAIL.\n", strings.ToLower(transition), title, severity, board, e.id, when.UTC().Format(time.RFC3339), now.UTC().Format(time.RFC3339))
	return subject, body, true
}

// Consume a bounded, ascending keyset from durable events on the existing
// sampler. The outbox insertion and cursor movement commit together, so failed
// flushes/restarts cannot lose a transition or enqueue it twice. Enabling this
// optional channel starts at the current event tail. Once enabled, restarts
// resume its cursor while skipping transitions older than 24 hours.
func (s *server) scheduleOperationalEmails(ctx context.Context, now time.Time) error {
	to := operationalEmailRecipient()
	if to == "" || !emailConfigured() {
		return nil
	}
	if _, ready := portalBase(); !ready {
		return nil
	}
	return inTx(ctx, s.store.DB(), func(c *sql.Conn) error {
		var cursor int64
		if err := c.QueryRowContext(ctx, `SELECT last_event_id FROM ops_email_cursor WHERE id=1`).Scan(&cursor); err != nil {
			return err
		}
		if cursor < 0 {
			_, err := c.ExecContext(ctx, `UPDATE ops_email_cursor SET last_event_id=(SELECT COALESCE(MAX(id),0) FROM events) WHERE id=1`)
			return err
		}
		rows, err := c.QueryContext(ctx, `SELECT id,ts,kind,severity,subject_id FROM events WHERE id>? AND subject_type='alert' AND kind IN ('alert.raised','alert.cleared') ORDER BY id LIMIT 100`, cursor)
		if err != nil {
			return err
		}
		batch := []operationalEmailEvent{}
		for rows.Next() {
			var e operationalEmailEvent
			if err = rows.Scan(&e.id, &e.ts, &e.kind, &e.severity, &e.key); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range batch {
			if subject, body, ok := operationalEventCopy(e, now); ok {
				kind := "ops_alert_" + strings.TrimPrefix(e.kind, "alert.")
				if err = queueEmailConn(ctx, c, "ops_event_"+strconv.FormatInt(e.id, 10), "", kind, to, subject, body); err != nil {
					return err
				}
			}
			cursor = e.id
		}
		_, err = c.ExecContext(ctx, `UPDATE ops_email_cursor SET last_event_id=? WHERE id=1`, cursor)
		return err
	})
}
