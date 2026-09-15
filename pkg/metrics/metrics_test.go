package metrics

import (
	"context"
	"sync"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
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

func (m *memRecorder) RecordActive(context.Context, Event, Transport, int64) {}

func (m *memRecorder) all() []recordCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]recordCall, len(m.records))
	copy(out, m.records)
	return out
}

func TestNopRecorder_Noop(t *testing.T) {
	r := NopRecorder()
	r.Record(context.Background(), Event{Object: "secret", Action: "get"}, TransportGRPC, 0.01)
	r.RecordActive(context.Background(), Event{}, TransportHTTP, 1)
}

func TestOtelRecorder_NoopProviderSafe(t *testing.T) {
	// 无全局 MeterProvider 配置时（no-op），Record/RecordActive 不应 panic。
	r := New("bald/test")
	r.Record(context.Background(), Event{Object: "secret", Action: "get", Result: "allow"}, TransportGRPC, 0.02)
	r.RecordActive(context.Background(), Event{}, TransportHTTP, 1)
}

// withManualProvider 装配 ManualReader 支撑的全局 MeterProvider，返回
// reader 与还原函数（全局位保存恢复——测试不污染其他用例）。
func withManualProvider(t *testing.T) (*sdkmetric.ManualReader, func()) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	old := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	return reader, func() {
		otel.SetMeterProvider(old)
		_ = mp.Shutdown(context.Background())
	}
}

// collectMetrics 收集并展平为 指标名 → instrument 数据 的映射。
func collectMetrics(t *testing.T, reader *sdkmetric.ManualReader) map[string]metricdata.Aggregation {
	t.Helper()
	var data metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &data); err != nil {
		t.Fatalf("collect: %v", err)
	}
	out := make(map[string]metricdata.Aggregation)
	for _, sm := range data.ScopeMetrics {
		for _, m := range sm.Metrics {
			out[m.Name] = m.Data
		}
	}
	return out
}

// attrsOf 取首个数据点的属性集（单序列断言够用）。
func attrsOf(t *testing.T, agg metricdata.Aggregation) attribute.Set {
	t.Helper()
	switch d := agg.(type) {
	case metricdata.Histogram[float64]:
		if len(d.DataPoints) == 0 {
			t.Fatal("no histogram datapoints")
		}
		return d.DataPoints[0].Attributes
	case metricdata.Sum[int64]:
		if len(d.DataPoints) == 0 {
			t.Fatal("no sum datapoints")
		}
		return d.DataPoints[0].Attributes
	}
	t.Fatalf("unexpected aggregation type %T", agg)
	return attribute.Set{}
}

func wantAttr(t *testing.T, set attribute.Set, key, val string) {
	t.Helper()
	got, ok := set.Value(attribute.Key(key))
	// Emit 输出值的字符串形式（int 200 → "200"），兼容 string/int 属性。
	if !ok || got.Emit() != val {
		t.Errorf("attr %s: want %q, got %q (found=%v)", key, val, got.Emit(), ok)
	}
}

