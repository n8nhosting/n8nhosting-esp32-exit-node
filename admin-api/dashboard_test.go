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
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
}
