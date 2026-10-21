package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- in-process mux node (mirrors the firmware's mux1 protocol) ---------

type tnStream struct {
	tc      net.Conn
	mu      sync.Mutex
	cond    *sync.Cond
	credit  int
	pending int
	// broker -> node accounting: bytes received and credit granted back, so a
	// test can check the broker never exceeds the window the node advertised
	recvTotal, grantTotal int
}

// tnStats is what a test node observed about the broker's behaviour.
type tnStats struct {
	mu          sync.Mutex
	maxFrame    int // largest DATA payload the broker sent
	maxInflight int // peak of bytes received minus credit granted back, any stream
}

func (s *tnStats) note(frame, inflight int) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.maxFrame = max(s.maxFrame, frame)
	s.maxInflight = max(s.maxInflight, inflight)
	s.mu.Unlock()
}

func (s *tnStats) get() (maxFrame, maxInflight int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.maxFrame, s.maxInflight
}

type testMuxNode struct {
	c     net.Conn
	wmu   sync.Mutex
	mu    sync.Mutex
	st    map[uint32]*tnStream
	ota   chan string
	stats *tnStats
}

func (n *testMuxNode) send(typ byte, sid uint32, p []byte) {
	var h [muxHeaderLen]byte
	h[0] = typ
	binary.BigEndian.PutUint16(h[2:4], uint16(len(p)))
	binary.BigEndian.PutUint32(h[4:8], sid)
	n.wmu.Lock()
	n.c.Write(append(h[:], p...))
	n.wmu.Unlock()
}

// runMuxNode keeps one mux link up (reconnecting) until stop closes.
func runMuxNode(t *testing.T, poolAddr, token, mac string, allowLocal bool, maxStreams int, ota chan string, stop <-chan struct{}) {
	runMuxNodeOpts(t, poolAddr, token, mac, allowLocal, maxStreams, "", nil, ota, stop)
}

// runMuxNodeOpts is runMuxNode with extra HELLO tokens (e.g. " win=8192
// frame=4096 chip=esp32c3", leading space included) and optional stats.
func runMuxNodeOpts(t *testing.T, poolAddr, token, mac string, allowLocal bool, maxStreams int, opts string, stats *tnStats, ota chan string, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		c, err := net.DialTimeout("tcp", poolAddr, 2*time.Second)
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go func() { <-stop; c.Close() }()
		br := bufio.NewReader(c)
		fmt.Fprintf(c, "HELLO %s %s test-fw 1 - mux1 %d%s\n", token, mac, maxStreams, opts)
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "READY mux1" {
			c.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		n := &testMuxNode{c: c, st: map[uint32]*tnStream{}, ota: ota, stats: stats}
		n.loop(br, allowLocal, maxStreams)
		c.Close()
	}
}

func (n *testMuxNode) get(sid uint32) *tnStream {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.st[sid]
}

func (n *testMuxNode) drop(sid uint32) {
	n.mu.Lock()
	s := n.st[sid]
	delete(n.st, sid)
	n.mu.Unlock()
	if s != nil {
		s.tc.Close()
		s.mu.Lock()
		s.credit = -1
		s.cond.Broadcast()
		s.mu.Unlock()
	}
}

