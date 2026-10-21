package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type activityEvent struct {
	ID          int64           `json:"id"`
	TS          string          `json:"ts"`
	Kind        string          `json:"kind"`
	Severity    string          `json:"severity"`
	SubjectType string          `json:"subject_type"`
	SubjectID   string          `json:"subject_id"`
	Actor       string          `json:"actor"`
	Message     string          `json:"message"`
	Data        json.RawMessage `json:"data"`
}

// Call only after the store mutation's transaction has completed. Event payloads
// are explicit allowlists at each call site; request bodies are never recorded.
func (s *server) emit(kind, severity, subjectType, subjectID, actor, message string, data any) {
	s.emitAt(time.Now().UTC(), kind, severity, subjectType, subjectID, actor, message, data)
}

func (s *server) emitAt(now time.Time, kind, severity, subjectType, subjectID, actor, message string, data any) bool {
	if s.store == nil {
		return false
	}
	if subjectType == "node" || subjectType == "device" {
		subjectID = strings.ToLower(subjectID)
	}
	b := []byte(`{}`)
	if data != nil {
		var err error
		b, err = json.Marshal(data)
		if err != nil {
			log.Printf("events: invalid payload for %s: %v", kind, err)
			return false
		}
	}
	_, err := s.store.DB().Exec(`INSERT INTO events(ts,kind,severity,subject_type,subject_id,actor,message,data) VALUES(?,?,?,?,?,?,?,?)`,
		now.UTC().Format(time.RFC3339), kind, severity, subjectType, subjectID, actor, message, string(b))
	if err != nil {
		log.Printf("events: insert %s: %v", kind, err)
		return false
	}
	return true
}

func (s *server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method"})
		return
	}
	q := r.URL.Query()
	limit := 50
	bad := func(msg string) { writeJSON(w, 400, map[string]string{"error": msg}) }
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 200 {
			bad("limit must be 1..200")
			return
		}
		limit = n
	}
	where, args := []string{"1=1"}, []any{}
	if v := q.Get("before"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			bad("invalid before cursor")
			return
		}
		where = append(where, "id < ?")
		args = append(args, n)
	}
	if v := q.Get("kind"); v != "" {
		where = append(where, "substr(kind,1,length(?)) = ?")
		args = append(args, v, v)
	}
	if v := q.Get("subject"); v != "" {
		where = append(where, "subject_id = ?")
		args = append(args, v)
	}
	if v := q.Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			bad("invalid since timestamp")
			return
		}
		where = append(where, "ts >= ?")
		args = append(args, t.UTC().Format(time.RFC3339))
	}
	if v := q.Get("severity"); v != "" {
		marks := []string{}
		for _, sev := range strings.Split(v, ",") {
			if sev != "info" && sev != "warn" && sev != "error" && sev != "critical" {
				bad("invalid severity")
				return
			}
			marks = append(marks, "?")
			args = append(args, sev)
		}
		where = append(where, "severity IN ("+strings.Join(marks, ",")+")")
	}
	args = append(args, limit+1)
	rows, err := s.store.DB().Query(`SELECT id,ts,kind,severity,subject_type,subject_id,actor,message,data FROM events WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "events unavailable"})
		return
	}
	defer rows.Close()
	out := make([]activityEvent, 0, limit+1)
	for rows.Next() {
		var e activityEvent
		var data string
		if err := rows.Scan(&e.ID, &e.TS, &e.Kind, &e.Severity, &e.SubjectType, &e.SubjectID, &e.Actor, &e.Message, &data); err != nil {
			writeJSON(w, 500, map[string]string{"error": "events unavailable"})
			return
		}
		e.Data = json.RawMessage(data)
		out = append(out, e)
	}
	if rows.Err() != nil {
		writeJSON(w, 500, map[string]string{"error": "events unavailable"})
		return
	}
	var next any
	if len(out) > limit {
		out = out[:limit]
		next = out[len(out)-1].ID
	}
	writeJSON(w, 200, map[string]any{"events": out, "next_before": next})
}

func (s *server) pruneEvents(now time.Time) {
	days := atoiDefault(envOr("EVENTS_RETENTION_DAYS", "90"), 90)
	if days < 1 {
		days = 90
	}
	// Keep the newest 100,000 rows even when ids contain gaps.
	_, err := s.store.DB().Exec(`DELETE FROM events WHERE ts < ? OR id < COALESCE((SELECT id FROM events ORDER BY id DESC LIMIT 1 OFFSET 99999),0)`, now.AddDate(0, 0, -days).UTC().Format(time.RFC3339))
	if err != nil {
		log.Printf("events: retention: %v", err)
	}
}
