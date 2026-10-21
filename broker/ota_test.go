package main

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testSendOTA(r *registry, n *node) string {
	claim, ok := r.claimOTA(n, 0)
	if !ok {
		return ""
	}
	r.completeOTA(n, claim, true)
	return claim.URL
}

func TestOTAClaimWriteFailureReplacementAndCancel(t *testing.T) {
	r := newRegistry("")
	n := r.touch("board", "old", "", 100, "", "")
	r.setOTA(n.MAC, "https://example/one.bin", "one")
	first, ok := r.claimOTA(n, 0)
	if !ok || n.OTAStatus != "queued" || n.PendingOTA == "" {
		t.Fatal("claim must not claim successful delivery")
	}
	if _, ok := r.claimOTA(n, 0); ok {
		t.Fatal("two writers claimed one request")
	}
	r.completeOTA(n, first, false)
	if n.OTAStatus != "queued" || n.PendingOTA != first.URL || n.OTAError != "write_failed" {
		t.Fatalf("failed write was lost: %+v", n)
	}
	retry, ok := r.claimOTA(n, 0)
	if !ok || retry.ID == first.ID {
		t.Fatal("failed write did not release the claim")
	}
	r.completeOTA(n, first, true) // obsolete completion cannot complete a retry
	if n.OTAStatus != "queued" {
		t.Fatal("old claim completed a newer writer")
	}
	r.setOTA(n.MAC, "https://example/two.bin", "two")
	r.completeOTA(n, retry, true)
	if n.OTAStatus != "queued" || n.PendingOTA != "https://example/two.bin" || n.OTATarget != "two" {
		t.Fatal("old callback overwrote a replacement request")
	}
	newer, _ := r.claimOTA(n, 0)
	r.setOTA(n.MAC, "", "")
	r.completeOTA(n, newer, true)
	if n.OTAStatus != "" || n.PendingOTA != "" {
		t.Fatal("cancelled command was revived by its callback")
	}
}

func TestOTALatestMuxGenerationAndForgottenNodeGuard(t *testing.T) {
	r := newRegistry("")
	n := r.touch("board", "old", "", 100, "", "")
	oldGen := r.muxUp(n, 4)
	newGen := r.muxUp(n, 4)
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	if _, ok := r.claimOTA(n, oldGen); ok {
		t.Fatal("older overlapping mux link stole a new command")
	}
	if _, ok := r.claimOTA(n, 0); ok {
		t.Fatal("legacy connection stole the live mux command")
	}
	claim, ok := r.claimOTA(n, newGen)
	if !ok {
		t.Fatal("latest accepted mux could not claim")
	}
	r.forget(n.MAC)
	fresh := r.touch(n.MAC, "old", "", 101, "", "")
	r.setOTA(fresh.MAC, "https://example/fresh.bin", "fresh")
	r.completeOTA(n, claim, true)
	if _, ok := r.claimOTA(n, newGen); ok {
		t.Fatal("forgotten session claimed a detached request")
	}
	if fresh.OTAStatus != "queued" || fresh.PendingOTA != "https://example/fresh.bin" {
		t.Fatal("orphan callback changed the replacement node")
	}
}

