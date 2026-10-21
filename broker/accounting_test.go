package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func assertRelayCounters(t *testing.T, b *broker, n *node, up, down int64) {
	t.Helper()
	gotUp, gotDown := atomic.LoadInt64(&b.bytesUp), atomic.LoadInt64(&b.bytesDown)
	nodeUp, nodeDown := atomic.LoadInt64(&n.BytesUp), atomic.LoadInt64(&n.BytesDown)
	if gotUp != up || gotDown != down || nodeUp != up || nodeDown != down {
		t.Fatalf("payload counters broker=%d/%d node=%d/%d want=%d/%d", gotUp, gotDown, nodeUp, nodeDown, up, down)
	}
}

func TestLivePayloadCountersAdvanceBeforeEOFAndDoNotDoubleCount(t *testing.T) {
	for _, transport := range []string{"mux", "legacy"} {
		t.Run(transport, func(t *testing.T) {
			origin, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer origin.Close()
			originDone := make(chan struct{})
			go func() {
				defer close(originDone)
				c, err := origin.Accept()
				if err == nil {
					defer c.Close()
					io.Copy(c, c) // raw payload echo; no HTTP headers in totals
				}
			}()
			var b *broker
			var socks string
			var stop func()
			if transport == "mux" {
				b, _, socks, stop = spinMuxBrokerOpts(t, 4, " win=8192 frame=4096", nil)
			} else {
				b = newBroker(config{token: "tok", dialWait: 2 * time.Second}, 4)
				poolLn, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				socksLn, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					poolLn.Close()
					t.Fatal(err)
				}
				go b.servePool(poolLn)
				go b.serveSocks(socksLn)
				stopCh := make(chan struct{})
				go runNodeMAC(t, poolLn.Addr().String(), "tok", "legacy", true, false, stopCh)
				waitFor(t, func() bool { return b.reg.count() == 1 && b.reg.list()[0].Idle > 0 })
				socks = socksLn.Addr().String()
				stop = func() { close(stopCh); poolLn.Close(); socksLn.Close() }
			}
			defer func() { stop(); waitFor(t, func() bool { return b.muxCount() == 0 }) }()
			host, port, _ := net.SplitHostPort(origin.Addr().String())
			portNumber, _ := strconv.Atoi(port)
			client, err := socks5Connect(socks, host, portNumber)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			client.SetDeadline(time.Now().Add(10 * time.Second))
			var n *node
			b.reg.mu.Lock()
			for _, node := range b.reg.nodes {
				n = node
			}
			b.reg.mu.Unlock()
			var total int64
			for _, size := range []int{128*1024 + 17, 8193} {
				payload := bytes.Repeat([]byte{'p'}, size)
				written := make(chan error, 1)
				go func() { _, err := client.Write(payload); written <- err }()
				echo := make([]byte, size)
				if _, err := io.ReadFull(client, echo); err != nil {
					t.Fatal(err)
				}
				if err := <-written; err != nil || !bytes.Equal(payload, echo) {
					t.Fatalf("payload integrity: %v", err)
				}
				total += int64(size)
				waitFor(t, func() bool {
					return atomic.LoadInt64(&b.bytesUp) == total && atomic.LoadInt64(&b.bytesDown) == total && atomic.LoadInt64(&n.BytesUp) == total && atomic.LoadInt64(&n.BytesDown) == total
				})
				if atomic.LoadInt64(&n.Active) != 1 {
					t.Fatal("counter check happened after the stream closed")
				}
				assertRelayCounters(t, b, n, total, total)
			}
			client.(*net.TCPConn).CloseWrite()
			if _, err := io.Copy(io.Discard, client); err != nil {
				t.Fatal(err)
			}
			waitFor(t, func() bool { return atomic.LoadInt64(&n.Active) == 0 })
			assertRelayCounters(t, b, n, total, total) // no second close-time addition
			select {
			case <-originDone:
			case <-time.After(2 * time.Second):
				t.Fatal("origin connection did not close")
			}
		})
	}
}

type accountingConn struct {
	net.Conn
	input io.Reader
	write func([]byte) (int, error)
}

func newAccountingConn(t *testing.T, input string, write func([]byte) (int, error)) *accountingConn {
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	return &accountingConn{Conn: a, input: strings.NewReader(input), write: write}
}

func (c *accountingConn) Read(p []byte) (int, error)  { return c.input.Read(p) }
func (c *accountingConn) Write(p []byte) (int, error) { return c.write(p) }

