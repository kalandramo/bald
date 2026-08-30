package metrics

import (
	"context"
	"sync"
	"testing"
)

// memRecorder 是测试用内存 Recorder，记录所有 emit 的指标事件。
type memRecorder struct {
	mu      sync.Mutex
	records []recordCall
}

type recordCall struct {
	ev        Event
	transport Transport
	dur       float64
}

func (m *memRecorder) Record(_ context.Context, ev Event, t Transport, dur float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, recordCall{ev: ev, transport: t, dur: dur})
}

func (m *memRecorder) all() []recordCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]recordCall, len(m.records))
	copy(out, m.records)
	return out
}

func TestNopRecorder_Noop(t *testing.T) {
	NopRecorder().Record(context.Background(), Event{Object: "secret", Action: "get"}, TransportGRPC, 0.01)
}

func TestOtelRecorder_NoopProviderSafe(t *testing.T) {
	// 无全局 MeterProvider 配置时（no-op），Record 不应 panic。
	r := New("bald/test")
	r.Record(context.Background(), Event{Object: "secret", Action: "get", Result: "allow"}, TransportGRPC, 0.02)
}
