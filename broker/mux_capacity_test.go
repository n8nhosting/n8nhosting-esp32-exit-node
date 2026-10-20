package main

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- capacity management on a mux link ------------------------------------
//
// A heavy browser user parks dozens of idle keep-alive connections. With a hard
// per-link stream cap that used to mean "no idle node" for every new
// connection until the browser let go. These tests pin the three remedies:
// evict-when-full, idle reaping and a bound on half-closed streams.

// withMuxTimers shortens the production timers for one test.
func withMuxTimers(t *testing.T, idle, drain, evictMin, reap time.Duration) {
	t.Helper()
	oi, od, oe, or := muxIdleTimeout, muxHalfCloseDrain, muxEvictMinIdle, muxReapEvery
	muxIdleTimeout, muxHalfCloseDrain, muxEvictMinIdle, muxReapEvery = idle, drain, evictMin, reap
	t.Cleanup(func() { muxIdleTimeout, muxHalfCloseDrain, muxEvictMinIdle, muxReapEvery = oi, od, oe, or })
}

// closedWithin reports whether the peer closes/resets c within d.
func closedWithin(c net.Conn, d time.Duration) bool {
	c.SetReadDeadline(time.Now().Add(d))
	defer c.SetReadDeadline(time.Time{})
	_, err := c.Read(make([]byte, 1))
	if err == nil {
		return false
	}
	ne, ok := err.(net.Error)
	return !(ok && ne.Timeout())
}

func muxActive(b *broker) int {
	b.muxMu.Lock()
	defer b.muxMu.Unlock()
	n := 0
	for _, m := range b.muxes {
		n += m.active()
	}
	return n
}

func httpOver(t *testing.T, c net.Conn, path string) string {
	t.Helper()
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n", path)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	data, _ := io.ReadAll(c)
	return string(data)
}

// A link at its stream cap makes room for a new customer by resetting the
// stream that has been idle longest, instead of refusing the customer.
func TestMuxFullLinkEvictsIdleStream(t *testing.T) {
	withMuxTimers(t, time.Hour, time.Hour, 50*time.Millisecond, time.Hour)
	oh, op, oclose := originServer(t)
	defer oclose()
	b, _, socks, stop := spinMuxBroker(t, 2)
	defer stop()

	first, err := socks5Connect(socks, oh, op) // oldest idle stream
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	time.Sleep(20 * time.Millisecond)
	second, err := socks5Connect(socks, oh, op) // younger idle stream
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	time.Sleep(150 * time.Millisecond) // both idle longer than muxEvictMinIdle

	body, err := fetchVia(socks, oh, op, "/hello")
	if err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("third customer must get through a full link: err=%v body=%q", err, body)
	}
	if !closedWithin(first, 2*time.Second) {
		t.Fatal("the longest-idle stream should have been reset to make room")
	}
	if got := httpOver(t, second, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
		t.Fatalf("the younger idle stream must survive the eviction, got %q", got)
	}
	if n := atomic.LoadInt64(&b.streamsEvicted); n != 1 {
		t.Fatalf("streams_evicted = %d, want 1", n)
	}
}

// A stream with no data in either direction for muxIdleTimeout is closed so
// parked keep-alives stop occupying the link; a recently active one is kept.
func TestMuxIdleStreamReaped(t *testing.T) {
	withMuxTimers(t, 500*time.Millisecond, time.Hour, time.Hour, 50*time.Millisecond)
	oh, op, oclose := originServer(t)
	defer oclose()
	b, _, socks, stop := spinMuxBroker(t, 24)
	defer stop()

	old, err := socks5Connect(socks, oh, op)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	time.Sleep(300 * time.Millisecond)
	young, err := socks5Connect(socks, oh, op)
	if err != nil {
		t.Fatal(err)
	}
	defer young.Close()
	time.Sleep(350 * time.Millisecond) // old idle ~650ms (> timeout), young ~350ms (< timeout)

	if !closedWithin(old, time.Second) {
		t.Fatal("idle stream should have been reaped")
	}
	if got := httpOver(t, young, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
		t.Fatalf("recently opened stream must not be reaped, got %q", got)
	}
	if n := atomic.LoadInt64(&b.streamsIdleClosed); n != 1 {
		t.Fatalf("streams_idle_closed = %d, want 1", n)
	}
}

// After one side half-closes, the other side gets muxHalfCloseDrain to finish;
// a peer that never closes cannot pin the stream forever.
func TestMuxHalfClosedStreamDrained(t *testing.T) {
	withMuxTimers(t, time.Hour, 300*time.Millisecond, time.Hour, 50*time.Millisecond)
	// A sink that reads everything and never closes its side.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c)
		}
	}()
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	b, _, socks, stop := spinMuxBroker(t, 24)
	defer stop()

	c, err := socks5Connect(socks, host, port)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("x"))
	c.(*net.TCPConn).CloseWrite() // customer is done sending; sink never answers
	waitFor(t, func() bool { return muxActive(b) == 1 })

	if !closedWithin(c, 2*time.Second) {
		t.Fatal("half-closed stream should be reset after the drain timeout")
	}
	waitFor(t, func() bool { return muxActive(b) == 0 })
	if n := atomic.LoadInt64(&b.streamsIdleClosed); n != 1 {
		t.Fatalf("streams_idle_closed = %d, want 1", n)
	}
}
