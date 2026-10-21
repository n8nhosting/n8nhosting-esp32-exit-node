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
