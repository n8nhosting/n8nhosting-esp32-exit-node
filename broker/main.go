// broker - reverse exit-node broker for the ESP32 proxy platform.
//
// It bridges two sides:
//
//   - POOL side (:7000, TLS): ESP32 exit nodes dial IN and park idle
//     connections. Each connection authenticates with
//     "HELLO <token> <mac> <fw>\n" and the broker replies "READY\n", then
//     holds the connection in an idle pool. Nodes are tracked by MAC address
//     and can be blocked/allowed from the admin dashboard.
//
//   - SOCKS side (:1080, internal): GOST chains outbound customer traffic
//     here as a SOCKS5 upstream. For each CONNECT the broker takes one idle
//     node connection, sends "DIAL <host> <port>\n", waits for "OK\n"/"ERR …\n",
//     and on OK relays bytes between GOST and the node (which has already
//     connected out through the home WiFi, i.e. the Thai exit IP).
//
// Per-device slots: gost's slot services log in as "slot-N" (SOCKS5
// user/pass, password SLOT_SECRET). Those streams go only to the board that
// holds slot N (POST /nodes/{mac}/slot); if it is offline the customer is
// refused, never moved to another board. No-auth clients use the shared pool.
//
// Legacy (v1.2 and older firmware): one node connection serves one DIAL; the
// node opens a fresh pool connection afterwards to refill the pool.
//
// Multiplexed (HELLO ends with "mux1"): the node keeps ONE long-lived
// connection and the broker runs many customer streams over it as binary
// frames (see mux.go). Both kinds of node can be connected at the same time.
//
// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type config struct {
	poolAddr   string
	socksAddr  string
	statusAddr string
	token      string
	dialWait   time.Duration
	tlsCert    string // PEM (env TLS_CERT_PEM) or empty for plain TCP (dev only)
	tlsKey     string
	nodesFile  string // persisted node policy (block list)
	slotSecret string // SOCKS user/pass password for gost's per-slot chains ("" = slot logins refused)
	// OTA_ALLOW_HTTP=1 lets /nodes/{mac}/ota queue a plain http:// image URL.
	// Firmware images are not signed, so the download must ride on TLS in
	// production; the flag exists for bench tests that serve an image from a PC.
	allowHTTPOTA bool
}

