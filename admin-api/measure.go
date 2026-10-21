// measure.go - a board's capacity, measured the way a customer sees it: a
// download of SPEEDTEST_URL through gost's slot port for the board, as a
// throwaway _measure- user bound to it (uncapped and seatless, see
// admission.go).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	measureMaxBytes        = 8 << 20          // the size of the speedtest object
	measureMinBytes        = 64 << 10         // less is no measurement
	measureTimeout         = 45 * time.Second // a slow board is measured on what it got through by then
	measureSoakMaxBytes    = 512 << 20        // per worker, even if the timed origin is unusually fast
	measureRolloverRetries = 3
	measureWorkerRetries   = 8
	measureRolloverWindow  = time.Second
)

// Identified from the local proxy's CONNECT response, never an origin HTTP
// status or an error-message match. A full C3 mux can briefly retain the old
// tunnel while a timed worker rotates to its next object.
var errMeasureConnectUnavailable = errors.New("proxy CONNECT: Service Unavailable")

type measureOptions struct {
	Parallel    int  `json:"parallel"`
	DurationSec int  `json:"duration_sec"`
	Persist     bool `json:"persist"`
}

func readMeasureOptions(body io.Reader) (measureOptions, error) {
	opts := measureOptions{Parallel: 1, Persist: true}
	if body == nil {
		return opts, nil
	}
	dec := json.NewDecoder(io.LimitReader(body, 4097))
	var raw json.RawMessage
	if err := dec.Decode(&raw); errors.Is(err, io.EOF) {
		return opts, nil
	} else if err != nil {
		return opts, errors.New("bad measurement options")
	}
	if len(raw) > 4096 || !strings.HasPrefix(strings.TrimSpace(string(raw)), "{") {
		return opts, errors.New("measurement options must be an object")
	}
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	for _, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			return opts, errors.New("measurement options cannot be null")
		}
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return opts, errors.New("bad measurement options")
	}
	strict := json.NewDecoder(strings.NewReader(string(raw)))
	strict.DisallowUnknownFields()
	if err := strict.Decode(&opts); err != nil {
		return opts, errors.New("bad measurement options")
	}
	if opts.Parallel != 1 && opts.Parallel != 2 && opts.Parallel != 4 {
		return opts, errors.New("parallel must be 1, 2 or 4")
	}
	if opts.DurationSec != 0 && (opts.DurationSec < 30 || opts.DurationSec > 300) {
		return opts, errors.New("duration_sec must be 0 or between 30 and 300")
	}
	return opts, nil
}

type measureStreamResult struct {
	Stream      int     `json:"stream"`
	Bytes       int64   `json:"bytes"`
	Seconds     float64 `json:"seconds"`
	Mbps        float64 `json:"mbps"`
	Downloads   int     `json:"downloads"`
	Retries     int     `json:"retries"`
	Connect503s int     `json:"connect_503s"`
	Completed   bool    `json:"completed"`
	Error       string  `json:"error,omitempty"`
	StopReason  string  `json:"stop_reason"`
}

type measureDiagnosticResult struct {
	Bytes    int64
	Seconds  float64
	Mbps     float64
	Streams  []measureStreamResult
	Complete bool
}

// Each worker owns one request at a time. A timed soak repeats the fixed object
// until its shared context or byte budget ends; it never accepts a target from
// the caller. All workers finish before the throwaway account is deleted.
func measureDiagnostic(ctx context.Context, proxyURL, target string, parallel int, repeat bool) measureDiagnosticResult {
	return measureDiagnosticUsing(ctx, proxyURL, target, parallel, repeat, measureViaProxy)
}