func TestOTADurableQueueSentMetadataTimeoutAndLateSuccess(t *testing.T) {
	t0 := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	now := t0
	timeNow = func() time.Time { return now }
	defer func() { timeNow = time.Now }()
	file := filepath.Join(t.TempDir(), "nodes.json")
	r := newRegistry(file)
	n := r.touch("board", "old", "", 500, "", "")
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	r = newRegistry(file)
	n = r.nodes[n.MAC]
	if n.PendingOTA != "https://example/new.bin" || n.OTAStatus != "queued" {
		t.Fatal("restart lost queued URL")
	}
	claim, ok := r.claimOTA(n, 0)
	if !ok {
		t.Fatal("durable queue cannot be delivered")
	}
	r.completeOTA(n, claim, true)
	r = newRegistry(file)
	n = r.nodes[n.MAC]
	if n.otaUptime != 500 || n.otaFromFW != "old" || n.otaRevision != claim.Revision || n.OTAStatus != "sent" || n.PendingOTA != "" {
		t.Fatalf("sent metadata lost: %+v", n)
	}
	if _, ok := r.claimOTA(n, 0); ok {
		t.Fatal("a written command must not be automatically sent after restart")
	}
	now = t0.Add(15 * time.Minute)
	if r.list()[0].OTAStatus != "sent" {
		t.Fatal("timeout fired before the confirmation window ended")
	}
	now = now.Add(time.Second)
	v := r.list()[0]
	if v.OTAStatus != "timeout" || v.OTAError != "confirmation_timeout" || v.PendingOTA != "" {
		t.Fatalf("timeout did not become terminal: %+v", v)
	}
	r = newRegistry(file)
	n = r.nodes[n.MAC]
	if n.OTAStatus != "timeout" {
		t.Fatal("timeout did not survive restart")
	}
	r.touch(n.MAC, "new", "", 5, "", "esp32s3")
	if n.OTAStatus != "ok" || n.OTAError != "" {
		t.Fatal("late requested firmware could not confirm the timeout")
	}
}

