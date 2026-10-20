// Multiplexed pool link ("mux1").
//
// A node that ends its HELLO with "mux1" gets "READY mux1\n" back and the
// connection then carries binary frames instead of one DIAL per connection.
// One long-lived TLS connection serves many customer streams, so the node
// does no TLS handshake per customer connection and is not limited by the
// size of a pre-opened pool.
//
// Frame: 8-byte header + payload
//
//	type  u8      frame type (frame* constants)
//	flags u8      reserved, 0
//	len   u16 BE  payload length (<= muxMaxPayload)
//	sid   u32 BE  stream id (0 = link control)
//
// Stream lifecycle (broker opens, node dials):
//
//	OPEN "host port"  ->  OPEN_OK | OPEN_ERR "reason"
//	DATA both ways, each side may have at most muxWindow unacknowledged bytes
//	in flight per stream; the receiver returns credit with WINDOW (u32 BE)
//	after it has written the bytes out.
//	FIN half-closes one direction; RST aborts or forgets the stream.
//
// Link control on sid 0: PING/PONG keepalive, OTA "<url>".
//
// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	frameOpen    = 0x01
	frameOpenOK  = 0x02
	frameOpenErr = 0x03
	frameData    = 0x04
	frameFin     = 0x05
	frameRst     = 0x06
	frameWindow  = 0x07
	framePing    = 0x08
	framePong    = 0x09
	frameOTA     = 0x0A

	muxHeaderLen  = 8
	muxMaxPayload = 16384
	muxWindow     = 32768 // initial per-stream credit, each direction
	muxGrantAt    = 8192  // return credit once this much has been consumed

	muxPingEvery = 15 * time.Second
	muxDeadAfter = 45 * time.Second
	muxOpenWait  = 20 * time.Second
)

// Capacity management. Variables (not consts) so tests can shorten them.
var (
	muxIdleTimeout    = 90 * time.Second // no data either way -> close the stream
	muxHalfCloseDrain = 30 * time.Second // after one side's FIN, the other side gets this long
	muxEvictMinIdle   = 5 * time.Second  // a stream must be at least this idle to be evicted
	muxReapEvery      = 5 * time.Second  // how often idle/half-closed streams are checked
)

var errStreamReset = errors.New("stream reset")

type muxSession struct {
	conn       net.Conn
	br         *bufio.Reader
	node       *node
	maxStreams int

	wmu sync.Mutex // serialises frame writes

	mu      sync.Mutex
	streams map[uint32]*muxStream
	nextID  uint32
	closed  bool
	done    chan struct{}

	lastRx atomic.Int64 // unix nanos of the last frame received

	evicted, idleClosed *int64 // broker-wide counters (nil = don't count)

	pollOTA func() string // returns a queued OTA URL to push on the live link (nil = none)
}

type muxStream struct {
	id uint32
	s  *muxSession

	openRes chan string // "" = OK, else the node's error reason

	opened     atomic.Bool  // OPEN_OK received; eligible for reaping/eviction
	lastActive atomic.Int64 // unix nanos of the last DATA either way (or open)
	finAt      atomic.Int64 // unix nanos of the first FIN either way, 0 = none

	mu      sync.Mutex
	cond    *sync.Cond
	rx      []byte // node -> broker bytes not yet written to the client
	rxEOF   bool   // node sent FIN
	reset   bool   // RST received or session gone
	credit  int    // bytes we may still send to the node
	pending int    // bytes written to the client but not yet granted back
}

func newMuxSession(c net.Conn, br *bufio.Reader, n *node, maxStreams int) *muxSession {
	if maxStreams <= 0 {
		maxStreams = 24
	}
	s := &muxSession{conn: c, br: br, node: n, maxStreams: maxStreams,
		streams: map[uint32]*muxStream{}, done: make(chan struct{})}
	s.lastRx.Store(time.Now().UnixNano())
	return s
}

func (s *muxSession) writeFrame(typ byte, sid uint32, payload []byte) error {
	var hdr [muxHeaderLen]byte
	hdr[0] = typ
	binary.BigEndian.PutUint16(hdr[2:4], uint16(len(payload)))
	binary.BigEndian.PutUint32(hdr[4:8], sid)
	s.wmu.Lock()
	defer s.wmu.Unlock()
	s.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	if _, err := s.conn.Write(append(hdr[:], payload...)); err != nil {
		s.conn.Close()
		return err
	}
	return nil
}

func (s *muxSession) active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams)
}