func (n *testMuxNode) loop(br *bufio.Reader, allowLocal bool, maxStreams int) {
	var h [muxHeaderLen]byte
	for {
		if _, err := io.ReadFull(br, h[:]); err != nil {
			return
		}
		ln := int(binary.BigEndian.Uint16(h[2:4]))
		sid := binary.BigEndian.Uint32(h[4:8])
		p := make([]byte, ln)
		if _, err := io.ReadFull(br, p); err != nil {
			return
		}
		switch h[0] {
		case framePing:
			n.send(framePong, 0, p)
		case frameOTA:
			if n.ota != nil {
				n.ota <- string(p)
			}
		case frameOpen:
			n.mu.Lock()
			full := len(n.st) >= maxStreams
			n.mu.Unlock()
			if full {
				n.send(frameOpenErr, sid, []byte("busy"))
				continue
			}
			go n.dial(sid, string(p), allowLocal)
		case frameData:
			if s := n.get(sid); s != nil {
				s.tc.Write(p) // test node: blocking write is fine
				s.mu.Lock()
				s.pending += len(p)
				s.recvTotal += len(p)
				n.stats.note(len(p), s.recvTotal-s.grantTotal)
				g := 0
				if s.pending >= muxGrantAt {
					g, s.pending = s.pending, 0
					s.grantTotal += g
				}
				s.mu.Unlock()
				if g > 0 {
					var b [4]byte
					binary.BigEndian.PutUint32(b[:], uint32(g))
					n.send(frameWindow, sid, b[:])
				}
			}
		case frameFin:
			if s := n.get(sid); s != nil {
				s.tc.(*net.TCPConn).CloseWrite()
			}
		case frameRst:
			n.drop(sid)
		case frameWindow:
			if s := n.get(sid); s != nil && len(p) == 4 {
				s.mu.Lock()
				s.credit += int(binary.BigEndian.Uint32(p))
				s.cond.Broadcast()
				s.mu.Unlock()
			}
		}
	}
}

func (n *testMuxNode) dial(sid uint32, req string, allowLocal bool) {
	f := strings.Fields(req)
	if len(f) != 2 {
		n.send(frameOpenErr, sid, []byte("badreq"))
		return
	}
	port, _ := strconv.Atoi(f[1])
	if blockedPortTest(port) {
		n.send(frameOpenErr, sid, []byte("blocked"))
		return
	}
	ips, err := net.LookupIP(f[0])
	if err != nil || len(ips) == 0 {
		n.send(frameOpenErr, sid, []byte("dial"))
		return
	}
	if !allowLocal && blockedIPtest(ips[0]) {
		n.send(frameOpenErr, sid, []byte("blocked"))
		return
	}
	tc, err := net.DialTimeout("tcp", net.JoinHostPort(f[0], f[1]), 5*time.Second)
	if err != nil {
		n.send(frameOpenErr, sid, []byte("dial"))
		return
	}
	s := &tnStream{tc: tc, credit: muxWindow}
	s.cond = sync.NewCond(&s.mu)
	n.mu.Lock()
	n.st[sid] = s
	n.mu.Unlock()
	n.send(frameOpenOK, sid, nil)
	buf := make([]byte, 4096)
	for {
		s.mu.Lock()
		for s.credit == 0 {
			s.cond.Wait()
		}
		cr := s.credit
		s.mu.Unlock()
		if cr < 0 {
			return
		}
		r, err := tc.Read(buf[:min(cr, len(buf))])
		if r > 0 {
			s.mu.Lock()
			s.credit -= r
			s.mu.Unlock()
			n.send(frameData, sid, buf[:r])
		}
		if err != nil {
			n.send(frameFin, sid, nil)
			return
		}
	}
}

// ---- tests ---------------------------------------------------------------

func spinMuxBroker(t *testing.T, maxStreams int) (b *broker, poolAddr, socksAddr string, stop func()) {
	return spinMuxBrokerOpts(t, maxStreams, "", nil)
}