// pemFromEnv reads a PEM from a plain env var or a base64-encoded one
// (base64 avoids multi-line values in dashboards like Coolify).
func pemFromEnv(plain, b64 string) string {
	if v := os.Getenv(plain); v != "" {
		return v
	}
	if v := os.Getenv(b64); v != "" {
		if d, err := base64.StdEncoding.DecodeString(v); err == nil {
			return string(d)
		}
	}
	return ""
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// idleConn is a parked node connection waiting for work.
type idleConn struct {
	c    net.Conn
	br   *bufio.Reader
	node *node
}

type broker struct {
	cfg  config
	idle chan *idleConn
	reg  *registry

	muxMu sync.Mutex
	muxes []*muxSession
	muxRR int

	dialsOK     int64
	dialsErr    int64
	dialsNoNode int64
	bytesUp     int64
	bytesDown   int64
	started     time.Time

	streamsEvicted    int64 // mux streams reset to make room on a full link
	streamsIdleClosed int64 // mux streams closed by the idle / half-close reaper

	dialsPinnedOffline int64 // slot dials refused because the slot's board had no usable link
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := config{
		poolAddr:     envOr("POOL_ADDR", ":7000"),
		socksAddr:    envOr("SOCKS_ADDR", ":1080"),
		statusAddr:   envOr("STATUS_ADDR", ":7001"),
		token:        os.Getenv("POOL_TOKEN"),
		tlsCert:      pemFromEnv("TLS_CERT_PEM", "TLS_CERT_B64"),
		tlsKey:       pemFromEnv("TLS_KEY_PEM", "TLS_KEY_B64"),
		nodesFile:    envOr("NODES_FILE", "/data/nodes.json"),
		slotSecret:   os.Getenv("SLOT_SECRET"),
		allowHTTPOTA: os.Getenv("OTA_ALLOW_HTTP") == "1",
	}
	waitS, _ := strconv.Atoi(envOr("DIAL_WAIT_SECONDS", "10"))
	cfg.dialWait = time.Duration(waitS) * time.Second
	if cfg.token == "" {
		log.Fatal("POOL_TOKEN is required")
	}
	if cfg.slotSecret == "" {
		log.Printf("SLOT_SECRET unset: every slot-N login is refused; only the shared pool is served")
	}
	depth, _ := strconv.Atoi(envOr("POOL_BACKLOG", "64"))

	b := newBroker(cfg, depth)

	var poolLn net.Listener
	var err error
	if cfg.tlsCert != "" && cfg.tlsKey != "" {
		cert, err := tls.X509KeyPair([]byte(cfg.tlsCert), []byte(cfg.tlsKey))
		if err != nil {
			log.Fatalf("TLS_CERT_PEM/TLS_KEY_PEM: %v", err)
		}
		tcfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		poolLn, err = tls.Listen("tcp", cfg.poolAddr, tcfg)
		if err != nil {
			log.Fatalf("pool tls listen %s: %v", cfg.poolAddr, err)
		}
		log.Printf("pool listening on %s (TLS)", cfg.poolAddr)
	} else {
		poolLn, err = net.Listen("tcp", cfg.poolAddr)
		if err != nil {
			log.Fatalf("pool listen %s: %v", cfg.poolAddr, err)
		}
		log.Printf("pool listening on %s (PLAIN TCP - set TLS_CERT_PEM/TLS_KEY_PEM for production)", cfg.poolAddr)
	}
	socksLn, err := net.Listen("tcp", cfg.socksAddr)
	if err != nil {
		log.Fatalf("socks listen %s: %v", cfg.socksAddr, err)
	}
	log.Printf("socks5 listening on %s", cfg.socksAddr)

	go b.servePool(poolLn)
	go b.serveStatus()
	b.serveSocks(socksLn)
}

func newBroker(cfg config, depth int) *broker {
	return &broker{cfg: cfg, idle: make(chan *idleConn, depth), reg: newRegistry(cfg.nodesFile), started: time.Now()}
}

// ---- POOL side ----------------------------------------------------------

func (b *broker) servePool(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if isClosedErr(err) {
				return
			}
			log.Printf("pool accept: %v", err)
			continue
		}
		go b.handlePoolConn(c)
	}
}

func isClosedErr(err error) bool { return errors.Is(err, net.ErrClosed) }

// sanitizeSSID strips control chars and caps length for safe display/storage.
func sanitizeSSID(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			out = append(out, r)
		}
		if len(out) >= 32 {
			break
		}
	}
	return string(out)
}

func (b *broker) handlePoolConn(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		c.Close()
		return
	}
	f := strings.Fields(strings.TrimSpace(line))
	// HELLO <token> [mac] [fw]
	if len(f) < 2 || f[0] != "HELLO" || f[1] != b.cfg.token {
		c.Close()
		return
	}
	// HELLO <token> <mac> <fw> <uptime_s> <ssid_b64>
	// "mux1" as the 7th field selects the multiplexed link; an optional 8th
	// field is the node's stream limit.
	useMux := len(f) >= 7 && f[6] == "mux1"
	opts := helloOpts{Window: helloDefaultWindow, Frame: helloDefaultFrame}
	if useMux {
		opts = parseHelloOpts(f) // max_streams and the optional win= frame= chip= tokens
	}
	mac, fw := "unknown", ""
	var uptime uint64
	ssid := ""
	if len(f) >= 3 {
		mac = strings.ToLower(f[2])
	}
	if len(f) >= 4 {
		fw = f[3]
	}
	if len(f) >= 5 {
		uptime, _ = strconv.ParseUint(f[4], 10, 64)
	}
	if len(f) >= 6 {
		if dec, err := base64.StdEncoding.DecodeString(f[5]); err == nil {
			ssid = sanitizeSSID(string(dec))
		}
	}
	remoteIP, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	n := b.reg.touch(mac, fw, remoteIP, uptime, ssid, opts.Chip)
	if n.isBlocked() {
		c.Write([]byte("DENY blocked\n"))
		c.Close()
		return
	}
	if useMux {
		b.serveMux(c, br, n, opts)
		return
	}
	if _, err := c.Write([]byte("READY\n")); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})

	if url := b.reg.takeOTA(n); url != "" {
		log.Printf("ota: pushing %s to %s", url, mac)
		c.SetDeadline(time.Now().Add(10 * time.Second))
		c.Write([]byte("OTA " + url + "\n"))
		br.ReadString('\n') // OK
		c.Close()
		return
	}
	n.addIdle(1)

	ic := &idleConn{c: c, br: br, node: n}
	select {
	case b.idle <- ic:
	default:
		n.addIdle(-1)
		c.Close()
	}
}

