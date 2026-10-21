package main

import (
	"strings"
	"testing"
)

func TestParseHelloOpts(t *testing.T) {
	cases := []struct {
		line string
		want helloOpts
	}{
		// today's firmware: positional max_streams, no tokens
		{"HELLO t m fw 1 - mux1 40", helloOpts{40, 32768, 16384, ""}},
		// small board advertising its receive window, frame size and chip
		{"HELLO t m fw 1 - mux1 4 win=8192 frame=4096 chip=esp32c3", helloOpts{4, 8192, 4096, "esp32c3"}},
		// token order does not matter
		{"HELLO t m fw 1 - mux1 4 frame=4096 win=8192", helloOpts{4, 8192, 4096, ""}},
		// garbage values fall back to the defaults, never to zero
		{"HELLO t m fw 1 - mux1 4 win=abc frame=0 chip=../x", helloOpts{4, 32768, 16384, ""}},
		// out-of-range values fall back too
		{"HELLO t m fw 1 - mux1 4 win=1 frame=99999", helloOpts{4, 32768, 16384, ""}},
		// unknown keys are ignored; chip is lower-cased
		{"HELLO t m fw 1 - mux1 4 bogus=1 chip=ESP32C3", helloOpts{4, 32768, 16384, "esp32c3"}},
		// no max_streams at all
		{"HELLO t m fw 1 - mux1", helloOpts{0, 32768, 16384, ""}},
	}
	for _, c := range cases {
		if got := parseHelloOpts(strings.Fields(c.line)); got != c.want {
			t.Errorf("%q: got %+v want %+v", c.line, got, c.want)
		}
	}
}

func TestParseStats(t *testing.T) {
	st, ok := parseStats("up=600 heap=140000 heapmin=26000 heaptot=320000 cpu=12 app=1245184 slot=2031616 flash=4194304 streams=3")
	want := nodeStats{UptimeSec: 600, HeapFree: 140000, HeapMin: 26000, HeapTotal: 320000, CPUPct: 12,
		AppBytes: 1245184, SlotBytes: 2031616, FlashBytes: 4194304, Streams: 3}
	if !ok || st != want {
		t.Fatalf("got %+v (ok=%v) want %+v", st, ok, want)
	}
	// PSRAM only when the board has it
	st, _ = parseStats("heap=1 psram=7000000 psramtot=8388608")
	if st.PSRAMFree != 7000000 || st.PSRAMTotal != 8388608 {
		t.Fatalf("psram: %+v", st)
	}
	// partial line with garbage: known fields parsed, bad ones unset, cpu unknown (-1)
	st, ok = parseStats("heap=1000 cpu=abc bogus=1 up=-5")
	if !ok || st.HeapFree != 1000 || st.CPUPct != -1 || st.UptimeSec != 0 {
		t.Fatalf("partial: %+v ok=%v", st, ok)
	}
	// the firmware says cpu=-1 when it cannot measure; out of range is unknown too
	if st, _ = parseStats("heap=1 cpu=-1"); st.CPUPct != -1 {
		t.Fatalf("cpu=-1: %+v", st)
	}
	if st, _ = parseStats("heap=1 cpu=250"); st.CPUPct != -1 {
		t.Fatalf("cpu=250: %+v", st)
	}
	// nothing usable
	for _, s := range []string{"", "garbage", "=", "x=1"} {
		if _, ok := parseStats(s); ok {
			t.Fatalf("%q parsed as a sample", s)
		}
	}
}