// spinMuxBrokerOpts is spinMuxBroker whose node sends extra HELLO tokens and
// records what the broker did into stats.
func spinMuxBrokerOpts(t *testing.T, maxStreams int, opts string, stats *tnStats) (b *broker, poolAddr, socksAddr string, stop func()) {
	b = newBroker(config{token: "tok", dialWait: 2 * time.Second}, 64)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stopCh := make(chan struct{})
	go runMuxNodeOpts(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:50", true, maxStreams, opts, stats, nil, stopCh)
	waitFor(t, func() bool { return b.muxCount() == 1 })
	return b, poolLn.Addr().String(), socksLn.Addr().String(), func() { close(stopCh); poolLn.Close(); socksLn.Close() }
}

// firstMux is the broker's only mux session (tests spin exactly one node).
func firstMux(t *testing.T, b *broker) *muxSession {
	t.Helper()
	b.muxMu.Lock()
	defer b.muxMu.Unlock()
	if len(b.muxes) != 1 {
		t.Fatalf("want 1 mux link, have %d", len(b.muxes))
	}
	return b.muxes[0]
}

// A board without PSRAM advertises win=8192 frame=4096: the broker must size
// that session accordingly and never send a bigger frame or more unacknowledged
// bytes than that, while a 1 MiB body still arrives intact.
func TestMuxSmallWindowNodeNeverOverrun(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	stats := &tnStats{}
	b, _, socks, stop := spinMuxBrokerOpts(t, 4, " win=8192 frame=4096 chip=esp32c3", stats)
	defer stop()

	s := firstMux(t, b)
	if s.window != 8192 || s.frame != 4096 || s.maxStreams != 4 {
		t.Fatalf("session sized wrong: window %d frame %d max %d", s.window, s.frame, s.maxStreams)
	}
	if n := b.reg.list(); len(n) != 1 || n[0].Chip != "esp32c3" {
		t.Fatalf("chip not recorded: %+v", n)
	}
	big, err := fetchVia(socks, oh, op, "/big")
	if err != nil || strings.Count(big, "A") != 1<<20 {
		t.Fatalf("1 MiB body over a small-window link: err=%v got %d A's", err, strings.Count(big, "A"))
	}
	// the GET only carries bulk node->broker; push 256 KiB broker->node too
	resp, err := postVia(socks, oh, op, "/echo-len", []byte(strings.Repeat("B", 256<<10)))
	if err != nil || !strings.Contains(resp, "LEN=262144") {
		t.Fatalf("256 KiB upload over a small-window link: err=%v resp=%q", err, resp)
	}
	maxFrame, maxInflight := stats.get()
	if maxInflight < 4096 {
		t.Fatalf("test did not exercise the broker->node window (peak in flight %d)", maxInflight)
	}
	if maxFrame > 4096 {
		t.Errorf("broker sent a %d-byte DATA frame to a frame=4096 node", maxFrame)
	}
	if maxInflight > 8192 {
		t.Errorf("broker had %d bytes in flight to a win=8192 node", maxInflight)
	}
}

// Firmware that predates the tokens (2.4.21-mux1.x) keeps today's sizes.
func TestMuxLegacyHelloDefaults(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b, _, socks, stop := spinMuxBroker(t, 24)
	defer stop()

	s := firstMux(t, b)
	if s.window != 32768 || s.frame != 16384 {
		t.Fatalf("legacy session sized wrong: window %d frame %d", s.window, s.frame)
	}
	if n := b.reg.list(); len(n) != 1 || n[0].Chip != "" {
		t.Fatalf("legacy node must have no chip: %+v", n)
	}
	big, err := fetchVia(socks, oh, op, "/big")
	if err != nil || strings.Count(big, "A") != 1<<20 {
		t.Fatalf("1 MiB body over a legacy link: err=%v", err)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

func TestMuxChainReachabilityAndBigBody(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b, _, socks, stop := spinMuxBroker(t, 24)
	defer stop()

	body, err := fetchVia(socks, oh, op, "/hello")
	if err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("mux fetch failed: err=%v body=%q", err, body)
	}
	big, err := fetchVia(socks, oh, op, "/big")
	if err != nil || strings.Count(big, "A") != 1<<20 {
		t.Fatalf("1 MiB body corrupted over mux: err=%v got %d A's", err, strings.Count(big, "A"))
	}
	time.Sleep(100 * time.Millisecond)
	nodes := b.reg.list()
	if len(nodes) != 1 || nodes[0].Mux != 1 || nodes[0].DialsOK != 2 || nodes[0].BytesDown < 1<<20 || !nodes[0].Online {
		t.Fatalf("node stats wrong: %+v", nodes[0].node)
	}
}

// One link must carry far more parallel customers than the old pool of 4.
func TestMuxManyConcurrentStreamsOneLink(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	_, _, socks, stop := spinMuxBroker(t, 64)
	defer stop()

	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := fetchVia(socks, oh, op, "/big")
			if err != nil || strings.Count(body, "A") != 1<<20 {
				errs <- fmt.Errorf("err=%v len=%d", err, len(body))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}
}

// A client that stops reading must not stall other streams on the same link.
func TestMuxSlowStreamDoesNotBlockOthers(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	_, _, socks, stop := spinMuxBroker(t, 24)
	defer stop()

	slow, err := socks5Connect(socks, oh, op)
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	fmt.Fprintf(slow, "GET /big HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n")
	// never read from `slow`
	time.Sleep(300 * time.Millisecond)

	done := make(chan error, 1)
	go func() {
		body, err := fetchVia(socks, oh, op, "/hello")
		if err == nil && !strings.Contains(body, "HELLO-ORIGIN") {
			err = fmt.Errorf("bad body %q", body)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second stream stalled behind a slow reader")
	}
}

func TestMuxSafetyFilterAndBusy(t *testing.T) {
	b := newBroker(config{token: "tok", dialWait: 500 * time.Millisecond}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	defer func() { close(stop); poolLn.Close(); socksLn.Close() }()
	go runMuxNode(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:51", false, 24, nil, stop)
	waitFor(t, func() bool { return b.muxCount() == 1 })

	if _, err := socks5Connect(socksLn.Addr().String(), "127.0.0.1", 80); err == nil || !strings.Contains(err.Error(), "rep=5") {
		t.Fatalf("loopback must be refused over mux, got %v", err)
	}
	if _, err := socks5Connect(socksLn.Addr().String(), "example.com", 25); err == nil || !strings.Contains(err.Error(), "rep=5") {
		t.Fatalf("SMTP must be refused over mux, got %v", err)
	}
}

func TestMuxOTADeliveredToConnectedLink(t *testing.T) {
	withMuxTimers(t, time.Hour, time.Hour, time.Hour, 50*time.Millisecond)
	ota := make(chan string, 1)
	b := newBroker(config{token: "tok", dialWait: time.Second}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	stop := make(chan struct{})
	defer func() {
		close(stop)
		poolLn.Close()
		waitFor(t, func() bool { return b.muxCount() == 0 }) // link fully down before timers are restored
	}()
	go runMuxNode(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:60", true, 24, ota, stop)
	waitFor(t, func() bool { return b.muxCount() == 1 })
	if !b.reg.setOTA("aa:bb:cc:dd:ee:60", "https://x/fw.bin", "") {
		t.Fatal("setOTA")
	}
	select {
	case u := <-ota:
		if u != "https://x/fw.bin" {
			t.Fatalf("got %q", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OTA not delivered while link stayed up")
	}
	// the live-link push goes through takeOTA like a push at HELLO
	for _, v := range b.reg.list() {
		if v.MAC == "aa:bb:cc:dd:ee:60" && v.OTAStatus != "sent" {
			t.Fatalf("OTA status %q after a live-link push, want sent", v.OTAStatus)
		}
	}
}

func TestMuxOTAAndBlock(t *testing.T) {
	b := newBroker(config{token: "tok", dialWait: 300 * time.Millisecond}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	defer func() { close(stop); poolLn.Close(); socksLn.Close() }()

	mac := "aa:bb:cc:dd:ee:52"
	b.reg.touch(mac, "", "", 0, "", "")
	b.reg.setOTA(mac, "https://flash.example/fw.bin", "")
	ota := make(chan string, 1)
	go runMuxNode(t, poolLn.Addr().String(), "tok", mac, true, 24, ota, stop)
	select {
	case u := <-ota:
		if u != "https://flash.example/fw.bin" {
			t.Fatalf("ota url %q", u)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OTA frame not delivered on mux link")
	}

	// A blocked node gets no new streams even while its link is still up.
	b.reg.setBlocked(mac, true)
	oh, op, oclose := originServer(t)
	defer oclose()
	if _, err := socks5Connect(socksLn.Addr().String(), oh, op); err == nil {
		t.Fatal("blocked node must not serve streams")
	}
}

// Old firmware (one DIAL per connection) and new mux firmware work side by side.
func TestMuxAndLegacyCoexist(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b := newBroker(config{token: "tok", dialWait: 2 * time.Second}, 64)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	defer func() { close(stop); poolLn.Close(); socksLn.Close() }()
	go runNodeMAC(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:60", true, false, stop)
	waitFor(t, func() bool { return len(b.idle) > 0 })
	// legacy only
	if body, err := fetchVia(socksLn.Addr().String(), oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("legacy path broken: %v", err)
	}
	go runMuxNode(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:61", true, 24, nil, stop)
	waitFor(t, func() bool { return b.muxCount() == 1 })
	for i := 0; i < 5; i++ {
		if body, err := fetchVia(socksLn.Addr().String(), oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
			t.Fatalf("mixed fetch %d failed: %v", i, err)
		}
	}
}

// A node reports health in STATS frames (sid 0): the broker keeps the latest
// sample and the uptime it carries. An unknown sid-0 frame type is ignored
// rather than dropping the link, so newer firmware keeps working on an older
// broker and vice versa.
func TestMuxStatsFrame(t *testing.T) {
	b := newBroker(config{token: "tok", dialWait: 2 * time.Second}, 64)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	defer poolLn.Close()
	go b.servePool(poolLn)
	c, err := net.Dial("tcp", poolLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	br := bufio.NewReader(c)
	fmt.Fprintf(c, "HELLO tok aa:bb:cc:dd:ee:60 2.4.23-c3 7 - mux1 4 chip=esp32c3\n")
	if line, _ := br.ReadString('\n'); strings.TrimSpace(line) != "READY mux1" {
		t.Fatalf("hello: %q", line)
	}
	n := &testMuxNode{c: c}
	n.send(0x7F, 0, []byte("a control frame from the future"))
	n.send(frameStats, 0, []byte("up=600 heap=140000 heapmin=26000 heaptot=320000 cpu=12 app=1245184 slot=2031616 flash=4194304 streams=0"))
	waitFor(t, func() bool { l := b.reg.list(); return len(l) == 1 && l[0].Stats != nil })
	v := b.reg.list()[0]
	if v.Stats.HeapFree != 140000 || v.Stats.CPUPct != 12 || v.Stats.FlashBytes != 4194304 || v.Stats.At == "" {
		t.Fatalf("stats = %+v", v.Stats)
	}
	if v.UptimeSec < 600 || v.UptimeSec > 605 {
		t.Fatalf("uptime from STATS: %d", v.UptimeSec)
	}
	if b.muxCount() != 1 {
		t.Fatal("the unknown sid-0 frame dropped the link")
	}
	// the JSON the dashboard reads carries the sample
	data, _ := json.Marshal(v)
	if !strings.Contains(string(data), `"heap_free":140000`) || !strings.Contains(string(data), `"uptime_sec":60`) {
		t.Fatalf("json: %s", data)
	}
	// the link closing freezes the uptime and stamps last_seen
	c.Close()
	waitFor(t, func() bool { return b.muxCount() == 0 })
	v1 := b.reg.list()[0]
	time.Sleep(1100 * time.Millisecond)
	if v2 := b.reg.list()[0]; v2.UptimeSec != v1.UptimeSec || v2.LastSeen < v1.Stats.At {
		t.Fatalf("after link down: %d -> %d, last_seen %s", v1.UptimeSec, v2.UptimeSec, v2.LastSeen)
	}
}
