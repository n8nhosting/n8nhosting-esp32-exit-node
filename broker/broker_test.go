package main

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- in-process test node (mirrors the ESP32 rproxy protocol) ----------

func blockedIPtest(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate()
	}
	if ip4[0] == 0 || ip4[0] == 127 {
		return true
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return true
	}
	if ip4[0] == 100 && ip4[1]&0xc0 == 64 {
		return true
	}
	return false
}
func blockedPortTest(p int) bool { return p == 0 || p == 25 || p == 465 || p == 587 }

// runNode dials the broker pool, does the handshake, serves one DIAL, then
// loops to refill — exactly like the firmware worker.
func runNode(t *testing.T, poolAddr, token string, allowLocal bool, stop <-chan struct{}) {
	runNodeMAC(t, poolAddr, token, "aa:bb:cc:dd:ee:01", allowLocal, false, stop)
}

// runNodeMAC lets tests pick the MAC and optionally speak TLS (pinned to the test CA).
func runNodeMAC(t *testing.T, poolAddr, token, mac string, allowLocal, useTLS bool, stop <-chan struct{}) {
	for {
		select {
		case <-stop:
			return
		default:
		}
		var c net.Conn
		var err error
		if useTLS {
			c, err = tls.Dial("tcp", poolAddr, &tls.Config{RootCAs: testCAPool, ServerName: "node.jbacteam.com"})
		} else {
			c, err = net.DialTimeout("tcp", poolAddr, 2*time.Second)
		}
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		br := bufio.NewReader(c)
		fmt.Fprintf(c, "HELLO %s %s test-fw\n", token, mac)
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "READY" {
			c.Close()
			continue
		}
		line, err = br.ReadString('\n')
		if err != nil {
			c.Close()
			continue
		}
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) != 3 || f[0] != "DIAL" {
			c.Write([]byte("ERR badreq\n"))
			c.Close()
			continue
		}
		host, portS := f[1], f[2]
		port, _ := strconv.Atoi(portS)
		if blockedPortTest(port) {
			c.Write([]byte("ERR blocked\n"))
			c.Close()
			continue
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			c.Write([]byte("ERR dial\n"))
			c.Close()
			continue
		}
		if !allowLocal && blockedIPtest(ips[0]) {
			c.Write([]byte("ERR blocked\n"))
			c.Close()
			continue
		}
		tc, err := net.DialTimeout("tcp", net.JoinHostPort(host, portS), 5*time.Second)
		if err != nil {
			c.Write([]byte("ERR dial\n"))
			c.Close()
			continue
		}
		c.Write([]byte("OK\n"))
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.Copy(tc, br); tc.(*net.TCPConn).CloseWrite() }()
		go func() { defer wg.Done(); io.Copy(c, tc); closeWrite(c) }()
		wg.Wait()
		tc.Close()
		c.Close()
	}
}

// spinBroker starts a broker with ephemeral listeners and `n` nodes.
func spinBroker(t *testing.T, nodes int, allowLocal bool) (socksAddr string, stop func()) {
	b := newBroker(config{token: "tok", dialWait: 3 * time.Second, nodesFile: ""}, 64)
	poolLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	socksLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stopCh := make(chan struct{})
	for i := 0; i < nodes; i++ {
		go runNode(t, poolLn.Addr().String(), "tok", allowLocal, stopCh)
	}
	time.Sleep(300 * time.Millisecond) // let nodes park
	return socksLn.Addr().String(), func() { close(stopCh); poolLn.Close(); socksLn.Close() }
}

