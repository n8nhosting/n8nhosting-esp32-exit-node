package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The final Read returns bytes and the original error after the real context
// deadline has expired, making the coincidence deterministic without sockets
// or weakening TLS verification.
type measureDeadlineReader struct {
	ctx       context.Context
	remaining int
	failure   error
	wait      bool
}

func (r *measureDeadlineReader) Read(p []byte) (int, error) {
	n := min(len(p), r.remaining)
	r.remaining -= n
	if r.remaining == 0 {
		if r.wait {
			<-r.ctx.Done()
		}
		return n, r.failure
	}
	return n, nil
}

func TestMeasureBodyDeadlineDoesNotHideUnrelatedReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name        string
		failure     error
		wantSuccess bool
	}{
		{"expected_deadline", context.DeadlineExceeded, true},
		{"wrapped_deadline", fmt.Errorf("read stopped: %w", context.DeadlineExceeded), true},
		{"wrapped_http_cancel_at_deadline", &url.Error{Op: "Get", URL: "https://speedtest.invalid", Err: context.Canceled}, true},
		{"unexpected_eof", io.ErrUnexpectedEOF, false},
		{"wrapped_unexpected_eof", fmt.Errorf("body failed: %w", io.ErrUnexpectedEOF), false},
		{"unrelated_network_failure", io.ErrClosedPipe, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			mbps, n, err := measureResponseBody(ctx, &measureDeadlineReader{ctx: ctx, remaining: measureMinBytes, failure: tc.failure, wait: true}, measureMaxBytes)
			if ctx.Err() != context.DeadlineExceeded || n != measureMinBytes {
				t.Fatalf("deadline/partial setup failed: ctx=%v bytes=%d", ctx.Err(), n)
			}
			if tc.wantSuccess {
				if err != nil || mbps <= 0 {
					t.Fatalf("valid deadline partial rejected: mbps=%v err=%v", mbps, err)
				}
			} else if err == nil || !errors.Is(err, tc.failure) {
				t.Fatalf("unrelated body error swallowed: %v", err)
			}
		})
	}
}

func TestMeasureBodyRequiresOwnDeadlineAndMeaningfulData(t *testing.T) {
	mbps, n, err := measureResponseBody(context.Background(), &measureDeadlineReader{remaining: measureMinBytes, failure: context.DeadlineExceeded}, measureMaxBytes)
	if err == nil || n != measureMinBytes || mbps != 0 {
		t.Fatalf("foreign deadline accepted: mbps=%v bytes=%d err=%v", mbps, n, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, n, err = measureResponseBody(ctx, &measureDeadlineReader{ctx: ctx, remaining: measureMinBytes, failure: context.Canceled}, measureMaxBytes)
	if err == nil || n != measureMinBytes {
		t.Fatalf("client cancellation accepted: bytes=%d err=%v", n, err)
	}
	deadline, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	_, n, err = measureResponseBody(deadline, &measureDeadlineReader{ctx: deadline, remaining: measureMinBytes / 2, failure: context.DeadlineExceeded, wait: true}, measureMaxBytes)
	if err == nil || !strings.Contains(err.Error(), "too little data") || n != measureMinBytes/2 {
		t.Fatalf("deadline bypassed minimum data: bytes=%d err=%v", n, err)
	}
}

func TestMeasureRolloverCoincidentDeadlinePreservesBodyFailure(t *testing.T) {
	for _, tc := range []struct {
		name         string
		failure      error
		wantComplete bool
	}{
		{"unexpected_eof", io.ErrUnexpectedEOF, false},
		{"wrapped_unexpected_eof", fmt.Errorf("body failed: %w", io.ErrUnexpectedEOF), false},
		{"unrelated_network_failure", io.ErrClosedPipe, false},
		{"expected_partial_deadline", fmt.Errorf("read stopped: %w", context.DeadlineExceeded), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			calls := 0
			download := func(ctx context.Context, _ string, _ string, _ int64) (float64, int64, error) {
				calls++
				switch calls {
				case 1:
					return 1, measureMinBytes, nil
				case 2:
					return 0, 0, rolloverConnectError()
				default:
					<-ctx.Done()
					return 0, measureMinBytes, tc.failure
				}
			}
			r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
			stream := r.Streams[0]
			if r.Complete != tc.wantComplete || stream.Completed != tc.wantComplete || stream.Bytes != 2*measureMinBytes || stream.Downloads != 1 || stream.Retries != 1 || stream.Connect503s != 1 || calls != 3 {
				t.Fatalf("coincident deadline result: %+v calls=%d", r, calls)
			}
			if tc.wantComplete {
				if stream.Error != "" || stream.StopReason != "duration_elapsed" {
					t.Fatalf("true deadline partial failed: %+v", stream)
				}
			} else if stream.Error == "" || stream.StopReason != "error" || !strings.Contains(stream.Error, tc.failure.Error()) {
				t.Fatalf("original failure missing: %+v", stream)
			}
		})
	}
}

func TestMeasureRepeatedCoincidentDeadlineWithoutRetryPreservesFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	calls := 0
	download := func(ctx context.Context, _ string, _ string, _ int64) (float64, int64, error) {
		calls++
		if calls == 1 {
			return 1, measureMinBytes, nil
		}
		<-ctx.Done()
		return 0, measureMinBytes, io.ErrUnexpectedEOF
	}
	r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
	if r.Complete || r.Streams[0].Retries != 0 || r.Bytes != 2*measureMinBytes || !strings.Contains(r.Streams[0].Error, "unexpected EOF") {
		t.Fatalf("plain rollover swallowed unrelated read failure: %+v", r)
	}
}

func TestMeasureRepeatedFinalShortDeadlineKeepsAggregateValid(t *testing.T) {
	for _, partial := range []int{0, measureMinBytes / 2} {
		t.Run(fmt.Sprintf("final_bytes_%d", partial), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			calls := 0
			download := func(ctx context.Context, _ string, _ string, _ int64) (float64, int64, error) {
				calls++
				if calls == 1 {
					return 1, measureMinBytes, nil
				}
				return measureResponseBody(ctx, &measureDeadlineReader{ctx: ctx, remaining: partial, failure: context.DeadlineExceeded, wait: true}, measureMaxBytes)
			}
			r := measureDiagnosticUsing(ctx, "http://proxy.invalid", "https://speedtest.invalid/speedtest.bin", 1, true, download)
			stream := r.Streams[0]
			if !r.Complete || stream.Error != "" || stream.StopReason != "duration_elapsed" || stream.Downloads != 1 || r.Bytes != int64(measureMinBytes+partial) || calls != 2 {
				t.Fatalf("meaningful aggregate lost its typed final deadline: %+v calls=%d", r, calls)
			}
		})
	}
}
