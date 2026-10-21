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

// parseStats reads a STATS payload (node -> broker on sid 0, firmware
// 2.4.23+): "key=value" tokens like HELLO's options, so new keys never need
// a new frame type. Unknown keys are ignored, a bad value leaves its field
// unset, and ok is false when nothing was recognised. cpu is -1 (unknown)
// unless the node gave 0..100.
//
//	up=<s> heap=<free> heapmin=<low water> heaptot=<total>   internal heap, bytes
//	psram=<free> psramtot=<total>                           only with PSRAM
//	cpu=<0..100|-1> app=<image bytes> slot=<partition bytes> flash=<chip bytes>
//	streams=<open mux streams>
//	rssi=<-127..0 dBm>                                    optional, firmware 2.4.25+
func parseStats(s string) (st nodeStats, ok bool) {
	st.CPUPct = -1
	for _, tok := range strings.Fields(s) {
		k, v, found := strings.Cut(tok, "=")
		if !found {
			continue
		}
		if k == "rssi" {
			if value, err := strconv.ParseInt(v, 10, 16); err == nil && value >= -127 && value <= 0 {
				rssi := int(value)
				st.RSSI, ok = &rssi, true
			}
			continue
		}
		u, err := strconv.ParseUint(v, 10, 32)
		if err != nil {
			continue
		}
		switch k {
		case "up":
			st.UptimeSec = u
		case "heap":
			st.HeapFree = uint32(u)
		case "heapmin":
			st.HeapMin = uint32(u)
		case "heaptot":
			st.HeapTotal = uint32(u)
		case "psram":
			st.PSRAMFree = uint32(u)
		case "psramtot":
			st.PSRAMTotal = uint32(u)
		case "cpu":
			if u <= 100 {
				st.CPUPct = int(u)
			}
		case "app":
			st.AppBytes = uint32(u)
		case "slot":
			st.SlotBytes = uint32(u)
		case "flash":
			st.FlashBytes = uint32(u)
		case "streams":
			st.Streams = int(u)
		default:
			continue
		}
		ok = true
	}
	return st, ok
}