// The injected downloader lets policy tests reproduce a completed object and
// precise CONNECT rejection without changing production TLS verification.
func measureDiagnosticUsing(ctx context.Context, proxyURL, target string, parallel int, repeat bool, download func(context.Context, string, string, int64) (float64, int64, error)) measureDiagnosticResult {
	start := time.Now()
	result := measureDiagnosticResult{Streams: make([]measureStreamResult, parallel), Complete: true}
	var wg sync.WaitGroup
	for i := range result.Streams {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			workerStart := time.Now()
			r := measureStreamResult{Stream: i + 1, StopReason: "object_complete"}
			budget := int64(measureMaxBytes)
			if repeat {
				budget = measureSoakMaxBytes
			}
			for r.Bytes < budget {
				if budget-r.Bytes < measureMinBytes {
					r.StopReason = "byte_limit"
					break
				}
				if ctx.Err() != nil {
					break
				}
				var n int64
				var err, lastConnect503 error
				objectRetries := 0
				var retryUntil time.Time
				for {
					_, n, err = download(ctx, proxyURL, target, min(int64(measureMaxBytes), budget-r.Bytes))
					r.Bytes += n
					if errors.Is(err, errMeasureConnectUnavailable) {
						r.Connect503s++
						lastConnect503 = err
					}
					// An unfinished retry with no body must not convert an earlier
					// CONNECT failure into a successful end-of-duration sample.
					if err != nil && n == 0 && objectRetries > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) && !errors.Is(err, errMeasureConnectUnavailable) {
						err = errors.Join(lastConnect503, err)
					}
					if !repeat || r.Downloads == 0 || n != 0 || !errors.Is(err, errMeasureConnectUnavailable) || ctx.Err() != nil || objectRetries >= measureRolloverRetries || r.Retries >= measureWorkerRetries {
						break
					}
					if retryUntil.IsZero() {
						retryUntil = time.Now().Add(measureRolloverWindow)
					}
					backoff := (100 * time.Millisecond) << objectRetries
					if !time.Now().Add(backoff).Before(retryUntil) {
						break
					}
					if !waitMeasureRollover(ctx, backoff) || !time.Now().Before(retryUntil) {
						break
					}
					objectRetries++
					r.Retries++ // only count the retry actually about to run
				}
				if err != nil {
					if errors.Is(ctx.Err(), context.DeadlineExceeded) && r.Bytes >= measureMinBytes && !errors.Is(err, errMeasureConnectUnavailable) {
						break
					}
					r.Error = strings.ReplaceAll(err.Error(), proxyURL, "proxy")
					r.StopReason = "error"
					break
				}
				if ctx.Err() == nil {
					r.Downloads++
				}
				if !repeat {
					break
				}
			}
			r.Seconds = time.Since(workerStart).Seconds()
			if r.Seconds > 0 {
				r.Mbps = float64(r.Bytes) * 8 / r.Seconds / 1e6
			}
			if ctx.Err() != nil {
				if errors.Is(ctx.Err(), context.Canceled) {
					r.Error = "measurement cancelled"
					r.StopReason = "cancelled"
				} else if r.Error == "" {
					r.StopReason = "duration_elapsed"
				}
			} else if repeat && r.Bytes >= budget {
				r.StopReason = "byte_limit"
			}
			if r.Bytes < measureMinBytes && r.Error == "" {
				r.Error = fmt.Sprintf("too little data (%d bytes)", r.Bytes)
				r.StopReason = "error"
			}
			r.Completed = r.Error == "" && r.Bytes >= measureMinBytes
			result.Streams[i] = r
		}(i)
	}
	wg.Wait()
	result.Seconds = time.Since(start).Seconds()
	for _, r := range result.Streams {
		result.Bytes += r.Bytes
		result.Complete = result.Complete && r.Completed
	}
	if result.Seconds > 0 {
		result.Mbps = float64(result.Bytes) * 8 / result.Seconds / 1e6
	}
	return result
}

func waitMeasureRollover(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return ctx.Err() == nil
	}
}

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
	tr.OnProxyConnectResponse = func(_ context.Context, _ *url.URL, _ *http.Request, resp *http.Response) error {
		if resp.StatusCode == http.StatusServiceUnavailable {
			return errMeasureConnectUnavailable
		}
		return nil
	}
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
	opts, err := readMeasureOptions(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
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
	timeout := measureTimeout
	if opts.DurationSec > 0 {
		timeout = time.Duration(opts.DurationSec) * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	target := envOr("SPEEDTEST_URL", "https://flash.jbacteam.com/speedtest.bin")
	var mbps float64
	var n int64
	var diagnostic *measureDiagnosticResult
	if opts.Parallel == 1 && opts.DurationSec == 0 && opts.Persist {
		mbps, n, err = measureViaProxy(ctx, pu.String(), target, measureMaxBytes)
	} else {
		result := measureDiagnostic(ctx, pu.String(), target, opts.Parallel, opts.DurationSec > 0)
		diagnostic = &result
		mbps, n = result.Mbps, result.Bytes
		if !result.Complete {
			err = errors.New("one or more measurement streams failed")
		}
	}
	if r.Context().Err() != nil {
		err = errors.New("measurement cancelled")
	}
	response := map[string]any{"bytes": n, "active_streams": active}
	if diagnostic != nil {
		response["parallel"], response["duration_sec"], response["persisted"] = opts.Parallel, opts.DurationSec, false
		response["aggregate_mbps"], response["seconds"], response["streams"], response["all_complete"] = diagnostic.Mbps, diagnostic.Seconds, diagnostic.Streams, diagnostic.Complete && r.Context().Err() == nil
	}
	if err != nil {
		log.Printf("measure %s (slot %d): %v", mac, d.Slot, err)
		response["error"] = "measure failed: " + err.Error()
		writeJSON(w, http.StatusBadGateway, response)
		return
	}
	capMbps := math.Round(mbps*10) / 10
	at := time.Now().UTC().Format(time.RFC3339)
	if opts.Persist {
		err = s.devices.Upsert(mac, func(d *Device) error {
			d.CapacityMbps, d.CapacityMeasuredAt, d.CapacityActiveStreams = capMbps, at, active
			return nil
		})
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not save device"})
		return
	}
	log.Printf("measure %s (slot %d): %.1f Mbps, %d bytes, %d active streams", mac, d.Slot, capMbps, n, active)
	if opts.Persist {
		s.emit("device.measured", "info", "device", mac, "admin", "Device capacity measured", map[string]any{"capacity_mbps": capMbps, "active_streams": active, "bytes": n})
	} else {
		s.emit("device.diagnostic", "info", "device", mac, "admin", "Bounded throughput diagnostic completed", map[string]any{"parallel": opts.Parallel, "duration_sec": opts.DurationSec, "bytes": n, "aggregate_mbps": mbps})
	}
	response["capacity_mbps"] = capMbps
	if diagnostic == nil {
		response["seconds"] = math.Round(float64(n)*8/mbps/1e6*100) / 100
	} else {
		response["persisted"] = opts.Persist
	}
	writeJSON(w, http.StatusOK, response)
}