func TestOTALegacyQueuedRecordWithoutURLIsExplicitFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nodes.json")
	if err := os.WriteFile(file, []byte(`[{"mac":"old","firmware":"old","ota_status":"queued","ota_target":"new","ota_at":"2026-10-22T00:00:00Z"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	r := newRegistry(file)
	v := r.list()[0]
	if v.OTAStatus != "failed" || v.OTAError != "request_missing" || v.PendingOTA != "" {
		t.Fatalf("missing URL remains an endless queue: %+v", v)
	}
}

func TestOTARestartDuringDispatchDoesNotDuplicateAmbiguousWrite(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nodes.json")
	r := newRegistry(file)
	n := r.touch("board", "old", "", 300, "", "")
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	if _, ok := r.claimOTA(n, 0); !ok {
		t.Fatal("could not claim command")
	}
	// No write completion: the old process may have died before or after the
	// board got all bytes. Restoring a queued URL would risk a second flash.
	restarted := newRegistry(file)
	fresh := restarted.nodes[n.MAC]
	if fresh.OTAStatus != "sent" || fresh.OTAError != "delivery_unconfirmed" || fresh.PendingOTA != "" || fresh.otaUptime != 300 {
		t.Fatalf("ambiguous dispatch recovery: %+v", fresh)
	}
	if _, ok := restarted.claimOTA(fresh, 0); ok {
		t.Fatal("restart blindly repeated an ambiguous delivery")
	}
}

func TestOTAFastHelloBeforeWriteCompletionCanConfirm(t *testing.T) {
	r := newRegistry("")
	n := r.touch("board", "old", "", 300, "", "")
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	claim, _ := r.claimOTA(n, 0)
	r.touch(n.MAC, "new", "", 3, "", "esp32s3")
	r.completeOTA(n, claim, true)
	if n.OTAStatus != "ok" {
		t.Fatalf("fast confirmation lost: %+v", n)
	}
	r.noteOTAUnconfirmed(n, claim)
	if n.OTAError != "" || n.OTAStatus != "ok" {
		t.Fatal("late legacy ACK failure overwrote proven firmware confirmation")
	}
}

func TestOTALateTargetHelloRecoversEarlierUnchangedVersionFailure(t *testing.T) {
	r := newRegistry("")
	n := r.touch("board", "old", "", 300, "", "esp32s3")
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	testSendOTA(r, n)
	r.touch(n.MAC, "old", "", 320, "", "esp32s3")
	if n.OTAStatus != "failed" || n.OTAError != "version_unchanged" {
		t.Fatal("unchanged firmware did not report generic update failure")
	}
	r.touch(n.MAC, "new", "", 3, "", "esp32s3")
	if n.OTAStatus != "ok" || n.OTAError != "" {
		t.Fatal("a later target HELLO must supersede an inferred failure")
	}
}

func TestOTAOldVersionHelloInterleavingWriteDoesNotInferFailure(t *testing.T) {
	r := newRegistry("")
	n := r.touch("board", "old", "", 300, "", "esp32s3")
	r.setOTA(n.MAC, "https://example/new.bin", "new")
	claim, _ := r.claimOTA(n, 0)
	r.touch(n.MAC, "old", "", 310, "", "esp32s3")
	r.completeOTA(n, claim, true)
	if n.OTAStatus != "sent" {
		t.Fatalf("pre-delivery HELLO inferred failure: %+v", n)
	}
}

// Scripted net.Conn exercises the production legacy/mux delivery paths while
// independently controlling complete, partial, and ambiguous write results.
type otaScriptConn struct {
	net.Conn
	input          *strings.Reader
	failAt, writes int
	mode           string
}

func newOTAScriptConn(t *testing.T, input, mode string) *otaScriptConn {
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return &otaScriptConn{Conn: a, input: strings.NewReader(input), failAt: 2, mode: mode}
}

func (c *otaScriptConn) Read(p []byte) (int, error) { return c.input.Read(p) }
func (c *otaScriptConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writes == c.failAt {
		switch c.mode {
		case "failed":
			return 0, io.ErrClosedPipe
		case "partial":
			return len(p) - 1, nil
		case "ambiguous":
			return len(p), io.ErrClosedPipe
		}
	}
	return len(p), nil
}

func TestOTAMuxWriteResultControlsResend(t *testing.T) {
	for _, mode := range []string{"failed", "partial", "ambiguous", "success"} {
		t.Run(mode, func(t *testing.T) {
			b := newBroker(config{}, 1)
			n := b.reg.touch("board", "old", "", 500, "", "")
			b.reg.setOTA(n.MAC, "https://example/new.bin", "new")
			c := newOTAScriptConn(t, "", mode)
			b.serveMux(c, bufio.NewReader(c), n, helloOpts{MaxStreams: 4})
			v := b.reg.list()[0]
			if mode == "failed" || mode == "partial" {
				if v.OTAStatus != "queued" || v.PendingOTA == "" || v.OTAError != "write_failed" {
					t.Fatalf("definitely failed delivery not queued: %+v", v)
				}
			} else if v.OTAStatus != "sent" || v.PendingOTA != "" {
				t.Fatalf("complete write was queued for duplicate delivery: %+v", v)
			}
		})
	}
}

func TestOTALegacyWriteAndACKResult(t *testing.T) {
	for _, tc := range []struct{ name, mode, ack, status, reason string }{
		{"failed write", "failed", "", "queued", "write_failed"},
		{"partial write", "partial", "", "queued", "write_failed"},
		{"valid ACK", "success", "OK\n", "sent", ""},
		{"missing ACK", "success", "", "sent", "ack_unconfirmed"},
		{"negative ACK", "success", "ERR memory\n", "sent", "ack_unconfirmed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(config{token: "tok"}, 1)
			n := b.reg.touch("board", "old", "", 500, "", "")
			b.reg.setOTA(n.MAC, "https://example/new.bin", "new")
			c := newOTAScriptConn(t, "HELLO tok board old 500\n"+tc.ack, tc.mode)
			b.handlePoolConn(c)
			v := b.reg.list()[0]
			if v.OTAStatus != tc.status || v.OTAError != tc.reason {
				t.Fatalf("legacy delivery: %+v", v)
			}
		})
	}
}

func TestOTAMaxURLBytes(t *testing.T) {
	b := newBroker(config{}, 1)
	b.reg.touch("board", "old", "", 1, "", "")
	for _, size := range []int{255, 256} {
		url := "https://example/" + strings.Repeat("x", size-len("https://example/"))
		rr := postOTA(b, "board", `{"url":"`+url+`"}`)
		if (rr.Code == 200) != (size == 255) {
			t.Fatalf("URL size %d: %d %s", size, rr.Code, rr.Body.String())
		}
	}
}

func TestOTAShortFrameWriteIsError(t *testing.T) {
	c := newOTAScriptConn(t, "", "partial")
	c.failAt = 1
	s := newMuxSession(c, bufio.NewReader(c), &node{}, helloOpts{})
	written, err := s.writeFrameResult(frameOTA, 0, []byte("url"))
	if written || !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("partial write result: %v %v", written, err)
	}
}
