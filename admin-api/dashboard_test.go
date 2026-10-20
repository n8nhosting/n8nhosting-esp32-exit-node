package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardServesDeviceEditor(t *testing.T) {
	srv := httptest.NewServer(dashboardHandler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	body := string(b)

	for _, want := range []string{
		`id="dmodal"`,
		`role="dialog"`,
		`const T = k => i18n[k] ?? k;`,
		`<th>Device</th>`,
		`reveal-wifi`,
		`id="dmCoordHint"`,
		`measureDevice(`,
		`slotPort(`,
		`n.slot_mismatch`,                // drift pill on the device row
		`d.slot_mismatch) body.slot`,     // Save re-sends a drifted slot
		`$('#dmSlot').validity.badInput`, // unparsable slot input is an error, never slot 0
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// the users table passes names via data-user to delegated listeners, never
	// inside inline script (a quote in a name used to break out of it)
	if !strings.Contains(body, `data-user="${un}"`) {
		t.Error("users table must carry the username in data-user")
	}
	for _, bad := range []string{`onclick="copyCreds`, `onclick='openUser`, `onclick="openUser`, `onclick="userAction`,
		`onclick="delUser`, `onclick="assignDevice`, `JSON.stringify(u)`} {
		if strings.Contains(body, bad) {
			t.Errorf("dashboard still has inline handler %q", bad)
		}
	}
}
