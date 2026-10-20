// fakenode - a test stand-in for the ESP32 rproxy firmware. By default it
// speaks the multiplexed "mux1" link exactly like the firmware (one link, many
// streams, per-stream credit). LEGACY=1 switches to the old one-DIAL-per-
// connection pool protocol (HELLO/READY, DIAL/OK/ERR, relay) used by v1.2 and
// older firmware. Both apply the same private-IP / blocked-port safety filter.
package main

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

func blockedIP(ip net.IP) bool {
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
		return true // CGNAT
	}
	return false
}

func blockedPort(p int) bool { return p == 0 || p == 25 || p == 465 || p == 587 }

// allowLocal disables the private-IP filter for loopback reachability testing
// in the sandbox (set ALLOW_LOCAL=1). Never set in production.
var allowLocal = os.Getenv("ALLOW_LOCAL") == "1"

// dialChecked applies the safety filter and connects. reason is "" on success.
func dialChecked(host, portS string) (net.Conn, string) {
	port, _ := strconv.Atoi(portS)
	if blockedPort(port) {
		return nil, "blocked"
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, "dial"
	}
	if !allowLocal && blockedIP(ips[0]) {
		return nil, "blocked"
	}
	tc, err := net.DialTimeout("tcp", net.JoinHostPort(host, portS), 10*time.Second)
	if err != nil {
		return nil, "dial"
	}
	return tc, ""
}

// ---- legacy pool worker -------------------------------------------------

func worker(id int, broker, token string) {
	backoff := time.Second
	for {
		c, err := net.DialTimeout("tcp", broker, 10*time.Second)
		if err != nil {
			time.Sleep(backoff)
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		br := bufio.NewReader(c)
		fmt.Fprintf(c, "HELLO %s\n", token)
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
		tc, reason := dialChecked(f[1], f[2])
		if reason != "" {
			c.Write([]byte("ERR " + reason + "\n"))
			c.Close()
			continue
		}
		c.Write([]byte("OK\n"))
		log.Printf("w%d dial %s:%s", id, f[1], f[2])
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			io.Copy(tc, br)
			if t, ok := tc.(*net.TCPConn); ok {
				t.CloseWrite()
			}
		}()
		go func() {
			defer wg.Done()
			io.Copy(c, tc)
			if t, ok := c.(*net.TCPConn); ok {
				t.CloseWrite()
			}
		}()
		wg.Wait()
		tc.Close()
		c.Close()
	}
}

// ---- mux link -----------------------------------------------------------

const (
	fOpen, fOpenOK, fOpenErr, fData, fFin, fRst, fWindow, fPing, fPong, fOTA = 1, 2, 3, 4, 5, 6, 7, 8, 9, 10

	hdrLen  = 8
	window  = 32768
	grantAt = 8192
)

type stream struct {
	tc      net.Conn
	mu      sync.Mutex
	cond    *sync.Cond
	credit  int
	pending int
	dead    bool
}

type muxLink struct {
	c   net.Conn
	wmu sync.Mutex
	mu  sync.Mutex
	st  map[uint32]*stream
	max int
}

func (m *muxLink) send(typ byte, sid uint32, p []byte) {
	var h [hdrLen]byte
	h[0] = typ
	binary.BigEndian.PutUint16(h[2:4], uint16(len(p)))
	binary.BigEndian.PutUint32(h[4:8], sid)
	m.wmu.Lock()
	m.c.Write(append(h[:], p...))
	m.wmu.Unlock()
}

func (m *muxLink) get(sid uint32) *stream {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st[sid]
}

func (m *muxLink) drop(sid uint32) {
	m.mu.Lock()
	s := m.st[sid]
	delete(m.st, sid)
	m.mu.Unlock()
	if s != nil && s.tc != nil {
		s.tc.Close()
		s.mu.Lock()
		s.dead = true
		s.cond.Broadcast()
		s.mu.Unlock()
	}
}

