package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// openTestStore opens a store and closes its database when the test ends, so
// no SQLite file is still open when t.TempDir() is removed (Windows refuses).
func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.DB().Close() })
	return st
}

func TestStoreImportsLegacyJSONOnce(t *testing.T) {
	dir := t.TempDir()
	js := filepath.Join(dir, "users.json")
	os.WriteFile(js, []byte(`[{"username":"earth","password":"plain","enabled":true,"rate_mbps":100}]`), 0o600)
	st := openTestStore(t, js)
	u, err := st.Get("earth")
	if err != nil || !verifyPassword(u.PassHash, "plain") {
		t.Fatalf("import: %v %v", u, err)
	}
	if _, err := os.Stat(js + ".imported"); err != nil {
		t.Fatal("json should be renamed after import")
	}
	st2 := openTestStore(t, js)
	if len(st2.List()) != 1 {
		t.Fatal("second open must not duplicate")
	}
}

func TestStoreConcurrentUsageWrites(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "p.db"))
	st.Create(&User{Username: "c", PassHash: hashPassword("x"), Enabled: true})
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); st.AddUsage("c", 10, time.Now()) }()
	}
	wg.Wait()
	if u, _ := st.Get("c"); u.UsageBytes != 2000 {
		t.Fatalf("usage %d", u.UsageBytes)
	}
}

// A failed Update must roll back and release the single connection; a
// transaction left open there would make every later write fail.
func TestStoreUpdateErrorsRollBack(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "p.db"))
	st.Create(&User{Username: "r", PassHash: hashPassword("x"), Enabled: true})
	boom := errors.New("boom")
	if err := st.Update("r", func(u *User) error { u.Note = "lost"; return boom }); err != boom {
		t.Fatalf("callback error: %v", err)
	}
	if err := st.Update("ghost", func(*User) error { return nil }); err != ErrNotFound {
		t.Fatalf("missing user: %v", err)
	}
	st.AddUsage("r", 7, time.Now())
	if u, _ := st.Get("r"); u.UsageBytes != 7 || u.Note != "" {
		t.Fatalf("write after failed updates: %+v", u)
	}
}

func TestStoreImportScrubsPlaintextAndSkipsPopulatedDB(t *testing.T) {
	dir := t.TempDir()
	js := filepath.Join(dir, "users.json")
	os.WriteFile(js, []byte(`[{"username":"earth","password":"plain","enabled":true}]`), 0o600)
	openTestStore(t, js)
	if data, _ := os.ReadFile(js + ".imported"); len(data) == 0 || strings.Contains(string(data), "plain") {
		t.Fatalf("kept copy must exist without plaintext: %q", data)
	}
	// a users.json appearing later is not imported into a populated database
	os.WriteFile(js, []byte(`[{"username":"mars","password":"x"}]`), 0o600)
	st := openTestStore(t, js)
	if _, err := st.Get("mars"); err != ErrNotFound {
		t.Fatalf("mars imported into a populated db: %v", err)
	}
	if _, err := os.Stat(js); err != nil {
		t.Fatal("skipped file must be left in place")
	}
}

func TestStoreAuthorizePersistsMonthRoll(t *testing.T) {
	st := openTestStore(t, filepath.Join(t.TempDir(), "p.db"))
	st.Create(&User{Username: "m", PassHash: hashPassword("x"), Enabled: true})
	st.Update("m", func(u *User) error { u.UsageMonth, u.UsageBytes = "2000-01", 999; return nil })
	if ok, why := st.Authorize("m", "x", time.Now()); !ok {
		t.Fatal(why)
	}
	if u, _ := st.Get("m"); u.UsageBytes != 0 || u.UsageMonth != monthKey(time.Now()) {
		t.Fatalf("roll not persisted: %+v", u)
	}
}
