package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// ---- per-device slots: SOCKS5 user/pass "slot-N" pins a customer to a board

// socks5UserPass runs the RFC-1929 sub-negotiation and returns the status byte.
func socks5UserPass(c net.Conn, user, pass string) (byte, error) {
	req := append([]byte{1, byte(len(user))}, user...)
	req = append(req, byte(len(pass)))
	req = append(req, pass...)
	c.Write(req)
	st := make([]byte, 2)
	if _, err := io.ReadFull(c, st); err != nil {
		return 0, err
	}
	if st[0] != 1 {
		return 0, fmt.Errorf("auth version %d", st[0])
	}
	return st[1], nil
}

// socks5Request sends a CONNECT for host:port (domain addr) and reads the reply.
func socks5Request(c net.Conn, host string, port int) error {
	req := []byte{5, 1, 0, 3, byte(len(host))}
	req = append(req, []byte(host)...)
	req = append(req, byte(port>>8), byte(port))
	c.Write(req)
	resp := make([]byte, 10)
	if _, err := io.ReadFull(c, resp); err != nil {
		return err
	}
	if resp[1] != 0 {
		return fmt.Errorf("rep=%d", resp[1])
	}
	return nil
}

// socks5ConnectAuth performs an RFC-1928 CONNECT offering no-auth and
// user/pass (as gost's socks5 connector does) and logs in with user/pass when
// the broker selects it.
func socks5ConnectAuth(proxy, user, pass, host string, port int) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", proxy, 3*time.Second)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (net.Conn, error) { c.Close(); return nil, err }
	c.SetDeadline(time.Now().Add(10 * time.Second))
	c.Write([]byte{5, 2, 0x00, 0x02})
	sel := make([]byte, 2)
	if _, err := io.ReadFull(c, sel); err != nil {
		return fail(err)
	}
	switch sel[1] {
	case 0x00:
	case 0x02:
		st, err := socks5UserPass(c, user, pass)
		if err != nil {
			return fail(err)
		}
		if st != 0 {
			return fail(fmt.Errorf("auth status=%d", st))
		}
	default:
		return fail(fmt.Errorf("method %#x", sel[1]))
	}
	if err := socks5Request(c, host, port); err != nil {
		return fail(err)
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

// spinSlotBroker starts a broker with SLOT_SECRET "ss" and one mux node per MAC.
func spinSlotBroker(t *testing.T, macs ...string) (b *broker, socksAddr string) {
	t.Helper()
	return spinSlotBrokerSecret(t, "ss", macs...)
}

// spinSlotBrokerSecret is spinSlotBroker with the given SLOT_SECRET ("" = unset).
func spinSlotBrokerSecret(t *testing.T, secret string, macs ...string) (b *broker, socksAddr string) {
	t.Helper()
	b = newBroker(config{token: "tok", dialWait: 500 * time.Millisecond, slotSecret: secret}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); poolLn.Close(); socksLn.Close() })
	for _, mac := range macs {
		go runMuxNode(t, poolLn.Addr().String(), "tok", mac, true, 24, nil, stop)
	}
	waitFor(t, func() bool { return b.muxCount() == len(macs) })
	return b, socksLn.Addr().String()
}

func nodeRow(t *testing.T, b *broker, mac string) nodeView {
	t.Helper()
	for _, n := range b.reg.list() {
		if n.MAC == mac {
			return n
		}
	}
	t.Fatalf("node %s not registered", mac)
	return nodeView{}
}

func statusOf(t *testing.T, b *broker) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	b.statusMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("/status: %v (%q)", err, rec.Body.String())
	}
	return m
}

// A slot customer only ever lands on the board holding the slot, even though
// the other board is idle and would win least-loaded selection half the time.
func TestSlotRoutesOnlyToPinnedDevice(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	const macA, macB = "aa:bb:cc:dd:ee:70", "aa:bb:cc:dd:ee:71"
	b, socks := spinSlotBroker(t, macA, macB)
	if err := b.reg.setSlot(macB, 1); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		c, err := socks5ConnectAuth(socks, "slot-1", "ss", oh, op)
		if err != nil {
			t.Fatalf("slot dial %d: %v", i, err)
		}
		if got := httpOver(t, c, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
			t.Fatalf("slot dial %d: body %q", i, got)
		}
		c.Close()
	}
	if a, pinned := nodeRow(t, b, macA), nodeRow(t, b, macB); a.DialsOK != 0 || pinned.DialsOK != 4 {
		t.Fatalf("pinned dials went astray: A=%d B=%d (want 0, 4)", a.DialsOK, pinned.DialsOK)
	}
}