func (s *muxSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// run reads frames until the link dies, then tears down every stream.
func (s *muxSession) run() {
	go s.keepalive()
	defer s.shutdown()
	var hdr [muxHeaderLen]byte
	buf := make([]byte, muxMaxPayload)
	for {
		s.conn.SetReadDeadline(time.Now().Add(muxDeadAfter))
		if _, err := io.ReadFull(s.br, hdr[:]); err != nil {
			return
		}
		typ := hdr[0]
		n := int(binary.BigEndian.Uint16(hdr[2:4]))
		sid := binary.BigEndian.Uint32(hdr[4:8])
		if n > muxMaxPayload {
			log.Printf("mux %s: oversized frame (%d), dropping link", s.node.MAC, n)
			return
		}
		p := buf[:n]
		if _, err := io.ReadFull(s.br, p); err != nil {
			return
		}
		s.lastRx.Store(time.Now().UnixNano())
		s.dispatch(typ, sid, p)
	}
}

func (s *muxSession) dispatch(typ byte, sid uint32, p []byte) {
	if sid == 0 {
		if typ == framePing {
			s.writeFrame(framePong, 0, append([]byte(nil), p...))
		}
		return // PONG only refreshes lastRx
	}
	s.mu.Lock()
	st := s.streams[sid]
	s.mu.Unlock()
	if st == nil {
		return // late frame for a stream we already forgot
	}
	switch typ {
	case frameOpenOK:
		select {
		case st.openRes <- "":
		default:
		}
	case frameOpenErr:
		reason := string(p)
		if reason == "" {
			reason = "dial"
		}
		select {
		case st.openRes <- reason:
		default:
		}
	case frameData:
		st.touch()
		st.mu.Lock()
		if len(st.rx)+len(p) > muxWindow {
			st.mu.Unlock()
			log.Printf("mux %s: stream %d overran its window", s.node.MAC, sid)
			s.writeFrame(frameRst, sid, nil)
			st.markReset()
			return
		}
		st.rx = append(st.rx, p...)
		st.cond.Broadcast()
		st.mu.Unlock()
	case frameFin:
		st.markFin()
		st.mu.Lock()
		st.rxEOF = true
		st.cond.Broadcast()
		st.mu.Unlock()
	case frameRst:
		select {
		case st.openRes <- "reset":
		default:
		}
		st.markReset()
	case frameWindow:
		if len(p) == 4 {
			st.mu.Lock()
			st.credit += int(binary.BigEndian.Uint32(p))
			st.cond.Broadcast()
			st.mu.Unlock()
		}
	}
}

func (s *muxSession) keepalive() {
	t := time.NewTicker(muxPingEvery)
	defer t.Stop()
	reap := time.NewTicker(muxReapEvery)
	defer reap.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-reap.C:
			s.reapIdle()
			if s.pollOTA != nil {
				if url := s.pollOTA(); url != "" {
					log.Printf("ota: pushing %s to %s (mux, live)", url, s.node.MAC)
					s.writeFrame(frameOTA, 0, []byte(url))
				}
			}
		case <-t.C:
			if s.node.isBlocked() {
				log.Printf("mux %s: node blocked, closing link", s.node.MAC)
				s.conn.Close()
				return
			}
			if time.Since(time.Unix(0, s.lastRx.Load())) > muxDeadAfter {
				s.conn.Close()
				return
			}
			if s.writeFrame(framePing, 0, nil) != nil {
				return
			}
		}
	}
}

func (s *muxSession) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	streams := s.streams
	s.streams = map[uint32]*muxStream{}
	s.mu.Unlock()
	close(s.done)
	s.conn.Close()
	for _, st := range streams {
		select {
		case st.openRes <- "link closed":
		default:
		}
		st.markReset()
	}
}

// open asks the node to dial host:port. On success the stream is ready to relay.
func (s *muxSession) open(host string, port int) (*muxStream, string) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, "link closed"
	}
	s.nextID++
	st := &muxStream{id: s.nextID, s: s, openRes: make(chan string, 1), credit: muxWindow}
	st.cond = sync.NewCond(&st.mu)
	st.touch()
	s.streams[st.id] = st
	s.mu.Unlock()

	if err := s.writeFrame(frameOpen, st.id, []byte(host+" "+strconv.Itoa(port))); err != nil {
		s.forget(st.id)
		return nil, "link closed"
	}
	select {
	case r := <-st.openRes:
		if r != "" {
			s.forget(st.id)
			return nil, r
		}
		st.touch()
		st.opened.Store(true)
		return st, ""
	case <-time.After(muxOpenWait):
		s.writeFrame(frameRst, st.id, nil)
		s.forget(st.id)
		return nil, "timeout"
	}
}

func (s *muxSession) forget(id uint32) {
	s.mu.Lock()
	delete(s.streams, id)
	s.mu.Unlock()
}

// abort resets a stream from the broker side: the node drops it, the slot is
// freed right away (so a waiting customer can take it) and relay() unwinds,
// closing the customer connection.
func (s *muxSession) abort(st *muxStream) {
	s.writeFrame(frameRst, st.id, nil)
	s.forget(st.id)
	st.markReset()
}