// serveMux runs a multiplexed node link until it drops.
func (b *broker) serveMux(c net.Conn, br *bufio.Reader, n *node, o helloOpts) {
	if _, err := c.Write([]byte("READY mux1\n")); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{})
	s := newMuxSession(c, br, n, o)
	b.reg.muxUp(n, s.maxStreams)
	s.evicted, s.idleClosed = &b.streamsEvicted, &b.streamsIdleClosed
	s.pollOTA = func() string { return b.reg.takeOTA(n) }
	s.onStats = func(st nodeStats) { b.reg.stats(n, st) }
	if url := b.reg.takeOTA(n); url != "" {
		log.Printf("ota: pushing %s to %s (mux)", url, n.MAC)
		s.writeFrame(frameOTA, 0, []byte(url))
	}
	b.muxMu.Lock()
	b.muxes = append(b.muxes, s)
	b.muxMu.Unlock()
	log.Printf("mux link up: %s (max %d streams, win %d, frame %d, chip %q)", n.MAC, s.maxStreams, s.window, s.frame, n.Chip)

	s.run()

	n.addMux(-1)
	b.reg.linkDown(n)
	b.muxMu.Lock()
	for i, m := range b.muxes {
		if m == s {
			b.muxes = append(b.muxes[:i], b.muxes[i+1:]...)
			break
		}
	}
	b.muxMu.Unlock()
	log.Printf("mux link down: %s", n.MAC)
}

// usableFor reports whether m may carry a stream for pin: only pin's own links
// when pin is non-nil, else any link of a node that serves the shared pool.
func usableFor(m *muxSession, pin *node) bool {
	if pin != nil {
		return m.node == pin
	}
	return m.node.inPool()
}

// pickMux returns the least-loaded usable mux link (round-robin on ties),
// skipping any in `tried`. A non-nil pin limits the choice to that node's links;
// a nil pin (pool customer) skips nodes taken out of the pool (dedicated).
func (b *broker) pickMux(tried map[*muxSession]bool, pin *node) *muxSession {
	b.muxMu.Lock()
	defer b.muxMu.Unlock()
	var best *muxSession
	bestLoad := 0
	cnt := len(b.muxes)
	for i := 0; i < cnt; i++ {
		m := b.muxes[(b.muxRR+i)%cnt]
		if tried[m] || !usableFor(m, pin) || m.node.isBlocked() || m.isClosed() {
			continue
		}
		load := m.active()
		if load >= m.maxStreams {
			continue
		}
		if best == nil || load < bestLoad {
			best, bestLoad = m, load
		}
	}
	if cnt > 0 {
		b.muxRR = (b.muxRR + 1) % cnt
	}
	return best
}

func (b *broker) muxCount() int {
	b.muxMu.Lock()
	defer b.muxMu.Unlock()
	return len(b.muxes)
}