// socks5Connect performs an RFC-1928 CONNECT (no auth), domain addr.
func socks5Connect(proxy, host string, port int) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		return nil, err
	}
	c.SetDeadline(time.Now().Add(10 * time.Second))
	c.Write([]byte{5, 1, 0})
	rep := make([]byte, 2)
	if _, err := io.ReadFull(c, rep); err != nil || rep[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("method reject")
	}
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	c.Write(req)
	resp := make([]byte, 10)
	if _, err := io.ReadFull(c, resp); err != nil {
		c.Close()
		return nil, err
	}
	if resp[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("rep=%d", resp[1])
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

func fetchVia(proxy, host string, port int, path string) (string, error) {
	conn, err := socks5Connect(proxy, host, port)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: %s\r\nConnection: close\r\n\r\n", path, host)
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	data, _ := io.ReadAll(conn)
	return string(data), nil
}

// postVia sends body to path through the proxy (customer -> broker -> node ->
// origin, i.e. the broker->node direction carries the bulk) and returns the
// raw response.
func postVia(proxy, host string, port int, path string, body []byte) (string, error) {
	conn, err := socks5Connect(proxy, host, port)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", path, host, len(body))
	if _, err := conn.Write(body); err != nil {
		return "", err
	}
	conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	data, _ := io.ReadAll(conn)
	return string(data), nil
}

func originServer(t *testing.T) (host string, port int, close func()) {
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "HELLO-ORIGIN") })
	big := strings.Repeat("A", 1<<20)
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(big))) // avoid chunked encoding
		fmt.Fprint(w, big)
	})
	// echoes the number of body bytes received, e.g. "LEN=262144"
	mux.HandleFunc("/echo-len", func(w http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		fmt.Fprintf(w, "LEN=%d", n)
	})
	srv := httptest.NewServer(mux)
	u := strings.TrimPrefix(srv.URL, "http://")
	h, p, _ := net.SplitHostPort(u)
	pn, _ := strconv.Atoi(p)
	return h, pn, srv.Close
}

func TestChainReachability(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	socks, stop := spinBroker(t, 4, true)
	defer stop()

	body, err := fetchVia(socks, oh, op, "/hello")
	if err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("small fetch: err=%v body=%q", err, body)
	}
	// pool refill: second fetch must also work
	if b2, e2 := fetchVia(socks, oh, op, "/hello"); e2 != nil || !strings.Contains(b2, "HELLO-ORIGIN") {
		t.Fatalf("pool refill fetch: err=%v", e2)
	}
	// 1MB integrity
	bodyBig, _ := fetchVia(socks, oh, op, "/big")
	if idx := strings.Index(bodyBig, "\r\n\r\n"); idx < 0 || len(bodyBig)-idx-4 != 1<<20 {
		t.Fatalf("1MB transfer wrong size: got %d", len(bodyBig))
	}
}

func TestChainConcurrent(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	socks, stop := spinBroker(t, 4, true)
	defer stop()

	var wg sync.WaitGroup
	ok := make([]bool, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, e := fetchVia(socks, oh, op, "/hello")
			ok[i] = e == nil && strings.Contains(b, "HELLO-ORIGIN")
		}(i)
	}
	wg.Wait()
	for i, v := range ok {
		if !v {
			t.Fatalf("concurrent fetch %d failed", i)
		}
	}
}

func TestNoNodeRefused(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	socks, stop := spinBroker(t, 0, true) // no nodes
	defer stop()
	if _, err := socks5Connect(socks, oh, op); err == nil {
		t.Fatal("expected refusal when no node is available")
	}
}

func TestSafetyFilterBlocksLoopback(t *testing.T) {
	oh, op, oclose := originServer(t) // loopback origin
	defer oclose()
	socks, stop := spinBroker(t, 2, false) // filter ON
	defer stop()
	if _, err := socks5Connect(socks, oh, op); err == nil {
		t.Fatal("expected loopback target to be blocked by the node filter")
	}
}

func TestSafetyFilterBlocksSMTP(t *testing.T) {
	socks, stop := spinBroker(t, 2, true) // allowLocal true, but port filter still applies
	defer stop()
	if _, err := socks5Connect(socks, "example.com", 25); err == nil {
		t.Fatal("expected SMTP port 25 to be blocked")
	}
}

