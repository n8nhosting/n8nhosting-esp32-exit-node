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
// One node connection serves one DIAL; the node opens a fresh pool connection
// afterwards to refill the pool.
//
// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
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

	dialsOK     int64
	dialsErr    int64
	dialsNoNode int64
	bytesUp     int64
	bytesDown   int64
	started     time.Time
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := config{
		poolAddr:   envOr("POOL_ADDR", ":7000"),
		socksAddr:  envOr("SOCKS_ADDR", ":1080"),
		statusAddr: envOr("STATUS_ADDR", ":7001"),
		token:      os.Getenv("POOL_TOKEN"),
		tlsCert:    pemFromEnv("TLS_CERT_PEM", "TLS_CERT_B64"),
		tlsKey:     pemFromEnv("TLS_KEY_PEM", "TLS_KEY_B64"),
		nodesFile:  envOr("NODES_FILE", "/data/nodes.json"),
	}
	waitS, _ := strconv.Atoi(envOr("DIAL_WAIT_SECONDS", "10"))
	cfg.dialWait = time.Duration(waitS) * time.Second
	if cfg.token == "" {
		log.Fatal("POOL_TOKEN is required")
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
	mac, fw := "unknown", ""
	if len(f) >= 3 {
		mac = strings.ToLower(f[2])
	}
	if len(f) >= 4 {
		fw = f[3]
	}
	remoteIP, _, _ := net.SplitHostPort(c.RemoteAddr().String())
	n := b.reg.touch(mac, fw, remoteIP)
	if n.Blocked {
		c.Write([]byte("DENY blocked\n"))
		c.Close()
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
			if ic.node.Blocked {
				ic.c.Close()
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
)

func (b *broker) handleSocks(c net.Conn) {
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)

	ver, _ := br.ReadByte()
	if ver != socksVer5 {
		return
	}
	nm, _ := br.ReadByte()
	if _, err := io.CopyN(io.Discard, br, int64(nm)); err != nil {
		return
	}
	c.Write([]byte{socksVer5, 0x00})

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

	ic := b.takeIdle(time.Now().Add(b.cfg.dialWait))
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
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"idle_nodes":    len(b.idle),
			"nodes_online":  b.reg.onlineCount(),
			"nodes_total":   b.reg.count(),
			"dials_ok":      atomic.LoadInt64(&b.dialsOK),
			"dials_err":     atomic.LoadInt64(&b.dialsErr),
			"dials_no_node": atomic.LoadInt64(&b.dialsNoNode),
			"bytes_up":      atomic.LoadInt64(&b.bytesUp),
			"bytes_down":    atomic.LoadInt64(&b.bytesDown),
			"uptime_sec":    int64(time.Since(b.started).Seconds()),
			"tls":           b.cfg.tlsCert != "",
		})
	})
	mux.HandleFunc("/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, b.reg.list())
	})
	// POST /nodes/{mac}/block | /allow | /forget
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
				URL string `json:"url"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			if !strings.HasPrefix(body.URL, "http://") && !strings.HasPrefix(body.URL, "https://") {
				http.Error(w, "url required", http.StatusBadRequest)
				return
			}
			ok = b.reg.setOTA(mac, body.URL)
		case "block":
			ok = b.reg.setBlocked(mac, true)
		case "allow":
			ok = b.reg.setBlocked(mac, false)
		case "forget":
			ok = b.reg.forget(mac)
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
	log.Printf("status listening on %s", b.cfg.statusAddr)
	http.ListenAndServe(b.cfg.statusAddr, mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