// dialViaMux opens a stream on a mux link. ok=false means no mux link could
// take it (the caller falls back to the legacy pool); otherwise rep is the
// SOCKS reply code and st is non-nil on success.
//
// A non-nil pin (slot customer) only ever uses that node's links and never
// reports ok=false: with no usable link it answers repHostFail itself, so the
// customer is not handed to the pool or another board.
func (b *broker) dialViaMux(host string, port int, tried map[*muxSession]bool, pin *node) (st *muxStream, rep byte, ok bool) {
	for {
		m := b.pickMux(tried, pin)
		if m == nil {
			if b.evictIdleStream(tried, pin) {
				continue // a slot was just freed
			}
			if pin != nil {
				atomic.AddInt64(&b.dialsPinnedOffline, 1)
				atomic.AddInt64(&pin.DialsPinnedOffline, 1)
				log.Printf("socks: slot device %s has no usable link for %s:%d", pin.MAC, host, port)
				return nil, repHostFail, true
			}
			return nil, 0, false
		}
		tried[m] = true
		st, reason := m.open(host, port)
		switch {
		case st != nil:
			return st, repSuccess, true
		case reason == "busy" || reason == "link closed" || reason == "timeout":
			continue // try another link
		case reason == "blocked":
			atomic.AddInt64(&b.dialsErr, 1)
			m.node.addStats(0, 1, 0, 0)
			return nil, repRefused, true
		default:
			atomic.AddInt64(&b.dialsErr, 1)
			m.node.addStats(0, 1, 0, 0)
			return nil, repHostFail, true
		}
	}
}

// evictIdleStream makes room on a full link by resetting the stream that has
// been idle longest (at least muxEvictMinIdle), across links not in `tried`
// (only the pinned node's links when pin is non-nil, else only pool nodes'
// links, so a dedicated board's streams never make room for pool traffic).
// Browsers reopen a parked keep-alive connection transparently, so this beats
// refusing the new customer. Returns false if nothing qualifies.
func (b *broker) evictIdleStream(tried map[*muxSession]bool, pin *node) bool {
	b.muxMu.Lock()
	muxes := append([]*muxSession(nil), b.muxes...)
	b.muxMu.Unlock()
	since := time.Now().Add(-muxEvictMinIdle.Get())
	var victim *muxStream
	var on *muxSession
	for _, m := range muxes {
		if tried[m] || !usableFor(m, pin) || m.isClosed() || m.node.isBlocked() {
			continue
		}
		if st := m.oldestIdle(since); st != nil && (victim == nil || st.lastActive.Load() < victim.lastActive.Load()) {
			victim, on = st, m
		}
	}
	if victim == nil {
		return false
	}
	on.abort(victim)
	atomic.AddInt64(&b.streamsEvicted, 1)
	log.Printf("mux %s: link full, evicted idle stream %d", on.node.MAC, victim.id)
	return true
}

func (b *broker) takeIdle(deadline time.Time) *idleConn {
	for {
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil
		}
		select {
		case ic := <-b.idle:
			ic.node.addIdle(-1)
			if isDead(ic.c, ic.br) {
				ic.c.Close()
				continue
			}
			if ic.node.isBlocked() || !ic.node.inPool() {
				ic.c.Close() // a dedicated board never carries pool traffic
				continue
			}
			return ic
		case <-time.After(wait):
			return nil
		}
	}
}

func isDead(c net.Conn, br *bufio.Reader) bool {
	c.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	defer c.SetReadDeadline(time.Time{})
	_, err := br.Peek(1)
	if err == nil {
		return true
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	return false
}

// ---- SOCKS5 side --------------------------------------------------------

func (b *broker) serveSocks(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if isClosedErr(err) {
				return
			}
			log.Printf("socks accept: %v", err)
			continue
		}
		go b.handleSocks(c)
	}
}

const (
	socksVer5   = 0x05
	cmdConnect  = 0x01
	atypIPv4    = 0x01
	atypDomain  = 0x03
	atypIPv6    = 0x04
	repSuccess  = 0x00
	repGenFail  = 0x01
	repHostFail = 0x04
	repRefused  = 0x05

	methodNoAuth   = 0x00
	methodUserPass = 0x02
	userPassVer    = 0x01 // RFC 1929 sub-negotiation version
	userPassOK     = 0x00
	userPassFail   = 0x01
)

