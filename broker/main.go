// broker - reverse exit-node broker for the ESP32 proxy platform.
//
// It bridges two sides:
//
//   - POOL side (:7000): ESP32 exit nodes dial IN and park idle connections.
//     Each connection authenticates with "HELLO <token>\n" and the broker
//     replies "READY\n", then holds the connection in an idle pool.
//
//   - SOCKS side (:1080): GOST chains outbound customer traffic here as a
//     SOCKS5 upstream. For each CONNECT, the broker takes one idle ESP32
//     connection, sends "DIAL <host> <port>\n", waits for "OK\n" / "ERR …\n",
//     and on OK relays bytes between GOST and the ESP32 (which has already
//     connected out through the home WiFi, i.e. the Thai exit IP).
//
// One ESP32 connection serves one DIAL; the node opens a fresh pool
// connection afterwards to refill the pool. Concurrency therefore equals the
// node's configured pool size.
//
// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
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
	dialWait   time.Duration // how long a SOCKS request waits for an idle node
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// idleConn is a parked ESP32 pool connection waiting for work.
type idleConn struct {
	c  net.Conn
	br *bufio.Reader
}

type broker struct {
	cfg  config
	idle chan *idleConn

	// stats
	nodesConnected int64
	dialsOK        int64
	dialsErr       int64
	dialsNoNode    int64
	bytesUp        int64
	bytesDown      int64
}

func main() {
	log.SetFlags(log.LstdFlags | log.LUTC)
	cfg := config{
		poolAddr:   envOr("POOL_ADDR", ":7000"),
		socksAddr:  envOr("SOCKS_ADDR", ":1080"),
		statusAddr: envOr("STATUS_ADDR", ":7001"),
		token:      os.Getenv("POOL_TOKEN"),
	}
	waitS, _ := strconv.Atoi(envOr("DIAL_WAIT_SECONDS", "10"))
	cfg.dialWait = time.Duration(waitS) * time.Second
	if cfg.token == "" {
		log.Fatal("POOL_TOKEN is required")
	}
	depth, _ := strconv.Atoi(envOr("POOL_BACKLOG", "64"))

	b := &broker{cfg: cfg, idle: make(chan *idleConn, depth)}

	poolLn, err := net.Listen("tcp", cfg.poolAddr)
	if err != nil {
		log.Fatalf("pool listen %s: %v", cfg.poolAddr, err)
	}
	socksLn, err := net.Listen("tcp", cfg.socksAddr)
	if err != nil {
		log.Fatalf("socks listen %s: %v", cfg.socksAddr, err)
	}
	log.Printf("pool listening on %s", cfg.poolAddr)
	log.Printf("socks5 listening on %s", cfg.socksAddr)

	go b.servePool(poolLn)
	go b.serveStatus()
	b.serveSocks(socksLn) // blocks
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

func isClosedErr(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

func (b *broker) handlePoolConn(c net.Conn) {
	c.SetReadDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		c.Close()
		return
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 || fields[0] != "HELLO" || fields[1] != b.cfg.token {
		c.Close()
		return
	}
	if _, err := c.Write([]byte("READY\n")); err != nil {
		c.Close()
		return
	}
	c.SetReadDeadline(time.Time{}) // idle: no deadline until used

	ic := &idleConn{c: c, br: br}
	atomic.AddInt64(&b.nodesConnected, 1)

	// Park it. If the pool buffer is full, drop the oldest-style: just close.
	select {
	case b.idle <- ic:
	default:
		atomic.AddInt64(&b.nodesConnected, -1)
		c.Close()
	}
}

// takeIdle returns a usable idle node, discarding any that have died while
// parked, until the deadline.
func (b *broker) takeIdle(deadline time.Time) *idleConn {
	for {
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil
		}
		select {
		case ic := <-b.idle:
			atomic.AddInt64(&b.nodesConnected, -1)
			if isDead(ic.c, ic.br) {
				ic.c.Close()
				continue
			}
			return ic
		case <-time.After(wait):
			return nil
		}
	}
}

// isDead reports whether a parked connection has already been closed by the
// peer (EOF readable with zero bytes). The node never sends unsolicited data
// while idle, so any readable byte also means it is unusable.
func isDead(c net.Conn, br *bufio.Reader) bool {
	c.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	defer c.SetReadDeadline(time.Time{})
	_, err := br.Peek(1)
	if err == nil {
		return true // unexpected data while idle
	}
	if errors.Is(err, io.EOF) {
		return true
	}
	// timeout => still alive and quiet
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

	// greeting: VER NMETHODS METHODS...
	ver, _ := br.ReadByte()
	if ver != socksVer5 {
		return
	}
	nm, _ := br.ReadByte()
	if _, err := io.CopyN(io.Discard, br, int64(nm)); err != nil {
		return
	}
	// reply: no auth
	c.Write([]byte{socksVer5, 0x00})

	// request: VER CMD RSV ATYP ADDR PORT
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

	// grab an idle ESP32 node
	ic := b.takeIdle(time.Now().Add(b.cfg.dialWait))
	if ic == nil {
		atomic.AddInt64(&b.dialsNoNode, 1)
		log.Printf("socks: no idle node for %s:%d", host, port)
		socksReply(c, repRefused)
		return
	}

	// send DIAL and read result
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
	socksReply(c, repSuccess)

	// relay GOST (c) <-> ESP32 (ic.c). ic.br may hold buffered bytes; flush none
	// expected here, but drain it to be safe.
	b.pipe(c, ic.c, ic.br)
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
	// VER REP RSV ATYP(ipv4) BND.ADDR(0) BND.PORT(0)
	c.Write([]byte{socksVer5, rep, 0x00, atypIPv4, 0, 0, 0, 0, 0, 0})
}

// pipe relays between the GOST connection and the ESP32 connection, counting
// traffic. espBuffered carries any bytes already read into the node reader.
func (b *broker) pipe(gost net.Conn, esp net.Conn, espRd *bufio.Reader) {
	var wg sync.WaitGroup
	wg.Add(2)
	// esp -> gost (down)
	go func() {
		defer wg.Done()
		if n := espRd.Buffered(); n > 0 {
			buf := make([]byte, n)
			espRd.Read(buf)
			gost.Write(buf)
			atomic.AddInt64(&b.bytesDown, int64(n))
		}
		n, _ := io.Copy(gost, esp)
		atomic.AddInt64(&b.bytesDown, n)
		if t, ok := gost.(*net.TCPConn); ok {
			t.CloseWrite()
		}
	}()
	// gost -> esp (up)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(esp, gost)
		atomic.AddInt64(&b.bytesUp, n)
		if t, ok := esp.(*net.TCPConn); ok {
			t.CloseWrite()
		}
	}()
	wg.Wait()
	esp.Close()
	gost.Close()
}

// ---- status ------------------------------------------------------------

func (b *broker) serveStatus() {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"idle_nodes":    len(b.idle),
			"dials_ok":      atomic.LoadInt64(&b.dialsOK),
			"dials_err":     atomic.LoadInt64(&b.dialsErr),
			"dials_no_node": atomic.LoadInt64(&b.dialsNoNode),
			"bytes_up":      atomic.LoadInt64(&b.bytesUp),
			"bytes_down":    atomic.LoadInt64(&b.bytesDown),
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	log.Printf("status listening on %s", b.cfg.statusAddr)
	http.ListenAndServe(b.cfg.statusAddr, mux)
}