// A slot whose board has no link (or a slot nobody holds) is refused at once
// with "host unreachable"; the customer is never rerouted to the pool.
func TestSlotOfflineIsRefusedNotRerouted(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	const macA, macGone = "aa:bb:cc:dd:ee:72", "aa:bb:cc:dd:ee:99"
	b, socks := spinSlotBroker(t, macA)
	b.reg.touch(macGone, "fw", "1.2.3.4", 0, "") // seen before, no link now
	if err := b.reg.setSlot(macGone, 2); err != nil {
		t.Fatal(err)
	}
	for _, user := range []string{"slot-2", "slot-3"} { // offline board, unassigned slot
		start := time.Now()
		c, err := socks5ConnectAuth(socks, user, "ss", oh, op)
		if err == nil {
			c.Close()
			t.Fatalf("%s: dial must be refused", user)
		}
		if took := time.Since(start); !strings.Contains(err.Error(), "rep=4") || took > time.Second {
			t.Fatalf("%s: want rep=4 within 1s, got %v after %v", user, err, took)
		}
	}
	if a := nodeRow(t, b, macA); a.DialsOK != 0 || a.DialsErr != 0 || muxActive(b) != 0 {
		t.Fatalf("pool node must stay untouched: %+v", a.node)
	}
	st := statusOf(t, b)
	if st["dials_pinned_offline"] != float64(2) || st["slots_assigned"] != float64(1) || st["dials_err"] != float64(0) {
		t.Fatalf("status: dials_pinned_offline=%v slots_assigned=%v dials_err=%v (want 2, 1, 0)",
			st["dials_pinned_offline"], st["slots_assigned"], st["dials_err"])
	}
	for _, k := range []string{"idle_nodes", "mux_links", "nodes_online", "nodes_total", "dials_ok", "dials_no_node",
		"streams_evicted", "streams_idle_closed", "bytes_up", "bytes_down", "uptime_sec", "tls"} {
		if _, ok := st[k]; !ok {
			t.Errorf("/status lost %q", k)
		}
	}
}