// socksGreeting reads the client's method list and authenticates it. It
// returns the username ("" = shared pool) and false if the client must be
// dropped.
//
// User/pass (RFC 1929) is selected whenever the client offers it: gost's
// per-slot chains log in as "slot-N" with SLOT_SECRET. gost's socks5 connector
// offers user/pass even when it has no credentials (the pool chain), so an
// empty username and password is the pool, like no-auth. Without a secret
// every slot-N login is refused (fail closed); a client offering only no-auth
// gets no-auth and the pool.
func (b *broker) socksGreeting(c net.Conn, br *bufio.Reader) (user string, ok bool) {
	ver, _ := br.ReadByte()
	if ver != socksVer5 {
		return "", false
	}
	nm, err := br.ReadByte()
	if err != nil {
		return "", false
	}
	methods := make([]byte, nm)
	if _, err := io.ReadFull(br, methods); err != nil {
		return "", false
	}
	if bytes.IndexByte(methods, methodUserPass) < 0 {
		c.Write([]byte{socksVer5, methodNoAuth})
		return "", true
	}
	c.Write([]byte{socksVer5, methodUserPass})
	user, pass, err := readUserPass(br)
	if err != nil {
		c.Write([]byte{userPassVer, userPassFail})
		return "", false
	}
	_, isSlot := slotUser(user)
	anon := user == "" && pass == ""
	slotOK := isSlot && b.cfg.slotSecret != "" && subtle.ConstantTimeCompare([]byte(pass), []byte(b.cfg.slotSecret)) == 1
	if !anon && !slotOK {
		log.Printf("socks: login refused for user %q", user)
		c.Write([]byte{userPassVer, userPassFail})
		return "", false
	}
	c.Write([]byte{userPassVer, userPassOK})
	return user, true
}

// readUserPass reads an RFC 1929 request: VER ULEN UNAME PLEN PASSWD.
func readUserPass(br *bufio.Reader) (user, pass string, err error) {
	var hdr [2]byte
	if _, err = io.ReadFull(br, hdr[:]); err != nil {
		return "", "", err
	}
	if hdr[0] != userPassVer {
		return "", "", errors.New("bad user/pass version")
	}
	u := make([]byte, hdr[1])
	if _, err = io.ReadFull(br, u); err != nil {
		return "", "", err
	}
	pl, err := br.ReadByte()
	if err != nil {
		return "", "", err
	}
	p := make([]byte, pl)
	if _, err = io.ReadFull(br, p); err != nil {
		return "", "", err
	}
	return string(u), string(p), nil
}

// slotUser parses a "slot-N" SOCKS username (N >= 1, plain decimal).
func slotUser(u string) (int, bool) {
	s, ok := strings.CutPrefix(u, "slot-")
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || strconv.Itoa(n) != s {
		return 0, false
	}
	return n, true
}

func (b *broker) handleSocks(c net.Conn) {
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)

	user, ok := b.socksGreeting(c, br)
	if !ok {
		return
	}

	hdr := make([]byte, 4)
	if _, err := io.ReadFull(br, hdr); err != nil {
		return
	}
	if hdr[0] != socksVer5 || hdr[1] != cmdConnect {
		socksReply(c, repGenFail)
		return
	}
	host, err := readSocksAddr(br, hdr[3])
	if err != nil {
		socksReply(c, repGenFail)
		return
	}
	pb := make([]byte, 2)
	if _, err := io.ReadFull(br, pb); err != nil {
		return
	}
	port := int(pb[0])<<8 | int(pb[1])
	c.SetReadDeadline(time.Time{})

	if slot, pinned := slotUser(user); pinned {
		b.dialSlot(c, slot, host, port)
		return
	}

	deadline := time.Now().Add(b.cfg.dialWait)
	tried := map[*muxSession]bool{}
	var ic *idleConn
	for {
		if st, rep, ok := b.dialViaMux(host, port, tried, nil); ok {
			b.finishMux(c, st, rep)
			return
		}
		// No mux link can take it: wait briefly for a legacy idle connection,
		// then look again so a mux link that comes up meanwhile is used too.
		step := time.Now().Add(250 * time.Millisecond)
		if step.After(deadline) {
			step = deadline
		}
		if ic = b.takeIdle(step); ic != nil || !time.Now().Before(deadline) {
			break
		}
	}
	if ic == nil {
		atomic.AddInt64(&b.dialsNoNode, 1)
		log.Printf("socks: no idle node for %s:%d", host, port)
		socksReply(c, repRefused)
		return
	}

	ic.c.SetDeadline(time.Now().Add(20 * time.Second))
	if _, err := ic.c.Write([]byte("DIAL " + host + " " + strconv.Itoa(port) + "\n")); err != nil {
		ic.c.Close()
		socksReply(c, repGenFail)
		return
	}
	resp, err := ic.br.ReadString('\n')
	if err != nil {
		ic.c.Close()
		socksReply(c, repGenFail)
		return
	}
	resp = strings.TrimSpace(resp)
	if resp != "OK" {
		atomic.AddInt64(&b.dialsErr, 1)
		ic.node.addStats(0, 1, 0, 0)
		ic.c.Close()
		if strings.HasPrefix(resp, "ERR blocked") {
			socksReply(c, repRefused)
		} else {
			socksReply(c, repHostFail)
		}
		return
	}
	ic.c.SetDeadline(time.Time{})
	atomic.AddInt64(&b.dialsOK, 1)
	ic.node.addStats(1, 0, 0, 0)
	ic.node.addActive(1)
	socksReply(c, repSuccess)

	up, down := b.pipe(c, ic.c, ic.br)
	ic.node.addActive(-1)
	ic.node.addStats(0, 0, up, down)
}

