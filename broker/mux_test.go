package main

import (
	"bufio"
	"encoding/binary"
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
}

type testMuxNode struct {
	c   net.Conn
	wmu sync.Mutex
	mu  sync.Mutex
	st  map[uint32]*tnStream
	ota chan string
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
		fmt.Fprintf(c, "HELLO %s %s test-fw 1 - mux1 %d\n", token, mac, maxStreams)
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "READY mux1" {
			c.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		n := &testMuxNode{c: c, st: map[uint32]*tnStream{}, ota: ota}
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
				g := 0
				if s.pending >= muxGrantAt {
					g, s.pending = s.pending, 0
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
	b = newBroker(config{token: "tok", dialWait: 2 * time.Second}, 64)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stopCh := make(chan struct{})
	go runMuxNode(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:50", true, maxStreams, nil, stopCh)
	waitFor(t, func() bool { return b.muxCount() == 1 })
	return b, poolLn.Addr().String(), socksLn.Addr().String(), func() { close(stopCh); poolLn.Close(); socksLn.Close() }
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
	if !b.reg.setOTA("aa:bb:cc:dd:ee:60", "https://x/fw.bin") {
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
	b.reg.touch(mac, "", "", 0, "")
	b.reg.setOTA(mac, "https://flash.example/fw.bin")
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