// A wrong secret, or a username that is not slot-N, fails the RFC-1929
// sub-negotiation with status 0x01 and the broker hangs up.
func TestSlotBadSecretRejected(t *testing.T) {
	_, socks := spinSlotBroker(t)
	for _, cr := range []struct{ user, pass string }{{"slot-1", "wrong"}, {"slot-1", ""}, {"bob", "ss"}, {"slot-0", "ss"}, {"", "ss"}} {
		c, err := net.DialTimeout("tcp", socks, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		c.Write([]byte{5, 1, 0x02})
		sel := make([]byte, 2)
		if _, err := io.ReadFull(c, sel); err != nil || sel[1] != 0x02 {
			t.Fatalf("%q: broker must select user/pass, got %v %v", cr.user, sel, err)
		}
		if st, err := socks5UserPass(c, cr.user, cr.pass); err != nil || st != 0x01 {
			t.Fatalf("%q/%q: want auth status 1, got %d %v", cr.user, cr.pass, st, err)
		}
		if !closedWithin(c, time.Second) {
			t.Fatalf("%q/%q: connection must be closed after a failed login", cr.user, cr.pass)
		}
		c.Close()
	}
}

// The shared pool keeps working on the same listener with a secret set.
func TestPoolStillNoAuth(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	_, socks := spinSlotBroker(t, "aa:bb:cc:dd:ee:73")

	// A client offering only no-auth gets 0x00 as before.
	if body, err := fetchVia(socks, oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("no-auth pool dial: err=%v body=%q", err, body)
	}

	// gost's pool chain (no credentials) still offers user/pass and TLS next
	// to no-auth; the broker selects user/pass and an empty login is the pool.
	c, err := net.DialTimeout("tcp", socks, 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte{5, 3, 0x00, 0x02, 0x80})
	sel := make([]byte, 2)
	if _, err := io.ReadFull(c, sel); err != nil || sel[1] != 0x02 {
		t.Fatalf("want user/pass selected, got %v %v", sel, err)
	}
	if st, err := socks5UserPass(c, "", ""); err != nil || st != 0 {
		t.Fatalf("empty login must be accepted as the pool: status %d %v", st, err)
	}
	if err := socks5Request(c, oh, op); err != nil {
		t.Fatalf("pool dial after empty login: %v", err)
	}
	if got := httpOver(t, c, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
		t.Fatalf("pool body %q", got)
	}
}

// Without SLOT_SECRET the broker fails closed: it still selects user/pass when
// the client offers it, takes the empty login as the pool, and refuses every
// slot-N login (an unset secret must never match an empty password).
func TestNoSecretPoolWorksSlotsRefused(t *testing.T) {
	oh, op, oclose := originServer(t)
	defer oclose()
	const mac = "aa:bb:cc:dd:ee:76"
	b, socks := spinSlotBrokerSecret(t, "", mac)
	if err := b.reg.setSlot(mac, 1); err != nil {
		t.Fatal(err)
	}
	greet := func(methods ...byte) (net.Conn, byte) {
		t.Helper()
		c, err := net.DialTimeout("tcp", socks, 3*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
		c.Write(append([]byte{5, byte(len(methods))}, methods...))
		sel := make([]byte, 2)
		if _, err := io.ReadFull(c, sel); err != nil {
			c.Close()
			t.Fatalf("greeting %v: %v", methods, err)
		}
		return c, sel[1]
	}

	// a client offering only no-auth still gets 0x00 and the pool
	if body, err := fetchVia(socks, oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("no-auth pool dial: err=%v body=%q", err, body)
	}

	// gost's pool chain offers {no-auth, user/pass}: user/pass is selected and
	// the empty login is the pool
	c, sel := greet(0x00, 0x02)
	if sel != 0x02 {
		c.Close()
		t.Fatalf("no secret, {0x00,0x02} offered: broker selected %#x, want user/pass", sel)
	}
	if st, err := socks5UserPass(c, "", ""); err != nil || st != 0 {
		t.Fatalf("empty login must be the pool: status %d %v", st, err)
	}
	if err := socks5Request(c, oh, op); err != nil {
		t.Fatalf("pool dial after empty login: %v", err)
	}
	if got := httpOver(t, c, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
		t.Fatalf("pool body %q", got)
	}
	c.Close()

	// every slot login is refused, whatever the password
	for _, pass := range []string{"x", ""} {
		c, sel := greet(0x00, 0x02)
		if sel != 0x02 {
			c.Close()
			t.Fatalf("slot-1/%q: broker selected %#x, want user/pass", pass, sel)
		}
		if st, err := socks5UserPass(c, "slot-1", pass); err != nil || st != 0x01 {
			t.Fatalf("slot-1/%q without SLOT_SECRET: want auth status 1, got %d %v", pass, st, err)
		}
		if !closedWithin(c, time.Second) {
			t.Fatalf("slot-1/%q: connection must be closed after a refused login", pass)
		}
		c.Close()
	}
	if n := nodeRow(t, b, mac); n.DialsOK != 2 {
		t.Fatalf("dials_ok = %d, want the 2 pool dials only", n.DialsOK)
	}
}

// postSlot sends POST /nodes/{mac}/slot with body to the broker's status API.
func postSlot(b *broker, mac, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	b.statusMux().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/nodes/"+mac+"/slot", strings.NewReader(body)))
	return rec
}

// A dedicated board (pool false) carries only its slot: unpinned dials never
// land on it, not even while the only pool board is full, and its idle slot
// streams are never evicted to make room for pool traffic.
func TestPoolFalseNodeNeverServesPool(t *testing.T) {
	withMuxTimers(t, time.Hour, time.Hour, 50*time.Millisecond, time.Hour)
	oh, op, oclose := originServer(t)
	defer oclose()
	const macA, macB = "aa:bb:cc:dd:ee:77", "aa:bb:cc:dd:ee:78"
	b := newBroker(config{token: "tok", dialWait: time.Second, slotSecret: "ss"}, 8)
	poolLn, _ := net.Listen("tcp", "127.0.0.1:0")
	socksLn, _ := net.Listen("tcp", "127.0.0.1:0")
	go b.servePool(poolLn)
	go b.serveSocks(socksLn)
	stop := make(chan struct{})
	t.Cleanup(func() { close(stop); poolLn.Close(); socksLn.Close() })
	go runMuxNode(t, poolLn.Addr().String(), "tok", macA, true, 1, nil, stop)  // pool board, one stream
	go runMuxNode(t, poolLn.Addr().String(), "tok", macB, true, 24, nil, stop) // dedicated board
	waitFor(t, func() bool { return b.muxCount() == 2 })
	socks := socksLn.Addr().String()
	if r := postSlot(b, macB, `{"slot":2,"pool":false}`); r.Code != http.StatusOK {
		t.Fatalf("set B slot 2, pool false: %d %s", r.Code, r.Body.String())
	}

	// unpinned dials only ever use A (least-loaded would pick the idle B half the time)
	for i := 0; i < 4; i++ {
		if body, err := fetchVia(socks, oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
			t.Fatalf("pool dial %d: err=%v body=%q", i, err, body)
		}
		waitFor(t, func() bool { return muxActive(b) == 0 })
	}
	if a, d := nodeRow(t, b, macA), nodeRow(t, b, macB); a.DialsOK != 4 || d.DialsOK != 0 {
		t.Fatalf("pool dials: A=%d B=%d, want 4, 0", a.DialsOK, d.DialsOK)
	}

	// B's slot customer parks an idle stream, then a pool customer fills A
	slotC, err := socks5ConnectAuth(socks, "slot-2", "ss", oh, op)
	if err != nil {
		t.Fatalf("slot-2 dial: %v", err)
	}
	defer slotC.Close()
	time.Sleep(20 * time.Millisecond)
	poolC, err := socks5Connect(socks, oh, op)
	if err != nil {
		t.Fatalf("pool dial filling A: %v", err)
	}
	defer poolC.Close()
	time.Sleep(150 * time.Millisecond) // both idle past muxEvictMinIdle; B's is older

	// a new pool customer makes room on A by evicting A's idle stream, not B's
	if body, err := fetchVia(socks, oh, op, "/hello"); err != nil || !strings.Contains(body, "HELLO-ORIGIN") {
		t.Fatalf("pool dial on a full A: err=%v body=%q", err, body)
	}
	if !closedWithin(poolC, 2*time.Second) {
		t.Fatal("A's idle pool stream should have been evicted")
	}
	if got := httpOver(t, slotC, "/hello"); !strings.Contains(got, "HELLO-ORIGIN") {
		t.Fatalf("B's slot stream must never be evicted for pool traffic, got %q", got)
	}
	slotC.Close()

	// A full with nothing evictable: the pool customer is refused, B stays untouched
	muxEvictMinIdle.Set(time.Hour)
	waitFor(t, func() bool { return muxActive(b) == 0 })
	held, err := socks5Connect(socks, oh, op)
	if err != nil {
		t.Fatalf("pool dial filling A again: %v", err)
	}
	defer held.Close()
	if body, err := fetchVia(socks, oh, op, "/hello"); err == nil {
		t.Fatalf("pool dial with A full must be refused, not served by B: %q", body)
	}
	if d := nodeRow(t, b, macB); d.DialsOK != 1 {
		t.Fatalf("B dials_ok = %d, want only the slot dial", d.DialsOK)
	}
}

// The pool flag rides on POST /nodes/{mac}/slot (absent = unchanged), shows in
// /nodes, survives a restart, and nodes stored before it existed default to true.
func TestPoolFlagEndpointAndPersistence(t *testing.T) {
	file := t.TempDir() + "/nodes.json"
	const macA, macB = "aa:bb:cc:dd:ee:79", "aa:bb:cc:dd:ee:7a"
	if err := os.WriteFile(file, []byte(`[{"mac":"`+macA+`","first_seen":"x","last_seen":"x","uptime_sec":0,"blocked":false,"slot":3}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	b := newBroker(config{token: "tok", nodesFile: file}, 8)
	b.reg.touch(macB, "fw", "", 0, "")
	pools := func() map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		b.statusMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nodes", nil))
		var rows []map[string]any
		json.Unmarshal(rec.Body.Bytes(), &rows)
		out := map[string]any{}
		for _, row := range rows {
			out[row["mac"].(string)] = row["pool"]
		}
		return out
	}
	if p := pools(); p[macA] != true || p[macB] != true {
		t.Fatalf("default pool flags = %v, want true for a stored node without one and for a new node", p)
	}
	expect := func(r *httptest.ResponseRecorder, code int, what string) {
		t.Helper()
		if r.Code != code {
			t.Fatalf("%s: status %d (%s), want %d", what, r.Code, strings.TrimSpace(r.Body.String()), code)
		}
	}
	expect(postSlot(b, macB, `{"slot":4,"pool":false}`), http.StatusOK, "B slot 4, pool false")
	expect(postSlot(b, macA, `{"slot":4,"pool":false}`), http.StatusConflict, "A takes B's slot")
	if p := pools(); p[macA] != true || p[macB] != false {
		t.Fatalf("pool flags = %v; a refused request must not change A", p)
	}
	expect(postSlot(b, macB, `{"slot":5}`), http.StatusOK, "B slot 5, pool absent")
	expect(postSlot(b, macA, `{"slot":0,"pool":false}`), http.StatusOK, "A clears its slot, pool false")
	if p := pools(); p[macA] != false || p[macB] != false {
		t.Fatalf("pool flags = %v, want false, false", p)
	}
	r2 := newRegistry(file) // restart
	for _, mac := range []string{macA, macB} {
		if n := r2.nodes[mac]; n == nil || n.Pool || n.inPool() {
			t.Fatalf("pool false not persisted for %s: %+v", mac, n)
		}
	}
	if n := r2.bySlot(5); n == nil || n.MAC != macB {
		t.Fatalf("slot 5 not persisted: %+v", n)
	}
	expect(postSlot(b, macA, `{"slot":0,"pool":true}`), http.StatusOK, "A back in the pool")
	if p := pools(); p[macA] != true {
		t.Fatalf("pool flags = %v, want A true", p)
	}
}

// POST /nodes/{mac}/slot assigns, moves and clears slots, refuses a slot held
// by another board, and the assignment survives a restart.
func TestSlotEndpointAndPersistence(t *testing.T) {
	file := t.TempDir() + "/nodes.json"
	b := newBroker(config{token: "tok", nodesFile: file}, 8)
	const macA, macB = "aa:bb:cc:dd:ee:74", "aa:bb:cc:dd:ee:75"
	b.reg.touch(macA, "fw", "", 0, "")
	b.reg.touch(macB, "fw", "", 0, "")
	h := b.statusMux()
	post := func(mac, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/nodes/"+mac+"/slot", strings.NewReader(body)))
		return rec
	}
	expect := func(r *httptest.ResponseRecorder, code int, what string) {
		t.Helper()
		if r.Code != code {
			t.Fatalf("%s: status %d (%s), want %d", what, r.Code, strings.TrimSpace(r.Body.String()), code)
		}
	}
	expect(post(macA, `{"slot":1}`), http.StatusOK, "assign A=1")
	expect(post(strings.ToUpper(macA), `{"slot":1}`), http.StatusOK, "re-assign A=1 (idempotent, any case)")
	r := post(macB, `{"slot":1}`)
	expect(r, http.StatusConflict, "B=1 while A holds it")
	if !strings.Contains(r.Body.String(), "slot taken") {
		t.Fatalf("conflict body %q", r.Body.String())
	}
	expect(post("aa:bb:cc:dd:ee:00", `{"slot":2}`), http.StatusNotFound, "unknown MAC")
	expect(post(macB, `{"slot":-1}`), http.StatusBadRequest, "negative slot")
	expect(post(macB, `{}`), http.StatusBadRequest, "missing slot")
	expect(post(macB, `{"slot":2}`), http.StatusOK, "assign B=2")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nodes", nil))
	var rows []map[string]any
	json.Unmarshal(rec.Body.Bytes(), &rows)
	slots := map[string]any{}
	for _, row := range rows {
		slots[row["mac"].(string)] = row["slot"]
	}
	if slots[macA] != float64(1) || slots[macB] != float64(2) {
		t.Fatalf("/nodes slots = %v", slots)
	}
	if st := statusOf(t, b); st["slots_assigned"] != float64(2) {
		t.Fatalf("slots_assigned = %v, want 2", st["slots_assigned"])
	}

	r2 := newRegistry(file) // restart
	if n := r2.bySlot(1); n == nil || n.MAC != macA {
		t.Fatalf("slot 1 not persisted: %+v", n)
	}
	if n := r2.bySlot(2); n == nil || n.MAC != macB {
		t.Fatalf("slot 2 not persisted: %+v", n)
	}

	expect(post(macA, `{"slot":0}`), http.StatusOK, "clear A")
	if b.reg.bySlot(1) != nil || newRegistry(file).bySlot(1) != nil {
		t.Fatal("slot 1 must be free (and persisted free) after clearing")
	}
	expect(post(macB, `{"slot":1}`), http.StatusOK, "move B 2->1")
	if n := b.reg.bySlot(1); n == nil || n.MAC != macB || b.reg.bySlot(2) != nil {
		t.Fatal("moving B must free slot 2")
	}
}
