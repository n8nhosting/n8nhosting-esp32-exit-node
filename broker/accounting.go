package main

import (
	"io"
	"sync/atomic"
)

// A writer wrapper prevents io.Copy's optimized paths from bypassing live
// accounting. Count the actual accepted bytes even when Write also returns an
// error; short/failed writes must not count the full requested buffer.
type relayWriter struct {
	w       io.Writer
	onWrite func(int)
}

func (w relayWriter) Write(p []byte) (int, error) {
	n, err := w.w.Write(p)
	if n > 0 {
		w.onWrite(n)
	}
	return n, err
}

func (b *broker) addRelayBytes(n *node, up, down int64) {
	if up > 0 {
		atomic.AddInt64(&b.bytesUp, up)
		atomic.AddInt64(&n.BytesUp, up)
	}
	if down > 0 {
		atomic.AddInt64(&b.bytesDown, down)
		atomic.AddInt64(&n.BytesDown, down)
	}
}
