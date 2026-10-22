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
		`id="fwmodal"`,                   // firmware version picker (Update FW)
		`otaAll()`,                       // "Update all online…"
		`${otaPill(n)}`,                  // OTA status in the FW column
		`data-act="ota-cancel"`,          // cancel goes through the table's delegated listener
		`n.chip||'esp32s3'`,              // chip pill; nodes before 2.4.22 never said, all S3
		`targets[chip]`,                  // OTA picker chooses the image built for the board's chip
		`target: chip`,                   // and tells the broker which chip the image is for
		`/^https:\/\//.test(custom)`,     // custom OTA URLs must be https (unsigned images)
		`tgt.reports`,                    // the version a board reports carries its chip (2.4.22-c3)
		`id="dmMaps"`,                    // device editor: paste a Google Maps share link
		`/api/geo/resolve`,               // ... which the server turns into lat/lon
		`google.com/maps?q=`,             // the location cell opens Google Maps
		`id="cmodal"`,                    // connection sheet: fields with their own copy buttons
		`id="amodal"`,                    // assign picker: choose a free board, never type a MAC
		`id="toast"`,                     // transient confirmations instead of alert()
		`role="listbox"`,                 // the picker is keyboard-navigable
		`<th>Health</th>`,                // uptime + RAM/CPU/flash in one cell (no 14th column)
		`n.stats`,                        // the broker's STATS sample (firmware 2.4.23+)
		`fmtAgo(n.last_seen)`,            // an offline board says when it was last seen
		`id="rowmenu"`,                   // one shared popover for every row's "more" button
		`role="menu"`,                    // ... which is a real menu for keyboard and screen readers
		`aria-haspopup="menu"`,           // the trigger says so
		`n.status`,                       // the server owns the canonical state
		`n.tags`,                         // secondary reasons do not replace that state
		`id="ddrawer"`,                   // deep-linked device details
		`/api/metrics?entity=node&id=`,    // canonical per-node history query
		`/api/events?subject=`,            // canonical subject-filtered audit trail
		`kpi-online`,                     // inventory and live operation KPIs
		`kpi-seats`,
		`kpi-users`,
		`kpi-traffic`,
		`Table view`,                     // the chart has a non-visual data equivalent
		`with data`,                      // the table says how many samples were observed
		`No data · `,                     // consecutive unobserved samples collapse into one row
		`id="userSummary"`,               // users page states counts per status from real data
		`Up to board speed`,              // no placeholder-looking "≤ capacity" / "∞ GB" labels
		`avg Mbps`,                       // history charts plot average rate, not bytes per sample
		`row.dataset.key=key`,            // background updates retain row identity
		`document.hidden`,                // stop polling a hidden browser tab
		`body.lat=null;body.lon=null`,    // explicit paired coordinate deletion
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing %q", want)
		}
	}
	// the users table passes names via data-user to delegated listeners, never
	// inside inline script (a quote in a name used to break out of it); device
	// rows likewise carry the MAC in data-mac
	if !strings.Contains(body, `data-user="${un}"`) {
		t.Error("users table must carry the username in data-user")
	}
	// alert() boxes cannot be copied from and prompt() makes the operator type a
	// MAC; both are gone (confirm() for destructive actions stays)
	for _, bad := range []string{`onclick="copyCreds`, `onclick='openUser`, `onclick="openUser`, `onclick="userAction`,
		`onclick="delUser`, `onclick="assignDevice`, `JSON.stringify(u)`, `onclick="devAction`, `onclick="otaDevice`,
		`alert(`, `prompt(`,
		// destructive actions live at the bottom of the row menu, never as a red button beside Edit
		`class="danger" data-act="forget"`, `class="danger" data-act="delete"`} {
		if strings.Contains(body, bad) {
			t.Errorf("dashboard still has inline handler %q", bad)
		}
	}
}