// dialSlot serves a "slot-N" customer on the board holding slot N only. A slot
// nobody holds, or whose board has no usable mux link, is refused with "host
// unreachable" — never rerouted to the pool or another board.
func (b *broker) dialSlot(c net.Conn, slot int, host string, port int) {
	pin := b.reg.bySlot(slot)
	if pin == nil {
		atomic.AddInt64(&b.dialsPinnedOffline, 1)
		log.Printf("socks: slot %d has no device for %s:%d", slot, host, port)
		socksReply(c, repHostFail)
		return
	}
	st, rep, _ := b.dialViaMux(host, port, map[*muxSession]bool{}, pin)
	b.finishMux(c, st, rep)
}

// finishMux answers the customer and relays a successful stream; a failed dial
// (st == nil) was already counted by dialViaMux.
func (b *broker) finishMux(c net.Conn, st *muxStream, rep byte) {
	if st == nil {
		socksReply(c, rep)
		return
	}
	n := st.s.node
	atomic.AddInt64(&b.dialsOK, 1)
	n.addStats(1, 0, 0, 0)
	n.addActive(1)
	socksReply(c, repSuccess)
	up, down := st.relay(c)
	n.addActive(-1)
	n.addStats(0, 0, up, down)
	atomic.AddInt64(&b.bytesUp, up)
	atomic.AddInt64(&b.bytesDown, down)
}

func readSocksAddr(br *bufio.Reader, atyp byte) (string, error) {
	switch atyp {
	case atypIPv4:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case atypIPv6:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return net.IP(buf).String(), nil
	case atypDomain:
		l, err := br.ReadByte()
		if err != nil {
			return "", err
		}
		buf := make([]byte, int(l))
		if _, err := io.ReadFull(br, buf); err != nil {
			return "", err
		}
		return string(buf), nil
	}
	return "", errors.New("bad atyp")
}

