package datadog

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestNewWithOptions(t *testing.T) {
	p, err := New(
		WithAddress("127.0.0.1:8125"),
		WithNamespace("testns"),
		WithBufferSize(1024),
		WithFlushPeriod(50*time.Millisecond),
		WithSampleRate(1.0),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	ctx := context.Background()
	p.Counter(ctx, "c_total", 1, map[string]string{"a": "1"})
	p.Histogram(ctx, "h_seconds", 0.42, nil)
	p.Gauge(ctx, "g", 7, nil)
	p.GaugeAdd(ctx, "inflight", 1, nil)
	p.GaugeAdd(ctx, "inflight", -1, nil)
}

// TestSendsDogStatsD listens on a local UDP socket and asserts the wire
// format: Gauge emits the gauge type, GaugeAdd and Counter emit the count
// type, and Counter rounds instead of truncating.
func TestSendsDogStatsD(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer conn.Close()

	p, err := New(
		WithAddress(conn.LocalAddr().String()),
		WithFlushPeriod(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer p.Close()

	ctx := context.Background()
	p.Gauge(ctx, "g", 7, nil)
	p.GaugeAdd(ctx, "inflight", 2, nil)
	p.Counter(ctx, "c", 1.6, nil) // Round(1.6) = 2

	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var got strings.Builder
	for {
		buf := make([]byte, 1024)
		n, _, err := conn.ReadFrom(buf)
		if err != nil {
			break // read deadline
		}
		got.Write(buf[:n])
		s := got.String()
		if strings.Contains(s, "g:7|g") &&
			strings.Contains(s, "inflight:2|c") &&
			strings.Contains(s, "c:2|c") {
			return
		}
	}
	t.Fatalf("DogStatsD payloads = %q, want gauge g:7|g, count inflight:2|c, count c:2|c", got.String())
}
