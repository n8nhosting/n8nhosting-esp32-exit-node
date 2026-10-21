package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Permission bits are not a portable write-failure fixture on Windows or when
// tests run as root. File/directory collisions reliably fail each filesystem
// stage on all supported platforms without changing host permissions.
func brokenOTAStorage(t *testing.T, mode, validPath string) string {
	t.Helper()
	root := t.TempDir()
	switch mode {
	case "mkdir":
		parent := filepath.Join(root, "regular-file")
		if err := os.WriteFile(parent, []byte("not a directory"), 0600); err != nil {
			t.Fatal(err)
		}
		return filepath.Join(parent, "nodes.json")
	case "write":
		if err := os.Mkdir(validPath+".tmp", 0700); err != nil {
			t.Fatal(err)
		}
		return validPath
	case "rename":
		target := filepath.Join(root, "existing-directory")
		if err := os.Mkdir(target, 0700); err != nil {
			t.Fatal(err)
		}
		return target
	default:
		t.Fatalf("unknown storage failure %s", mode)
		return ""
	}
}

func TestOTAStorageFailureWithholdsMuxAndLegacyCommands(t *testing.T) {
	for _, transport := range []string{"mux", "legacy"} {
		for _, mode := range []string{"mkdir", "write", "rename"} {
			t.Run(transport+"/"+mode, func(t *testing.T) {
				valid := filepath.Join(t.TempDir(), "nodes.json")
				b := newBroker(config{token: "tok", nodesFile: valid}, 1)
				n := b.reg.touch("board", "old", "", 300, "", "")
				if !b.reg.setOTA(n.MAC, "https://example/new.bin", "new") {
					t.Fatal("could not seed durable queue")
				}
				previousAt := n.OTAAt
				b.reg.file = brokenOTAStorage(t, mode, valid)
				input := ""
				if transport == "legacy" {
					input = "HELLO tok board old 300\n"
				}
				c := newOTAScriptConn(t, input, "success")
				if transport == "mux" {
					b.serveMux(c, bufio.NewReader(c), n, helloOpts{MaxStreams: 4})
				} else {
					b.handlePoolConn(c)
				}
				if c.writes != 1 { // READY only; no OTA bytes may follow
					t.Fatalf("command written without durable dispatch marker: %d writes", c.writes)
				}
				if n.OTAStatus != "queued" || n.PendingOTA != "https://example/new.bin" || n.OTAError != "storage_error" || n.otaDispatching || n.otaClaimActive != 0 || n.OTAAt != previousAt {
					t.Fatalf("failed persistence lost/misreported queue: %+v", n)
				}
				restored := newRegistry(valid).nodes[n.MAC]
				if restored == nil || restored.OTAStatus != "queued" || restored.PendingOTA != n.PendingOTA {
					t.Fatalf("failed claim corrupted original durable queue: %+v", restored)
				}
				if mode == "write" {
					if err := os.Remove(valid + ".tmp"); err != nil {
						t.Fatal(err)
					}
				}
				b.reg.file = valid
				if _, ok := b.reg.claimOTA(n, 0); !ok {
					t.Fatal("storage recovery did not release the queued command for retry")
				}
			})
		}
	}
}

func TestOTAHTTPStorageFailureRestoresRequestAndActiveClaim(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nodes.json")
	b := newBroker(config{nodesFile: file}, 1)
	n := b.reg.touch("board", "old", "", 300, "", "")
	b.reg.setOTA(n.MAC, "https://example/original.bin", "original")
	claim, ok := b.reg.claimOTA(n, 0)
	if !ok {
		t.Fatal("could not seed active dispatch")
	}
	b.reg.file = brokenOTAStorage(t, "rename", file)
	for _, action := range []string{"replace", "cancel"} {
		var rr *httptest.ResponseRecorder
		if action == "replace" {
			rr = postOTA(b, n.MAC, `{"url":"https://example/replacement.bin","version":"replacement"}`)
		} else {
			rr = httptest.NewRecorder()
			b.statusMux().ServeHTTP(rr, httptest.NewRequest("POST", "/nodes/"+n.MAC+"/ota-cancel", nil))
		}
		if rr.Code != http.StatusInternalServerError || !strings.Contains(rr.Body.String(), "could not persist OTA request") {
			t.Fatalf("%s storage failure falsely acknowledged: %d %s", action, rr.Code, rr.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil || body["error"] != "could not persist OTA request" {
			t.Fatalf("storage failure must survive the admin JSON proxy: %s", rr.Body.String())
		}
		if n.PendingOTA != claim.URL || n.OTATarget != "original" || n.otaRevision != claim.Revision || n.otaClaimActive != claim.ID || !n.otaDispatching || n.OTAStatus != "queued" {
			t.Fatalf("failed %s invalidated previous request: %+v", action, n)
		}
	}
	b.reg.file = file
	b.reg.completeOTA(n, claim, true)
	if n.OTAStatus != "sent" || n.PendingOTA != "" || n.OTATarget != "original" {
		t.Fatal("failed replacement/cancel prevented the original completion")
	}
}

func TestOTACompletionPersistenceFailureKeepsCrashGuard(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nodes.json")
	r := newRegistry(file)
	n := r.touch("board", "old", "", 300, "", "")
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	claim, ok := r.claimOTA(n, 0)
	if !ok {
		t.Fatal("could not persist dispatch marker")
	}
	brokenOTAStorage(t, "write", file)
	r.completeOTA(n, claim, true)
	if n.OTAStatus != "sent" || n.PendingOTA != "" || n.OTAError != "storage_error" {
		t.Fatalf("completed write became retryable after storage failure: %+v", n)
	}
	restarted := newRegistry(file)
	fresh := restarted.nodes[n.MAC]
	if fresh.OTAStatus != "sent" || fresh.OTAError != "delivery_unconfirmed" || fresh.PendingOTA != "" {
		t.Fatalf("pre-write marker did not protect crash recovery: %+v", fresh)
	}
	if _, ok := restarted.claimOTA(fresh, 0); ok {
		t.Fatal("crash after failed completion save allowed duplicate delivery")
	}
}