func socksReply(c net.Conn, rep byte) {
	c.Write([]byte{socksVer5, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
}

// closeWrite half-closes if the connection supports it (TCP or TLS over TCP).
func closeWrite(c net.Conn) {
	switch t := c.(type) {
	case *net.TCPConn:
		t.CloseWrite()
	case *tls.Conn:
		t.CloseWrite()
	}
}

func (b *broker) pipe(gost net.Conn, esp net.Conn, espRd *bufio.Reader) (up, down int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if n := espRd.Buffered(); n > 0 {
			buf := make([]byte, n)
			espRd.Read(buf)
			gost.Write(buf)
			atomic.AddInt64(&down, int64(n))
		}
		n, _ := io.Copy(gost, esp)
		atomic.AddInt64(&down, n)
		closeWrite(gost)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(esp, gost)
		atomic.AddInt64(&up, n)
		closeWrite(esp)
	}()
	wg.Wait()
	esp.Close()
	gost.Close()
	atomic.AddInt64(&b.bytesUp, up)
	atomic.AddInt64(&b.bytesDown, down)
	return up, down
}

// ---- status / nodes API (internal network only) -------------------------

func (b *broker) serveStatus() {
	log.Printf("status listening on %s", b.cfg.statusAddr)
	http.ListenAndServe(b.cfg.statusAddr, b.statusMux())
}

func (b *broker) statusMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"idle_nodes":           len(b.idle),
			"mux_links":            b.muxCount(),
			"nodes_online":         b.reg.onlineCount(),
			"nodes_total":          b.reg.count(),
			"slots_assigned":       b.reg.slotCount(),
			"dials_ok":             atomic.LoadInt64(&b.dialsOK),
			"dials_err":            atomic.LoadInt64(&b.dialsErr),
			"dials_no_node":        atomic.LoadInt64(&b.dialsNoNode),
			"dials_pinned_offline": atomic.LoadInt64(&b.dialsPinnedOffline),
			"streams_evicted":      atomic.LoadInt64(&b.streamsEvicted),
			"streams_idle_closed":  atomic.LoadInt64(&b.streamsIdleClosed),
			"bytes_up":             atomic.LoadInt64(&b.bytesUp),
			"bytes_down":           atomic.LoadInt64(&b.bytesDown),
			"uptime_sec":           int64(time.Since(b.started).Seconds()),
			"tls":                  b.cfg.tlsCert != "",
		})
	})
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.reg.list())
	})
	// POST /nodes/{mac}/block | /allow | /forget | /ota {url,version} | /ota-cancel
	//      /slot {"slot": N, "pool": bool} (slot 0 clears; pool absent = unchanged)
	mux.HandleFunc("/nodes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		rest := strings.TrimPrefix(r.URL.Path, "/nodes/")
		parts := strings.Split(rest, "/")
		if len(parts) != 2 {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		mac, action := strings.ToLower(parts[0]), parts[1]
		var ok bool
		switch action {
		case "ota":
			var body struct {
				URL     string `json:"url"`
				Version string `json:"version"` // expected firmware afterwards (optional)
				Target  string `json:"target"`  // chip the image was built for (optional; esp32s3 / esp32c3)
			}
			json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasPrefix(body.URL, "https://") &&
				!(b.cfg.allowHTTPOTA && strings.HasPrefix(body.URL, "http://")) {
				http.Error(w, "https url required (images are unsigned; set OTA_ALLOW_HTTP=1 only on a test broker)", http.StatusBadRequest)
				return
			}
			if len(body.URL) >= 256 || len(body.Version) > 32 || len(body.Target) > 16 {
				http.Error(w, "url, version or target too long", http.StatusBadRequest)
				return
			}
			// An image for the wrong chip would be rejected by the bootloader
			// anyway, but refusing it here keeps the dashboard honest and the
			// board out of a pointless download + reboot.
			if body.Target != "" {
				chip, known := b.reg.chipOf(mac)
				if !known {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				if chip != body.Target {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					json.NewEncoder(w).Encode(map[string]string{"error": "target mismatch", "chip": chip})
					return
				}
			}
			ok = b.reg.setOTA(mac, body.URL, body.Version)
		case "ota-cancel":
			ok = b.reg.setOTA(mac, "", "")
		case "block":
			ok = b.reg.setBlocked(mac, true)
		case "allow":
			ok = b.reg.setBlocked(mac, false)
		case "forget":
			ok = b.reg.forget(mac)
		case "slot":
			var body struct {
				Slot *int  `json:"slot"`
				Pool *bool `json:"pool"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || body.Slot == nil || *body.Slot < 0 {
				http.Error(w, "slot must be a number >= 0 (0 clears)", http.StatusBadRequest)
				return
			}
			err := b.reg.setSlotPool(mac, *body.Slot, body.Pool)
			if errors.Is(err, errSlotTaken) {
				http.Error(w, "slot taken", http.StatusConflict)
				return
			}
			ok = err == nil
		default:
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