// TestOtelRecorder_SemconvEmit 验证 semconv v1.43.0 对齐：指标名、属性集、
// 单位语义（HTTP/gRPC duration 均 s；gRPC status 为 string 形态）。
func TestOtelRecorder_SemconvEmit(t *testing.T) {
	reader, restore := withManualProvider(t)
	defer restore()

	r := New("bald/test")
	ctx := context.Background()

	// HTTP：成功请求。
	r.Record(ctx, Event{
		Object: "secret", Action: "get", Result: "allow",
		Request: RequestInfo{
			Method: "GET", Scheme: "http", Template: "/v1/secret/:id",
			StatusCode: 200, ServerAddr: "api.example.com", ServerPort: 8080,
		},
	}, TransportHTTP, 0.042)

	// gRPC：PermissionDenied（codes.Code(7)）。
	r.Record(ctx, Event{
		Object: "secret", Action: "get", Result: "deny",
		Request: RequestInfo{
			Method:     "/pkg.v1.SecretService/GetSecret",
			StatusCode: 7,
		},
	}, TransportGRPC, 0.011)

	// active ±1（净 0）。
	r.RecordActive(ctx, Event{Request: RequestInfo{Scheme: "http", ServerAddr: "api.example.com", ServerPort: 8080}}, TransportHTTP, 1)
	r.RecordActive(ctx, Event{Request: RequestInfo{Scheme: "http", ServerAddr: "api.example.com", ServerPort: 8080}}, TransportHTTP, -1)

	metrics := collectMetrics(t, reader)

	// http.server.request.duration：属性与值。
	httpDur, ok := metrics["http.server.request.duration"]
	if !ok {
		t.Fatalf("http.server.request.duration not emitted; got %v", metricNames(metrics))
	}
	hd, ok := httpDur.(metricdata.Histogram[float64])
	if !ok || len(hd.DataPoints) == 0 {
		t.Fatalf("http duration bad aggregation: %T", httpDur)
	}
	if hd.DataPoints[0].Sum != 0.042 {
		t.Errorf("http duration sum: want 0.042, got %v", hd.DataPoints[0].Sum)
	}
	ha := hd.DataPoints[0].Attributes
	wantAttr(t, ha, "http.request.method", "GET")
	wantAttr(t, ha, "url.scheme", "http")
	wantAttr(t, ha, "url.template", "/v1/secret/:id")
	wantAttr(t, ha, "http.response.status_code", "200")

	// rpc.server.call.duration：属性与值（单位 s，不乘 1000）。
	rpcDur, ok := metrics["rpc.server.call.duration"]
	if !ok {
		t.Fatalf("rpc.server.call.duration not emitted; got %v", metricNames(metrics))
	}
	rd, ok := rpcDur.(metricdata.Histogram[float64])
	if !ok || len(rd.DataPoints) == 0 {
		t.Fatalf("rpc duration bad aggregation: %T", rpcDur)
	}
	if rd.DataPoints[0].Sum != 0.011 {
		t.Errorf("rpc duration sum: want 0.011, got %v", rd.DataPoints[0].Sum)
	}
	ra := rd.DataPoints[0].Attributes
	wantAttr(t, ra, "rpc.system.name", "grpc")
	wantAttr(t, ra, "rpc.method", "/pkg.v1.SecretService/GetSecret")
	// codes.Code.String() 输出驼峰（"PermissionDenied"）——与 otelgrpc 惯例一致。
	wantAttr(t, ra, "rpc.response.status_code", "PermissionDenied")

	// http.server.active_requests：±1 后净 0，属性带 scheme/server。
	active, ok := metrics["http.server.active_requests"]
	if !ok {
		t.Fatalf("http.server.active_requests not emitted; got %v", metricNames(metrics))
	}
	ad, ok := active.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("active bad aggregation: %T", active)
	}
	var activeSum int64
	for _, dp := range ad.DataPoints {
		activeSum += dp.Value
	}
	if activeSum != 0 {
		t.Errorf("active requests net: want 0, got %d", activeSum)
	}
	if len(ad.DataPoints) > 0 {
		aa := ad.DataPoints[0].Attributes
		wantAttr(t, aa, "url.scheme", "http")
		wantAttr(t, aa, "server.address", "api.example.com")
		wantAttr(t, aa, "server.port", "8080")
	}

	// bald_audit_events_total：正交业务维度，两条事件两个序列。
	audit, ok := metrics["bald_audit_events_total"]
	if !ok {
		t.Fatalf("bald_audit_events_total not emitted; got %v", metricNames(metrics))
	}
	au, ok := audit.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("audit bad aggregation: %T", audit)
	}
	if len(au.DataPoints) != 2 {
		t.Fatalf("audit events: want 2 series (http+grpc), got %d", len(au.DataPoints))
	}
	for _, dp := range au.DataPoints {
		if dp.Value != 1 {
			t.Errorf("audit count: want 1, got %d", dp.Value)
		}
	}
	// 两条序列的属性各自完整（transport/object/action/result）。
	seen := map[string]bool{}
	for _, dp := range au.DataPoints {
		for _, k := range []string{"transport", "object", "action", "result"} {
			if _, ok := dp.Attributes.Value(attribute.Key(k)); !ok {
				t.Errorf("audit series missing attr %q", k)
			}
		}
		tr, _ := dp.Attributes.Value(attribute.Key("transport"))
		seen[tr.AsString()] = true
	}
	if !seen["http"] || !seen["grpc"] {
		t.Errorf("audit series transports: want http+grpc, got %v", seen)
	}
}

// TestOtelRecorder_GrpcStatusCodeUnknown 验证未知码回退不 panic。
func TestOtelRecorder_GrpcStatusCodeUnknown(t *testing.T) {
	if got := grpcStatusCode(99); got == "" {
		t.Fatal("unknown grpc code must not return empty string")
	}
	if got := grpcStatusCode(0); got != "OK" {
		t.Errorf("code 0: want OK, got %q", got)
	}
}

func metricNames(m map[string]metricdata.Aggregation) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	return names
}