// reapIdle closes streams that carried no data for muxIdleTimeout, and
// half-closed streams whose other side did not finish within muxHalfCloseDrain.
func (s *muxSession) reapIdle() {
	now := time.Now()
	var victims []*muxStream
	s.mu.Lock()
	for _, st := range s.streams {
		if st.expired(now) {
			victims = append(victims, st)
		}
	}
	s.mu.Unlock()
	for _, st := range victims {
		s.abort(st)
		if s.idleClosed != nil {
			atomic.AddInt64(s.idleClosed, 1)
		}
	}
}

// oldestIdle returns the opened stream with the oldest activity, provided it
// has been idle since before `since`; nil if none qualifies.
func (s *muxSession) oldestIdle(since time.Time) *muxStream {
	s.mu.Lock()
	defer s.mu.Unlock()
	var best *muxStream
	for _, st := range s.streams {
		if !st.opened.Load() || st.lastActive.Load() > since.UnixNano() {
			continue
		}
		if best == nil || st.lastActive.Load() < best.lastActive.Load() {
			best = st
		}
	}
	return best
}

func (st *muxStream) touch() { st.lastActive.Store(time.Now().UnixNano()) }

func (st *muxStream) markFin() { st.finAt.CompareAndSwap(0, time.Now().UnixNano()) }

func (st *muxStream) expired(now time.Time) bool {
	if !st.opened.Load() {
		return false // still dialing; muxOpenWait bounds that
	}
	if fin := st.finAt.Load(); fin != 0 && now.Sub(time.Unix(0, fin)) > muxHalfCloseDrain {
		return true
	}
	return now.Sub(time.Unix(0, st.lastActive.Load())) > muxIdleTimeout
}

func (st *muxStream) markReset() {
	st.mu.Lock()
	st.reset = true
	st.cond.Broadcast()
	st.mu.Unlock()
}

// Read returns node->broker bytes; io.EOF after the node's FIN.
func (st *muxStream) Read(p []byte) (int, error) {
	st.mu.Lock()
	for len(st.rx) == 0 && !st.rxEOF && !st.reset {
		st.cond.Wait()
	}
	if len(st.rx) == 0 {
		reset := st.reset
		st.mu.Unlock()
		if reset {
			return 0, errStreamReset
		}
		return 0, io.EOF
	}
	n := copy(p, st.rx)
	st.rx = st.rx[n:]
	if len(st.rx) == 0 {
		st.rx = nil // let the backing array go
	}
	st.mu.Unlock()
	return n, nil
}

// consumed returns credit to the node once enough bytes reached the client.
func (st *muxStream) consumed(n int) {
	st.mu.Lock()
	st.pending += n
	grant := 0
	if st.pending >= muxGrantAt {
		grant, st.pending = st.pending, 0
	}
	st.mu.Unlock()
	if grant > 0 {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(grant))
		st.s.writeFrame(frameWindow, st.id, b[:])
	}
}

// Write sends broker->node bytes, waiting for credit as needed.
func (st *muxStream) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		st.mu.Lock()
		for st.credit == 0 && !st.reset {
			st.cond.Wait()
		}
		if st.reset {
			st.mu.Unlock()
			return written, errStreamReset
		}
		n := min(len(p), st.credit, muxMaxPayload)
		st.credit -= n
		st.mu.Unlock()
		if err := st.s.writeFrame(frameData, st.id, p[:n]); err != nil {
			return written, err
		}
		st.touch()
		written += n
		p = p[n:]
	}
	return written, nil
}

// relay pipes the client connection and the stream until both directions end.
func (st *muxStream) relay(client net.Conn) (up, down int64) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { // node -> client
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := st.Read(buf)
			if n > 0 {
				if _, werr := client.Write(buf[:n]); werr != nil {
					st.s.writeFrame(frameRst, st.id, nil)
					st.markReset()
					break
				}
				down += int64(n)
				st.consumed(n)
			}
			if err == io.EOF {
				closeWrite(client)
				break
			}
			if err != nil {
				client.Close() // node aborted: abort the client too
				break
			}
		}
	}()
	go func() { // client -> node
		defer wg.Done()
		n, err := io.Copy(st, client)
		up = n
		if err == nil {
			st.s.writeFrame(frameFin, st.id, nil)
			st.markFin()
		} else if !errors.Is(err, errStreamReset) {
			st.s.writeFrame(frameRst, st.id, nil)
			st.markReset()
		}
	}()
	wg.Wait()
	// Both directions are finished; tell the node to drop any state it still holds.
	st.s.writeFrame(frameRst, st.id, nil)
	st.s.forget(st.id)
	client.Close()
	return up, down
}