func muxNode(broker, token string, max int) {
	backoff := time.Second
	for {
		c, err := net.DialTimeout("tcp", broker, 10*time.Second)
		if err != nil {
			time.Sleep(backoff)
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		br := bufio.NewReader(c)
		fmt.Fprintf(c, "HELLO %s fa:ke:00:00:00:01 fakenode 0 - mux1 %d\n", token, max)
		line, err := br.ReadString('\n')
		if err != nil || strings.TrimSpace(line) != "READY mux1" {
			log.Printf("mux: handshake failed: %q %v", strings.TrimSpace(line), err)
			c.Close()
			time.Sleep(backoff)
			continue
		}
		backoff = time.Second
		log.Printf("mux link up -> %s", broker)
		m := &muxLink{c: c, st: map[uint32]*stream{}, max: max}
		m.serve(br)
		c.Close()
		m.mu.Lock()
		for sid := range m.st {
			go m.drop(sid)
		}
		m.mu.Unlock()
		log.Printf("mux link down")
	}
}

func (m *muxLink) serve(br *bufio.Reader) {
	var h [hdrLen]byte
	for {
		m.c.SetReadDeadline(time.Now().Add(45 * time.Second))
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
		case fPing:
			m.send(fPong, 0, p)
		case fOTA:
			log.Printf("mux: OTA requested: %s (ignored by fakenode)", p)
		case fOpen:
			m.mu.Lock()
			full := len(m.st) >= m.max
			if !full {
				s := &stream{credit: window}
				s.cond = sync.NewCond(&s.mu)
				m.st[sid] = s
			}
			m.mu.Unlock()
			if full {
				m.send(fOpenErr, sid, []byte("busy"))
				continue
			}
			go m.dial(sid, string(p))
		case fData:
			if s := m.get(sid); s != nil && s.tc != nil {
				s.tc.Write(p)
				s.mu.Lock()
				s.pending += len(p)
				g := 0
				if s.pending >= grantAt {
					g, s.pending = s.pending, 0
				}
				s.mu.Unlock()
				if g > 0 {
					var b [4]byte
					binary.BigEndian.PutUint32(b[:], uint32(g))
					m.send(fWindow, sid, b[:])
				}
			}
		case fFin:
			if s := m.get(sid); s != nil && s.tc != nil {
				if t, ok := s.tc.(*net.TCPConn); ok {
					t.CloseWrite()
				}
			}
		case fRst:
			m.drop(sid)
		case fWindow:
			if s := m.get(sid); s != nil && len(p) == 4 {
				s.mu.Lock()
				s.credit += int(binary.BigEndian.Uint32(p))
				s.cond.Broadcast()
				s.mu.Unlock()
			}
		}
	}
}

func (m *muxLink) dial(sid uint32, req string) {
	f := strings.Fields(req)
	if len(f) != 2 {
		m.drop(sid)
		m.send(fOpenErr, sid, []byte("badreq"))
		return
	}
	tc, reason := dialChecked(f[0], f[1])
	if reason != "" {
		m.drop(sid)
		m.send(fOpenErr, sid, []byte(reason))
		return
	}
	s := m.get(sid)
	if s == nil { // reset while dialing
		tc.Close()
		return
	}
	s.tc = tc
	m.send(fOpenOK, sid, nil)
	log.Printf("mux dial %s:%s (stream %d)", f[0], f[1], sid)
	buf := make([]byte, 4096)
	for {
		s.mu.Lock()
		for s.credit == 0 && !s.dead {
			s.cond.Wait()
		}
		cr, dead := s.credit, s.dead
		s.mu.Unlock()
		if dead {
			return
		}
		n, err := tc.Read(buf[:min(cr, len(buf))])
		if n > 0 {
			s.mu.Lock()
			s.credit -= n
			s.mu.Unlock()
			m.send(fData, sid, buf[:n])
		}
		if err != nil {
			m.send(fFin, sid, nil)
			return
		}
	}
}

func main() {
	broker := os.Getenv("BROKER")
	if broker == "" {
		broker = "127.0.0.1:7000"
	}
	token := os.Getenv("TOKEN")
	if os.Getenv("LEGACY") == "1" {
		n := 4
		if v := os.Getenv("POOL"); v != "" {
			n, _ = strconv.Atoi(v)
		}
		for i := 0; i < n; i++ {
			go worker(i, broker, token)
		}
		log.Printf("fakenode (legacy): %d workers -> %s", n, broker)
		select {}
	}
	max := 24
	if v := os.Getenv("MAX_STREAMS"); v != "" {
		max, _ = strconv.Atoi(v)
	}
	log.Printf("fakenode (mux): one link, up to %d streams -> %s", max, broker)
	muxNode(broker, token, max)
}
