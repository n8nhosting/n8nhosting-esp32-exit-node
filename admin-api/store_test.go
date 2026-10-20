package main

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Release the SQLite connection after every use so no test leaves files open
// in its t.TempDir() (Windows refuses to delete them).
func init() { dbKeepIdle = false }

func TestStoreImportsLegacyJSONOnce(t *testing.T) {
	dir := t.TempDir()
	js := filepath.Join(dir, "users.json")
	os.WriteFile(js, []byte(`[{"username":"earth","password":"plain","enabled":true,"rate_mbps":100}]`), 0o600)
	st, err := NewStore(js)
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.Get("earth")
	if err != nil || !verifyPassword(u.PassHash, "plain") {
		t.Fatalf("import: %v %v", u, err)
	}
	if _, err := os.Stat(js + ".imported"); err != nil {
		t.Fatal("json should be renamed after import")
	}
	st2, _ := NewStore(js)
	if len(st2.List()) != 1 {
		t.Fatal("second open must not duplicate")
	}
}

func TestStoreConcurrentUsageWrites(t *testing.T) {
	st, _ := NewStore(filepath.Join(t.TempDir(), "p.db"))
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

func TestStoreImportScrubsPlaintextAndSkipsPopulatedDB(t *testing.T) {
	dir := t.TempDir()
	js := filepath.Join(dir, "users.json")
	os.WriteFile(js, []byte(`[{"username":"earth","password":"plain","enabled":true}]`), 0o600)
	if _, err := NewStore(js); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(js + ".imported"); len(data) == 0 || strings.Contains(string(data), "plain") {
		t.Fatalf("kept copy must exist without plaintext: %q", data)
	}
	// a users.json appearing later is not imported into a populated database
	os.WriteFile(js, []byte(`[{"username":"mars","password":"x"}]`), 0o600)
	st, err := NewStore(js)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Get("mars"); err != ErrNotFound {
		t.Fatalf("mars imported into a populated db: %v", err)
	}
	if _, err := os.Stat(js); err != nil {
		t.Fatal("skipped file must be left in place")
	}
}

func TestStoreAuthorizePersistsMonthRoll(t *testing.T) {
	st, _ := NewStore(filepath.Join(t.TempDir(), "p.db"))
	st.Create(&User{Username: "m", PassHash: hashPassword("x"), Enabled: true})
	st.Update("m", func(u *User) error { u.UsageMonth, u.UsageBytes = "2000-01", 999; return nil })
	if ok, why := st.Authorize("m", "x", time.Now()); !ok {
		t.Fatal(why)
	}
	if u, _ := st.Get("m"); u.UsageBytes != 0 || u.UsageMonth != monthKey(time.Now()) {
		t.Fatalf("roll not persisted: %+v", u)
	}
}