func TestBlockedIPRanges(t *testing.T) {
	blocked := []string{"10.0.0.1", "127.0.0.1", "169.254.1.1", "172.16.0.1", "192.168.1.1", "100.64.0.1", "224.0.0.1"}
	allowed := []string{"8.8.8.8", "1.1.1.1", "210.246.215.196", "172.32.0.1"}
	for _, s := range blocked {
		if !blockedIPtest(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
	for _, s := range allowed {
		if blockedIPtest(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

// ---- TLS + node registry tests -----------------------------------------

var testCAPool *x509.CertPool

func mustLoadTestPKI(t *testing.T) tls.Certificate {
	ca, err := os.ReadFile("testdata/ca.crt")
	if err != nil {
		t.Skip("testdata/ca.crt missing")
	}
	testCAPool = x509.NewCertPool()
	testCAPool.AppendCertsFromPEM(ca)
	cert, err := tls.LoadX509KeyPair("testdata/broker.crt", "testdata/broker.key")
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestTLSPoolWithPinnedCA(t *testing.T) {
	cert := mustLoadTestPKI(t)
	oh, op, oclose := originServer(t)
	defer oclose()

	b := newBroker(config{token: "tok", dialWait: 3 * time.Second}, 64)
	poolLn, _ := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	defer func() { close(stop); poolLn.Close(); socksLn.Close() }()
	for i := 0; i < 2; i++ {
		go runNodeMAC(t, poolLn.Addr().String(), "tok", "aa:bb:cc:dd:ee:10", true, true, stop)
	}
	time.Sleep(400 * time.Millisecond)

	body, err := fetchVia(socksLn.Addr().String(), oh, op, "/hello")
	if err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("TLS chain fetch failed: err=%v", err)
	}
	time.Sleep(300 * time.Millisecond) // let the relay goroutines finish accounting
	// node registered with MAC + firmware
	nodes := b.reg.list()
	if len(nodes) != 1 || nodes[0].MAC != "aa:bb:cc:dd:ee:10" || nodes[0].Firmware != "test-fw" || !nodes[0].Online {
		t.Fatalf("registry wrong: %+v", nodes)
	}
	if nodes[0].DialsOK < 1 || nodes[0].BytesDown == 0 {
		t.Fatalf("per-node stats not attributed: %+v", nodes[0])
	}
}

func TestTLSRejectsUntrustedClientCA(t *testing.T) {
	cert := mustLoadTestPKI(t)
	b := newBroker(config{token: "tok", dialWait: time.Second}, 8)
	poolLn, _ := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	go b.servePool(poolLn)
	defer poolLn.Close()
	// A client that does NOT trust our CA must fail the handshake (what a
	// tampered/other server would look like from the firmware's side).
	_, err := tls.Dial("tcp", poolLn.Addr().String(), &tls.Config{RootCAs: x509.NewCertPool()})
	if err == nil {
		t.Fatal("expected handshake failure with empty trust store")
	}
}

func TestBlockedNodeIsRefused(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b := newBroker(config{token: "tok", dialWait: 1500 * time.Millisecond}, 64)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	defer func() { close(stop); poolLn.Close(); socksLn.Close() }()

	// pre-block the MAC, then let it try to join
	b.reg.touch("de:ad:be:ef:00:01", "", "", 0, "", "")
	b.reg.setBlocked("de:ad:be:ef:00:01", true)
	go runNodeMAC(t, poolLn.Addr().String(), "tok", "de:ad:be:ef:00:01", true, false, stop)
	time.Sleep(400 * time.Millisecond)

	if _, err := socks5Connect(socksLn.Addr().String(), oh, op); err == nil {
		t.Fatal("blocked node must not serve dials")
	}
	// allow it -> dials work
	b.reg.setBlocked("de:ad:be:ef:00:01", false)
	time.Sleep(400 * time.Millisecond)
	if body, err := fetchVia(socksLn.Addr().String(), oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("allowed node should serve: %v", err)
	}
}

func TestRegistryPersistence(t *testing.T) {
	f := t.TempDir() + "/nodes.json"
	r := newRegistry(f)
	r.touch("11:22:33:44:55:66", "fw1", "1.2.3.4", 0, "", "esp32c3")
	r.setBlocked("11:22:33:44:55:66", true)
	r2 := newRegistry(f)
	l := r2.list()
	if len(l) != 1 || !l[0].Blocked || l[0].Firmware != "fw1" {
		t.Fatalf("persistence failed: %+v", l)
	}
	// the chip must survive a broker restart, or the OTA target guard treats a
	// C3 as an S3 until the board HELLOs again
	if l[0].Chip != "esp32c3" {
		t.Fatalf("chip not persisted: %q", l[0].Chip)
	}
}

func TestOTACommandDelivered(t *testing.T) {
	b := newBroker(config{token: "tok", dialWait: time.Second}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	defer poolLn.Close()

	mac := "0a:0b:0c:0d:0e:0f"
	b.reg.touch(mac, "", "", 0, "", "")
	b.reg.setOTA(mac, "https://flash.example/fw.bin", "")

	c, _ := net.Dial("tcp", poolLn.Addr().String())
	fmt.Fprintf(c, "HELLO tok %s fw1\n", mac)
	br := bufio.NewReader(c)
	l1, _ := br.ReadString('\n')
	l2, _ := br.ReadString('\n')
	if strings.TrimSpace(l1) != "READY" || strings.TrimSpace(l2) != "OTA https://flash.example/fw.bin" {
		t.Fatalf("expected READY then OTA, got %q %q", l1, l2)
	}
	c.Write([]byte("OK\n"))
	// second connection must NOT get OTA again (one-shot)
	c2, _ := net.Dial("tcp", poolLn.Addr().String())
	fmt.Fprintf(c2, "HELLO tok %s fw1\n", mac)
	br2 := bufio.NewReader(c2)
	br2.ReadString('\n')
	c2.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, err := br2.ReadString('\n'); err == nil {
		t.Fatal("OTA should be one-shot")
	}
}

func TestNodeReportsUptimeAndSSID(t *testing.T) {
	b := newBroker(config{token: "tok", dialWait: time.Second}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	defer poolLn.Close()
	c, _ := net.Dial("tcp", poolLn.Addr().String())
	// SSID "My Home 2.4G" base64
	ssidB64 := base64.StdEncoding.EncodeToString([]byte("My Home 2.4G"))
	fmt.Fprintf(c, "HELLO tok aa:bb:cc:00:11:22 2.4.21 3600 %s\n", ssidB64)
	br := bufio.NewReader(c)
	if l, _ := br.ReadString('\n'); strings.TrimSpace(l) != "READY" {
		t.Fatalf("no READY")
	}
	time.Sleep(100 * time.Millisecond)
	nodes := b.reg.list()
	if len(nodes) != 1 || nodes[0].UptimeSec != 3600 || nodes[0].SSID != "My Home 2.4G" {
		t.Fatalf("uptime/ssid not captured: %+v", nodes[0].node)
	}
}

func TestOTAStatusTracking(t *testing.T) {
	r := newRegistry("")
	mac := "aa:bb:cc:00:00:01"
	r.touch(mac, "1.0", "1.2.3.4", 500, "", "")
	if !r.setOTA(mac, "https://flash.example/2.0.bin", "2.0") {
		t.Fatal("setOTA")
	}
	n := r.nodes[mac]
	if n.OTAStatus != "queued" {
		t.Fatalf("status %q, want queued", n.OTAStatus)
	}
	if u := r.takeOTA(n); u != "https://flash.example/2.0.bin" || n.OTAStatus != "sent" {
		t.Fatalf("take: %q %q", u, n.OTAStatus)
	}
	// still downloading on the old firmware, no reboot yet: stays "sent"
	r.touch(mac, "1.0", "1.2.3.4", 520, "", "")
	if n.OTAStatus != "sent" {
		t.Fatalf("status %q, want sent", n.OTAStatus)
	}
	// came back after a reboot on the new firmware
	r.touch(mac, "2.0", "1.2.3.4", 8, "", "")
	if n.OTAStatus != "ok" {
		t.Fatalf("status %q, want ok", n.OTAStatus)
	}

	// next update: the board reboots but is back on 2.0 (image rolled back)
	r.setOTA(mac, "https://flash.example/3.0.bin", "3.0")
	r.takeOTA(n)
	r.touch(mac, "2.0", "1.2.3.4", 60, "", "")
	r.touch(mac, "2.0", "1.2.3.4", 5, "", "")
	if n.OTAStatus != "failed" {
		t.Fatalf("status %q, want failed", n.OTAStatus)
	}

	// firmware 2.4.22+ (it sends chip=) closes the link for the download and
	// only HELLOs again when the download is over: back on the same version
	// without a reboot means it failed (older firmware keeps the link while
	// downloading, so for it "same version, no reboot" stays "sent")
	r.setOTA(mac, "https://flash.example/4.0.bin", "4.0")
	r.takeOTA(n)
	r.touch(mac, "2.0", "1.2.3.4", 100, "", "esp32c3")
	if n.OTAStatus != "failed" {
		t.Fatalf("status %q after a 2.4.22+ node came back on its old firmware, want failed", n.OTAStatus)
	}

	// cancel only clears a queued update
	r.setOTA(mac, "https://flash.example/3.0.bin", "3.0")
	r.setOTA(mac, "", "")
	if n.PendingOTA != "" || n.OTAStatus != "" {
		t.Fatalf("cancel: %q %q", n.PendingOTA, n.OTAStatus)
	}
}

// postOTA sends POST /nodes/{mac}/ota with body to the broker's status API.
func postOTA(b *broker, mac, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	b.statusMux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/nodes/"+mac+"/ota", strings.NewReader(body)))
	return rec
}

// An OTA push names the image's target chip; the broker refuses it when the
// node reported a different chip. Nodes that never sent chip= (firmware before
// 2.4.22) are S3 boards, and a push without a target (older dashboard) is let
// through as today.
func TestOTATargetGuard(t *testing.T) {
	b := newBroker(config{token: "tok"}, 64)
	c3, s3old := "aa:bb:cc:00:00:c3", "aa:bb:cc:00:00:53"
	b.reg.touch(c3, "2.4.22", "1.2.3.4", 10, "", "esp32c3")
	b.reg.touch(s3old, "2.4.21-mux1.2", "1.2.3.4", 10, "", "")

	cases := []struct {
		mac, target string
		code        int
	}{
		{c3, "esp32s3", http.StatusConflict},
		{c3, "esp32c3", http.StatusOK},
		{s3old, "esp32s3", http.StatusOK},
		{s3old, "esp32c3", http.StatusConflict},
		{c3, "", http.StatusOK},
	}
	// firmware is not signed, so the download must ride on TLS: plain http is
	// refused unless the broker was started with OTA_ALLOW_HTTP (test rigs)
	if rec := postOTA(b, c3, `{"url":"http://192.168.1.10:8088/fw.bin","version":"2.4.22","target":"esp32c3"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("http:// OTA URL: status %d, want 400", rec.Code)
	}
	if b.reg.nodes[c3].PendingOTA != "" {
		t.Errorf("http:// OTA URL was queued: %q", b.reg.nodes[c3].PendingOTA)
	}
	b.cfg.allowHTTPOTA = true
	if rec := postOTA(b, c3, `{"url":"http://192.168.1.10:8088/fw.bin","version":"2.4.22","target":"esp32c3"}`); rec.Code != http.StatusOK {
		t.Errorf("http:// OTA URL with OTA_ALLOW_HTTP: status %d, want 200", rec.Code)
	}
	b.cfg.allowHTTPOTA = false
	b.reg.setOTA(c3, "", "")

	for _, c := range cases {
		b.reg.setOTA(c.mac, "", "") // clear whatever the previous case queued
		rec := postOTA(b, c.mac, fmt.Sprintf(`{"url":"https://flash.example/fw.bin","version":"2.4.22","target":%q}`, c.target))
		if rec.Code != c.code {
			t.Errorf("%s target %q: status %d, want %d (%s)", c.mac, c.target, rec.Code, c.code, strings.TrimSpace(rec.Body.String()))
		}
		pending := b.reg.nodes[c.mac].PendingOTA
		if c.code == http.StatusOK && pending == "" {
			t.Errorf("%s target %q: accepted but nothing queued", c.mac, c.target)
		}
		if c.code == http.StatusConflict {
			if pending != "" {
				t.Errorf("%s target %q: refused but queued %q", c.mac, c.target, pending)
			}
			if !strings.Contains(rec.Body.String(), "target mismatch") {
				t.Errorf("%s target %q: body %q lacks the reason", c.mac, c.target, rec.Body.String())
			}
		}
	}
}
