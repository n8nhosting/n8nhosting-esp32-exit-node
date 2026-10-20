// measure.go - a board's capacity, measured the way a customer sees it: a
// download of SPEEDTEST_URL through gost's slot port for the board, as a
// throwaway _measure- user bound to it (uncapped and seatless, see
// admission.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	measureMaxBytes = 8 << 20          // the size of the speedtest object
	measureMinBytes = 64 << 10         // less is no measurement
	measureTimeout  = 45 * time.Second // a slow board is measured on what it got through by then
)

// measureProxyURL is gost's slot service for slot, as admin-api reaches it
// inside the compose network. A var so tests can point it at a fake proxy.
var measureProxyURL = func(slot int) string {
	return "http://" + envOr("GOST_INTERNAL_HOST", "gost") + ":" + strconv.Itoa(slotPort(slot))
}

// measureViaProxy downloads targetURL through the HTTP proxy at proxyURL (its
// userinfo is sent as Proxy-Authorization) and returns the transfer rate in
// Mbps. It reads up to maxBytes, or what arrives before ctx's deadline; the
// clock starts when the response headers arrive. A non-2xx answer, or under
// 64 KiB received, is an error. Errors never quote proxyURL (it holds the
// password).
func measureViaProxy(ctx context.Context, proxyURL, targetURL string, maxBytes int64) (mbps float64, bytes int64, err error) {
	pu, err := url.Parse(proxyURL)
	if err != nil {
		return 0, 0, errors.New("bad proxy url")
	}
	tr := &http.Transport{Proxy: http.ProxyURL(pu), DisableCompression: true, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return 0, 0, err
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return 0, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return 0, 0, fmt.Errorf("HTTP %s", resp.Status)
	}
	start := time.Now()
	bytes, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBytes))
	secs := time.Since(start).Seconds()
	if err != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return 0, bytes, err
	}
	if bytes < measureMinBytes || secs <= 0 {
		return 0, bytes, fmt.Errorf("too little data (%d bytes)", bytes)
	}
	return float64(bytes) * 8 / secs / 1e6, bytes, nil
}

// beginMeasure marks mac as being measured; false when it already is.
func (s *server) beginMeasure(mac string) bool {
	s.measureMu.Lock()
	defer s.measureMu.Unlock()
	if s.measuring[mac] {
		return false
	}
	if s.measuring == nil {
		s.measuring = map[string]bool{}
	}
	s.measuring[mac] = true
	return true
}

func (s *server) endMeasure(mac string) {
	s.measureMu.Lock()
	defer s.measureMu.Unlock()
	delete(s.measuring, mac)
}

// activeDials is the broker's active_dials for mac: the streams the board is
// carrying. 0 when the broker is unreachable or does not list mac.
func (s *server) activeDials(mac string) int {
	var nodes []brokerNode
	if err := s.brokerGET("/nodes", &nodes); err != nil {
		return 0
	}
	for _, n := range nodes {
		if n.MAC == mac {
			return int(n.Active)
		}
	}
	return 0
}

// POST /api/devices/{mac}/measure — download SPEEDTEST_URL through the board's
// slot port and store the rate as its capacity, with the streams the board
// was already carrying (a measurement under load is a lower bound). The
// throwaway user is created straight in the store (no admission) and always
// deleted. A failed measurement answers 502 and leaves the record as it was.
func (s *server) handleMeasure(w http.ResponseWriter, r *http.Request, mac string) {
	d, err := s.devices.Get(mac)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()})
		return
	}
	if d.Slot == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no slot"})
		return
	}
	if !s.beginMeasure(mac) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "measurement in progress"})
		return
	}
	defer s.endMeasure(mac)
	pu, err := url.Parse(measureProxyURL(d.Slot))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "bad GOST_INTERNAL_HOST"})
		return
	}

	// no measurement of mac is running, so a measurement user still bound to
	// it was left by a crash: remove it before it is forgotten for good
	for _, u := range s.store.List() {
		if isReservedUsername(u.Username) && u.DeviceMAC == mac {
			if err := s.store.Delete(u.Username); err != nil && !errors.Is(err, ErrNotFound) {
				log.Printf("measure %s: delete stale %s: %v", mac, u.Username, err)
			}
		}
	}
	name, pw := measurePrefix+randToken(3), randToken(16)
	err = s.store.Create(&User{Username: name, PassHash: hashPassword(pw), CredEnc: encryptCred(pw), Enabled: true, DeviceMAC: mac})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not create measurement user"})
		return
	}
	defer func() {
		if err := s.store.Delete(name); err != nil {
			log.Printf("measure %s: delete %s: %v", mac, name, err)
		}
	}()
	pu.User = url.UserPassword(name, pw)

	active := s.activeDials(mac) // just before the download
	ctx, cancel := context.WithTimeout(r.Context(), measureTimeout)
	defer cancel()
	target := envOr("SPEEDTEST_URL", "https://flash.jbacteam.com/speedtest.bin")
	mbps, n, err := measureViaProxy(ctx, pu.String(), target, measureMaxBytes)
	if err != nil {
		log.Printf("measure %s (slot %d): %v", mac, d.Slot, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "measure failed: " + err.Error()})
		return
	}
	capMbps := math.Round(mbps*10) / 10
	at := time.Now().UTC().Format(time.RFC3339)
	err = s.devices.Upsert(mac, func(d *Device) error {
		d.CapacityMbps, d.CapacityMeasuredAt, d.CapacityActiveStreams = capMbps, at, active
		return nil
	})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save device"})
		return
	}
	log.Printf("measure %s (slot %d): %.1f Mbps, %d bytes, %d active streams", mac, d.Slot, capMbps, n, active)
	writeJSON(w, http.StatusOK, map[string]any{
		"capacity_mbps":  capMbps,
		"bytes":          n,
		"seconds":        math.Round(float64(n)*8/mbps/1e6*100) / 100,
		"active_streams": active,
	})
}
