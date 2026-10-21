package main

import (
	"regexp"
	"strconv"
	"strings"
)

// HELLO capabilities.
//
//	HELLO <token> <mac> <fw> <uptime_s> <ssid_b64> mux1 <max_streams> [key=value ...]
//
// f[7] is the node's stream limit (2.4.21-mux1 and later). Anything after it is
// an optional key=value token; unknown keys are ignored and a bad value keeps
// the default, so every deployed firmware keeps working:
//
//	win=<bytes>    initial per-stream credit the NODE grants (broker -> node
//	               direction); small boards without PSRAM advertise 8192
//	frame=<bytes>  largest DATA payload the node accepts
//	chip=<name>    esp32s3 / esp32c3; shown on the dashboard and checked by
//	               the OTA target guard
const (
	helloDefaultWindow = 32768
	helloDefaultFrame  = 16384
	helloMinWindow     = 4096
	helloMaxWindow     = 262144
	helloMinFrame      = 1024
	helloMaxFrame      = muxMaxPayload // the broker's own frame buffer
)

var helloChipRE = regexp.MustCompile(`^[a-z0-9]{1,16}$`)

type helloOpts struct {
	MaxStreams int    // 0 = not given (newMuxSession applies its default)
	Window     int    // bytes, broker -> node credit per stream
	Frame      int    // bytes, max DATA payload broker -> node
	Chip       string // "" when the node did not say
}

// parseHelloOpts reads the optional fields of a HELLO line split by
// strings.Fields. It never fails: a missing or malformed field is a default.
func parseHelloOpts(f []string) helloOpts {
	o := helloOpts{Window: helloDefaultWindow, Frame: helloDefaultFrame}
	if len(f) >= 8 {
		o.MaxStreams, _ = strconv.Atoi(f[7])
	}
	for _, tok := range f[min(len(f), 8):] {
		k, v, ok := strings.Cut(tok, "=")
		if !ok {
			continue
		}
		switch k {
		case "win":
			if n, err := strconv.Atoi(v); err == nil && n >= helloMinWindow && n <= helloMaxWindow {
				o.Window = n
			}
		case "frame":
			if n, err := strconv.Atoi(v); err == nil && n >= helloMinFrame && n <= helloMaxFrame {
				o.Frame = n
			}
		case "chip":
			if c := strings.ToLower(v); helloChipRE.MatchString(c) {
				o.Chip = c
			}
		}
	}
	return o
}
