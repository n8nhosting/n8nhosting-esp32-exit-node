package main

import (
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func activityTestServer(t *testing.T) *server {
	t.Helper()
	st := openTestStore(t, filepath.Join(t.TempDir(), "activity.db"))
	return &server{store: st, devices: NewDeviceStore(st.DB())}
}

func eventCount(t *testing.T, s *server, kind string) int {
	t.Helper()
	var n int
	if err := s.store.DB().QueryRow(`SELECT COUNT(*) FROM events WHERE kind=?`, kind).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestEventsKeysetAndLiteralFilters(t *testing.T) {
	s := activityTestServer(t)
	s.emit("node.online", "info", "node", "AA:BB", "poller", "Online", nil)
	s.emit("node.offline", "warn", "node", "aa:bb", "poller", "Offline", map[string]any{"count": 2})
	s.emit("user.created", "info", "user", "test", "admin", "Created", nil)
	req := httptest.NewRequest("GET", "/api/events?limit=1", nil)
	w := httptest.NewRecorder()
	s.handleEvents(w, req)
	var page struct {
		Events     []activityEvent `json:"events"`
		NextBefore *int64          `json:"next_before"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(page.Events) != 1 || page.Events[0].Kind != "user.created" || page.NextBefore == nil {
		t.Fatalf("page: %s", w.Body)
	}
	w = httptest.NewRecorder()
	s.handleEvents(w, httptest.NewRequest("GET", "/api/events?before=3&kind=node.&subject=aa:bb&severity=warn", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Kind != "node.offline" || page.NextBefore != nil {
		t.Fatalf("filtered: %s", w.Body)
	}
	w = httptest.NewRecorder()
	s.handleEvents(w, httptest.NewRequest("GET", "/api/events?kind=node%25", nil))
	if strings.Contains(w.Body.String(), "node.online") {
		t.Fatal("prefix wildcard was interpreted as SQL pattern")
	}
	for _, q := range []string{"limit=0", "limit=201", "before=-1", "before=no", "since=yesterday", "severity=unknown"} {
		w = httptest.NewRecorder()
		s.handleEvents(w, httptest.NewRequest("GET", "/api/events?"+q, nil))
		if w.Code != 400 {
			t.Fatalf("%s: %d", q, w.Code)
		}
	}
}

func TestEventRetention(t *testing.T) {
	s := activityTestServer(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	s.emitAt(now.AddDate(0, 0, -91), "old", "info", "system", "", "system", "Old", nil)
	s.emitAt(now, "new", "info", "system", "", "system", "New", nil)
	s.pruneEvents(now)
	if eventCount(t, s, "old") != 0 || eventCount(t, s, "new") != 1 {
		t.Fatal("retention removed current event or retained expired event")
	}
}

func TestUserActionEventsExcludeCredentials(t *testing.T) {
	s := activityTestServer(t)
	body := `{"username":"audit","password":"SUPER_SECRET_PASSWORD","note":"private request note"}`
	w := httptest.NewRecorder()
	s.handleUsers(w, httptest.NewRequest("POST", "/api/users", strings.NewReader(body)))
	if w.Code != 201 {
		t.Fatalf("create: %s", w.Body)
	}
	w = httptest.NewRecorder()
	s.handleUser(w, httptest.NewRequest("PUT", "/api/users/audit", strings.NewReader(`{"password":"ANOTHER_SECRET"}`)))
	if w.Code != 200 {
		t.Fatalf("update: %s", w.Body)
	}
	w = httptest.NewRecorder()
	s.handleEvents(w, httptest.NewRequest("GET", "/api/events", nil))
	for _, secret := range []string{"SUPER_SECRET_PASSWORD", "ANOTHER_SECRET", "private request note", "pass_hash", "cred_enc"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatalf("event leaked %s", secret)
		}
	}
	if eventCount(t, s, "user.created") != 1 || eventCount(t, s, "user.credentials_rotated") != 1 {
		t.Fatalf("missing audit events: %s", w.Body)
	}
}