func accountingStream(b *broker, conn net.Conn, credit int) (*node, *muxStream) {
	n := b.reg.touch("accounted", "fw", "", 1, "", "")
	s := newMuxSession(conn, bufio.NewReader(conn), n, helloOpts{Window: credit, Frame: credit})
	st := &muxStream{id: 1, s: s, credit: credit}
	st.cond = sync.NewCond(&st.mu)
	s.streams[st.id] = st
	st.onBytes = func(up, down int64) { b.addRelayBytes(n, up, down) }
	return n, st
}

func TestMuxPayloadAccountingAdvancesWhileWaitingForCredit(t *testing.T) {
	b := newBroker(config{}, 1)
	c := newAccountingConn(t, "", func(p []byte) (int, error) { return len(p), nil })
	n, st := accountingStream(b, c, 4)
	finished := make(chan int, 1)
	go func() { count, _ := st.Write([]byte("12345678")); finished <- count }()
	waitFor(t, func() bool { return atomic.LoadInt64(&b.bytesUp) == 4 })
	assertRelayCounters(t, b, n, 4, 0)
	select {
	case <-finished:
		t.Fatal("writer was not waiting for more credit")
	default:
	}
	st.mu.Lock()
	st.credit = 4
	st.cond.Broadcast()
	st.mu.Unlock()
	if count := <-finished; count != 8 {
		t.Fatalf("returned payload count %d", count)
	}
	assertRelayCounters(t, b, n, 8, 0)
}

func TestMuxUploadCountsOnlyActuallyWrittenPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wire    int
		err     error
		payload int
	}{
		{"failed", 0, io.ErrClosedPipe, 0},
		{"partial header", 4, io.ErrClosedPipe, 0},
		{"partial payload", muxHeaderLen + 3, io.ErrClosedPipe, 3},
		{"short write without error", muxHeaderLen + 3, nil, 3},
		{"full payload with error", muxHeaderLen + 10, io.ErrClosedPipe, 10},
		{"full payload", muxHeaderLen + 10, nil, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(config{}, 1)
			c := newAccountingConn(t, "", func(p []byte) (int, error) { return tc.wire, tc.err })
			n, st := accountingStream(b, c, 10)
			written, err := st.Write([]byte("1234567890"))
			if written != tc.payload || (tc.wire < muxHeaderLen+10 && err == nil) {
				t.Fatalf("Write=%d,%v want payload%d", written, err, tc.payload)
			}
			assertRelayCounters(t, b, n, int64(tc.payload), 0)
		})
	}
}

func TestMuxDownloadCountsPartialClientWritesIncludingErrors(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written int
		err     error
	}{
		{"failed", 0, io.ErrClosedPipe},
		{"partial error", 3, io.ErrClosedPipe},
		{"short without error", 3, nil},
		{"full with error", 10, io.ErrClosedPipe},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBroker(config{}, 1)
			wire := newAccountingConn(t, "", func(p []byte) (int, error) { return len(p), nil })
			n, st := accountingStream(b, wire, 16)
			st.rx, st.rxEOF = []byte("1234567890"), true
			writes := 0
			client := newAccountingConn(t, "", func(p []byte) (int, error) {
				writes++
				if writes == 1 {
					return len(p), nil
				} // SOCKS reply is not payload
				return tc.written, tc.err
			})
			b.finishMux(client, st, repSuccess)
			assertRelayCounters(t, b, n, 0, int64(tc.written))
		})
	}
}

func TestLegacyBufferedPayloadCountsActualWrites(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(strconv.FormatBool(partial), func(t *testing.T) {
			b := newBroker(config{}, 1)
			n := b.reg.touch("legacy", "fw", "", 1, "", "")
			client := newAccountingConn(t, "upload", func(p []byte) (int, error) {
				if partial {
					return 3, io.ErrClosedPipe
				}
				return len(p), nil
			})
			esp := newAccountingConn(t, "", func(p []byte) (int, error) {
				if partial {
					return 2, io.ErrClosedPipe
				}
				return len(p), nil
			})
			reader := bufio.NewReaderSize(strings.NewReader(strings.Repeat("b", 64)), 16)
			if _, err := reader.Peek(1); err != nil || reader.Buffered() == 0 {
				t.Fatal("test must have buffered node payload")
			}
			up, down := b.pipe(client, esp, reader, n)
			wantUp, wantDown := int64(6), int64(64)
			if partial {
				wantUp, wantDown = 2, 3
			}
			if up != wantUp || down != wantDown {
				t.Fatalf("returned totals=%d/%d, want%d/%d", up, down, wantUp, wantDown)
			}
			assertRelayCounters(t, b, n, wantUp, wantDown)
		})
	}
}
