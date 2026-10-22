package main

import (
	"net"
	"strings"
	"testing"
	"time"
)

// ---- slot overflow: a full or unreachable board borrows a same-IP sibling

// spinOverflowBroker starts a slot broker whose nodes accept the given number
// of streams each, with idle-stream eviction out of the way so a held stream
// keeps its board full.
func spinOverflowBroker(t *testing.T, maxStreams map[string]int) (b *broker, socksAddr string) {
	t.Helper()
	old := muxEvictMinIdle.Get()
	muxEvictMinIdle.Set(time.Hour)
	b = newBroker(config{token: "tok", dialWait: 500 * time.Millisecond, slotSecret: "ss"}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); poolLn.Close(); socksLn.Close(); muxEvictMinIdle.Set(old) })
	for mac, max := range maxStreams {
		go runMuxNode(t, poolLn.Addr().String(), "tok", mac, true, max, nil, stop)
	}
	waitFor(t, func() bool { return b.muxCount() == len(maxStreams) })
	return b, socksLn.Addr().String()
}

// holdSlotStream opens a slot stream and keeps it open for the rest of the test.
func holdSlotStream(t *testing.T, socks, user, host string, port int) {
	t.Helper()
	c, err := socks5ConnectAuth(socks, user, "ss", host, port)
	if err != nil {
		t.Fatalf("hold %s: %v", user, err)
	}
	t.Cleanup(func() { c.Close() })
}

const (
	ovA = "aa:bb:cc:dd:ee:a0" // holds slot 1
	ovB = "aa:bb:cc:dd:ee:b0" // same public IP (both tests nodes dial from 127.0.0.1)
)

// A full board's new connection lands on a shared board behind the same
// public IP, so the customer keeps its exit IP, port and credentials.
func TestSlotOverflowsToSameIPSharedBoardWhenFull(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b, socks := spinOverflowBroker(t, map[string]int{ovA: 1, ovB: 24})
	if err := b.reg.setSlot(ovA, 1); err != nil {
		t.Fatal(err)
	}
	holdSlotStream(t, socks, "slot-1", oh, op)
	c, err := socks5ConnectAuth(socks, "slot-1", "ss", oh, op)
	if err != nil {
		t.Fatalf("overflow dial: %v", err)
	}
	if got := httpOver(t, c, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
		t.Fatalf("overflow body %q", got)
	}
	c.Close()
	a, sib := nodeRow(t, b, ovA), nodeRow(t, b, ovB)
	if a.DialsOK != 1 || sib.DialsOK != 1 || a.DialsOverflow != 1 || sib.DialsOverflow != 0 {
		t.Fatalf("A ok=%d overflow=%d, B ok=%d overflow=%d (want 1 1, 1 0)", a.DialsOK, a.DialsOverflow, sib.DialsOK, sib.DialsOverflow)
	}
	if st := statusOf(t, b); st["dials_overflow"] != float64(1) || st["dials_pinned_offline"] != float64(0) {
		t.Fatalf("status dials_overflow=%v dials_pinned_offline=%v (want 1, 0)", st["dials_overflow"], st["dials_pinned_offline"])
	}
}

// A board behind another public IP would change the customer's exit IP, so
// it is never used: the dial is refused as before.
func TestSlotOverflowNeverChangesExitIP(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b, socks := spinOverflowBroker(t, map[string]int{ovA: 1, ovB: 24})
	b.reg.touch(ovB, "fw", "198.51.100.7", 0, "", "") // B now reports another public IP
	if err := b.reg.setSlot(ovA, 1); err != nil {
		t.Fatal(err)
	}
	holdSlotStream(t, socks, "slot-1", oh, op)
	if c, err := socks5ConnectAuth(socks, "slot-1", "ss", oh, op); err == nil {
		c.Close()
		t.Fatal("dial must be refused, not moved to another exit IP")
	} else if !strings.Contains(err.Error(), "rep=4") {
		t.Fatalf("want rep=4, got %v", err)
	}
	if sib := nodeRow(t, b, ovB); sib.DialsOK != 0 || nodeRow(t, b, ovA).DialsOverflow != 0 {
		t.Fatalf("other-IP board was used: %+v", sib.node)
	}
}

// A dedicated customer's board is reserved for it, and a dedicated board is
// reserved for its own customer: neither side takes part in overflow.
func TestSlotOverflowSkipsDedicatedBoards(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	no := false
	for _, tc := range []struct {
		name                   string
		dedicatedA, dedicatedB bool
	}{{"dedicated customer", true, false}, {"dedicated sibling", false, true}} {
		t.Run(tc.name, func(t *testing.T) {
			b, socks := spinOverflowBroker(t, map[string]int{ovA: 1, ovB: 24})
			poolA, poolB := (*bool)(nil), (*bool)(nil)
			if tc.dedicatedA {
				poolA = &no
			}
			if tc.dedicatedB {
				poolB = &no
			}
			if err := b.reg.setSlotPool(ovA, 1, poolA); err != nil {
				t.Fatal(err)
			}
			if err := b.reg.setSlotPool(ovB, 2, poolB); err != nil {
				t.Fatal(err)
			}
			holdSlotStream(t, socks, "slot-1", oh, op)
			if c, err := socks5ConnectAuth(socks, "slot-1", "ss", oh, op); err == nil {
				c.Close()
				t.Fatal("dial must be refused")
			}
			if sib := nodeRow(t, b, ovB); sib.DialsOK != 0 {
				t.Fatalf("dedicated rule broken: B dials ok=%d", sib.DialsOK)
			}
		})
	}
}

// A sibling keeps a quarter of its streams (at least one) for its own slot
// customer, so overflow never fills it.
func TestSlotOverflowLeavesRoomOnSibling(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	b, socks := spinOverflowBroker(t, map[string]int{ovA: 1, ovB: 2})
	if err := b.reg.setSlot(ovA, 1); err != nil {
		t.Fatal(err)
	}
	holdSlotStream(t, socks, "slot-1", oh, op) // fills A
	holdSlotStream(t, socks, "slot-1", oh, op) // overflows to B, which keeps its second stream
	if c, err := socks5ConnectAuth(socks, "slot-1", "ss", oh, op); err == nil {
		c.Close()
		t.Fatal("third dial must be refused: B's last stream is reserved")
	}
	if a := nodeRow(t, b, ovA); a.DialsOverflow != 1 {
		t.Fatalf("overflow count = %d, want 1", a.DialsOverflow)
	}
}

// A target that refuses the connection is the target's fault, not the
// board's: the dial fails on the pinned board and is not retried elsewhere.
func TestSlotOverflowIgnoresTargetFailures(t *testing.T) {
	b, socks := spinOverflowBroker(t, map[string]int{ovA: 24, ovB: 24})
	if err := b.reg.setSlot(ovA, 1); err != nil {
		t.Fatal(err)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	closedPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	if c, err := socks5ConnectAuth(socks, "slot-1", "ss", "127.0.0.1", closedPort); err == nil {
		c.Close()
		t.Fatal("dial to a closed port must fail")
	}
	if sib := nodeRow(t, b, ovB); sib.DialsOK != 0 || sib.DialsErr != 0 || nodeRow(t, b, ovA).DialsOverflow != 0 {
		t.Fatalf("target failure was retried on B: %+v", sib.node)
	}
}
