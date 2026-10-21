package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMeasureConnect503IsTypedAndDistinctFromOrigin503(t *testing.T) {
	var connects atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodConnect {
			connects.Add(1)
		}
		http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
	}))
	defer proxy.Close()
	pu, _ := url.Parse(proxy.URL)
	pu.User = url.UserPassword("_measure-test", "secret-do-not-expose")
	_, n, err := measureViaProxy(context.Background(), pu.String(), "https://speedtest.invalid/speedtest.bin", measureMaxBytes)
	var wrapped *url.Error
	if n != 0 || !errors.As(err, &wrapped) || !errors.Is(err, errMeasureConnectUnavailable) || connects.Load() != 1 {
		t.Fatalf("CONNECT response classification: bytes=%d err=%v connects=%d", n, err, connects.Load())
	}
	if strings.Contains(err.Error(), "secret-do-not-expose") || strings.Contains(err.Error(), "_measure-test") {
		t.Fatal("CONNECT error exposes proxy credentials")
	}
	_, n, err = measureViaProxy(context.Background(), pu.String(), "http://speedtest.invalid/speedtest.bin", measureMaxBytes)
	if n != 0 || err == nil || errors.Is(err, errMeasureConnectUnavailable) || !strings.Contains(err.Error(), "HTTP 503") || connects.Load() != 1 {
		t.Fatalf("origin503 confused with CONNECT503: %d %v", n, err)
	}
}

func rolloverConnectError() error {
	return &url.Error{Op: "Get", URL: "https://speedtest.invalid/speedtest.bin", Err: errMeasureConnectUnavailable}
}

func TestMeasureRolloverRetryRecoversWithHonestElapsedAndCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	var calls int
	download := func(ctx context.Context, proxy, target string, maxBytes int64) (float64, int64, error) {
		calls++
		switch calls {
		case 1, 3:
			return 1, measureMinBytes, nil
		case 2:
			return 0, 0, rolloverConnectError()
		default:
			<-ctx.Done()
			return 0, 0, ctx.Err()
		}
	}
	r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
	stream := r.Streams[0]
	if !r.Complete || stream.Retries != 1 || stream.Connect503s != 1 || stream.Downloads != 2 || stream.Bytes != 2*measureMinBytes || r.Bytes != stream.Bytes || stream.StopReason != "duration_elapsed" {
		t.Fatalf("recovered rollover: %+v", r)
	}
	if r.Seconds < .20 || stream.Seconds < .20 || r.Mbps != float64(r.Bytes)*8/r.Seconds/1e6 {
		t.Fatalf("backoff excluded from actual throughput: %+v", r)
	}
}

func TestMeasureRolloverNeverRetriesInitialOrUnrelatedFailures(t *testing.T) {
	for _, tc := range []struct {
		name     string
		initial  bool
		failure  error
		partial  int64
		want503s int
	}{
		{"initial_connect503", true, rolloverConnectError(), 0, 1},
		{"origin503", false, fmt.Errorf("HTTP 503 Service Unavailable"), 0, 0},
		{"same_message_network_error", false, &url.Error{Op: "Get", URL: "https://speedtest.invalid", Err: errors.New("Service Unavailable")}, 0, 0},
		{"body_read_failure", false, io.ErrUnexpectedEOF, measureMinBytes, 0},
		{"context_unrelated_network_error", false, errors.New("connection reset by peer"), 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			download := func(context.Context, string, string, int64) (float64, int64, error) {
				calls++
				if !tc.initial && calls == 1 {
					return 1, measureMinBytes, nil
				}
				return 0, tc.partial, tc.failure
			}
			r := measureDiagnosticUsing(context.Background(), "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
			wantCalls, wantBytes := 2, int64(measureMinBytes)+tc.partial
			if tc.initial {
				wantCalls = 1
				wantBytes = tc.partial
			}
			stream := r.Streams[0]
			if r.Complete || stream.Completed || stream.Retries != 0 || stream.Connect503s != tc.want503s || calls != wantCalls || r.Bytes != wantBytes || stream.StopReason != "error" {
				t.Fatalf("failure was hidden/retried: %+v calls=%d", r, calls)
			}
		})
	}
}

func TestMeasureRolloverRetriesAreBoundedPerObjectAndPerWorker(t *testing.T) {
	for _, perWorker := range []bool{false, true} {
		t.Run(map[bool]string{false: "per_object", true: "per_worker"}[perWorker], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			calls := 0
			download := func(context.Context, string, string, int64) (float64, int64, error) {
				calls++
				if calls == 1 || perWorker && calls%2 == 1 {
					return 1, measureMinBytes, nil
				}
				return 0, 0, rolloverConnectError()
			}
			r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
			stream := r.Streams[0]
			wantRetries, wantFailures, wantDownloads, wantCalls := 3, 4, 1, 5
			if perWorker {
				wantRetries, wantFailures, wantDownloads, wantCalls = 8, 9, 9, 18
			}
			if r.Complete || stream.Retries != wantRetries || stream.Connect503s != wantFailures || stream.Downloads != wantDownloads || calls != wantCalls || stream.Bytes != int64(wantDownloads)*measureMinBytes || !strings.Contains(stream.Error, "Service Unavailable") {
				t.Fatalf("retry budget not enforced: %+v calls=%d", r, calls)
			}
		})
	}
}

func TestMeasureRolloverEligibilityWindowDoesNotScheduleLateRetries(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	calls := 0
	download := func(context.Context, string, string, int64) (float64, int64, error) {
		calls++
		if calls == 1 {
			return 1, measureMinBytes, nil
		}
		if calls == 3 {
			time.Sleep(measureRolloverWindow)
		}
		return 0, 0, rolloverConnectError()
	}
	r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
	if r.Complete || calls != 3 || r.Streams[0].Retries != 1 || r.Streams[0].Connect503s != 2 {
		t.Fatalf("scheduled retry outside rollover window: %+v calls=%d", r, calls)
	}
}

func TestMeasureRolloverDeadlineAndCancellationPreserveFailure(t *testing.T) {
	for _, tc := range []struct {
		name        string
		timeout     time.Duration
		cancel      bool
		waitOnRetry bool
	}{
		{"deadline_during_backoff", 40 * time.Millisecond, false, false},
		{"cancel_during_backoff", time.Second, true, false},
		{"deadline_during_retry", 160 * time.Millisecond, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			calls := 0
			download := func(ctx context.Context, _ string, _ string, _ int64) (float64, int64, error) {
				calls++
				if calls == 1 {
					return 1, measureMinBytes, nil
				}
				if calls == 2 {
					if tc.cancel {
						cancel()
					}
					return 0, 0, rolloverConnectError()
				}
				if tc.waitOnRetry {
					<-ctx.Done()
					return 0, 0, ctx.Err()
				}
				return 0, 0, rolloverConnectError()
			}
			r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
			stream := r.Streams[0]
			wantRetries := 0
			if tc.waitOnRetry {
				wantRetries = 1
			}
			if r.Complete || stream.Completed || stream.Retries != wantRetries || stream.Connect503s != 1 || stream.Downloads != 1 || stream.Bytes != measureMinBytes {
				t.Fatalf("deadline/cancel hid rollover rejection: %+v", r)
			}
			if !tc.cancel && !strings.Contains(stream.Error, "Service Unavailable") {
				t.Fatalf("original503 missing: %+v", stream)
			}
			if tc.cancel && stream.StopReason != "cancelled" {
				t.Fatalf("cancel not reported: %+v", stream)
			}
		})
	}
}
