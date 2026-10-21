package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMuxAcceptedCapabilitiesOnNodes(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		want         int
	}{
		{"legacy omitted limit", "", 24},
		{"legacy zero limit", " 0", 24},
		{"legacy malformed limit", " unknown", 24},
		{"current C3", " 4 win=8192 frame=4096 chip=esp32c3", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(config{token: "tok"}, 8)
			server, client := net.Pipe()
			defer client.Close()
			done := make(chan struct{})
			go func() { b.handlePoolConn(server); close(done) }()
			client.SetDeadline(time.Now().Add(3 * time.Second))
			fmt.Fprintf(client, "HELLO tok aa:bb:cc:dd:ee:01 fw 9 - mux1%s\n", tc.suffix)
			if line, err := bufio.NewReader(client).ReadString('\n'); err != nil || line != "READY mux1\n" {
				t.Fatalf("READY: %q, %v", line, err)
			}
			waitFor(t, func() bool { return b.muxCount() == 1 })
			w := httptest.NewRecorder()
			b.statusMux().ServeHTTP(w, httptest.NewRequest("GET", "/nodes", nil))
			var nodes []node
			if err := json.Unmarshal(w.Body.Bytes(), &nodes); err != nil {
				t.Fatal(err)
			}
			if len(nodes) != 1 || nodes[0].MaxStreams != tc.want || nodes[0].Mux != 1 || nodes[0].LinkSince == "" {
				t.Fatalf("/nodes = %s", w.Body.String())
			}
			if _, err := time.Parse(time.RFC3339, nodes[0].LinkSince); err != nil {
				t.Fatal(err)
			}
			client.Close()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("mux did not stop")
			}
			if v := b.reg.list()[0]; v.LinkSince != "" || v.MaxStreams != tc.want || v.Mux != 0 {
				t.Fatalf("disconnected node: %+v", v.node)
			}
		})
	}
}

func TestMuxFailedReadyHasNoLinkMetadata(t *testing.T) {
	b := newBroker(config{}, 8)
	n := b.reg.touch("aa:bb", "fw", "", 0, "", "")
	server, client := net.Pipe()
	client.Close()
	b.serveMux(server, bufio.NewReader(server), n, helloOpts{MaxStreams: 4})
	if v := b.reg.list()[0]; v.MaxStreams != 0 || v.LinkSince != "" || v.Mux != 0 {
		t.Fatalf("failed READY published a link: %+v", v.node)
	}
}

func TestMuxReconnectMetadataAndConcurrentSnapshots(t *testing.T) {
	r := newRegistry("")
	n := r.touch("aa:bb", "fw", "", 10, "", "")
	r.muxUp(n, 24)
	r.muxUp(n, 4) // replacement accepted before old link is found dead
	wantSince := r.list()[0].LinkSince
	n.addMux(-1)
	r.linkDown(n)
	if v := r.list()[0]; v.Mux != 1 || v.LinkSince != wantSince || v.MaxStreams != 4 || !n.offAt.IsZero() {
		t.Fatalf("old link cleared replacement: %+v", v.node)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.muxUp(n, 4)
				atomic.AddInt64(&n.DialsPinnedOffline, 1)
				r.list()
				n.addMux(-1)
				r.linkDown(n)
			}
		}()
	}
	wg.Wait()
	if v := r.list()[0]; v.Mux != 1 || v.LinkSince == "" || v.DialsPinnedOffline != 400 {
		t.Fatalf("concurrent snapshot = %+v", v.node)
	}
	n.addMux(-1)
	r.linkDown(n)
	if r.list()[0].LinkSince != "" {
		t.Fatal("last link must clear link_since")
	}
}

func TestMuxStatusFieldsNeverPersist(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nodes.json")
	r := newRegistry(file)
	n := r.touch("aa:bb", "fw", "", 1, "", "")
	r.muxUp(n, 4)
	atomic.AddInt64(&n.DialsPinnedOffline, 5)
	r.setBlocked(n.MAC, true) // force a save while live fields are populated
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"max_streams", "link_since", "dials_pinned_offline", "mux_links"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("live field persisted: %s", key)
		}
	}
	if v := newRegistry(file).list()[0]; v.MaxStreams != 0 || v.LinkSince != "" || v.DialsPinnedOffline != 0 {
		t.Fatalf("restart carried live metadata: %+v", v.node)
	}
}

func TestPinnedOfflineNodeCountersIncludeBlockedAndFull(t *testing.T) {
	for _, mode := range []string{"offline", "blocked", "full", "closed", "already tried"} {
		t.Run(mode, func(t *testing.T) {
			b := newBroker(config{}, 8)
			n := b.reg.touch("aa:bb", "fw", "", 1, "", "")
			tried := map[*muxSession]bool{}
			if mode != "offline" {
				s := newMuxSession(nil, nil, n, helloOpts{MaxStreams: 1})
				b.muxes = []*muxSession{s}
				switch mode {
				case "blocked":
					b.reg.setBlocked(n.MAC, true)
				case "full":
					s.streams[1] = &muxStream{} // no open/idle stream eligible for eviction
				case "closed":
					s.closed = true
				case "already tried":
					tried[s] = true
				}
			}
			st, rep, ok := b.dialViaMux("example.com", 443, tried, n)
			if st != nil || rep != repHostFail || !ok || atomic.LoadInt64(&b.dialsPinnedOffline) != 1 || b.reg.list()[0].DialsPinnedOffline != 1 {
				t.Fatalf("refusal not attributed: rep=%d ok=%v node=%+v", rep, ok, b.reg.list()[0].node)
			}
		})
	}
}
