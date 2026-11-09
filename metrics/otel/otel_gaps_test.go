package otel

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	colmetricpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
)

// 六个公开选项逐一写入 config，防止选项与字段错接。
func TestPublicOptions_ApplyToConfig(t *testing.T) {
	cfg := defaultConfig()
	WithEndpoint("collector:4317")(cfg)
	WithServiceName("svc")(cfg)
	WithServiceVersion("v9.9.9")(cfg)
	WithInsecure(true)(cfg)
	WithHTTP(true)(cfg)
	WithExportInterval(3 * time.Second)(cfg)

	if cfg.endpoint != "collector:4317" {
		t.Errorf("endpoint = %q", cfg.endpoint)
	}
	if cfg.serviceName != "svc" {
		t.Errorf("serviceName = %q", cfg.serviceName)
	}
	if cfg.serviceVersion != "v9.9.9" {
		t.Errorf("serviceVersion = %q", cfg.serviceVersion)
	}
	if !cfg.insecure {
		t.Error("insecure = false, want true")
	}
	if !cfg.useHTTP {
		t.Error("useHTTP = false, want true")
	}
	if cfg.exportInterval != 3*time.Second {
		t.Errorf("exportInterval = %v", cfg.exportInterval)
	}
}

// 默认值是 OTLP/gRPC 到 localhost:4317、TLS 开启、60s 导出间隔。
func TestDefaultConfig(t *testing.T) {
	cfg := defaultConfig()
	if cfg.endpoint != "localhost:4317" || cfg.serviceName != "bald-app" ||
		cfg.serviceVersion != "v0.0.1" || cfg.insecure || cfg.useHTTP ||
		cfg.exportInterval != 60*time.Second || cfg.reader != nil {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
}

// HTTP 协议 + insecure 走 otlpmetrichttp 分支；New 同时设置全局
// MeterProvider 并按 service name/version 生成 resource。
func TestNew_HTTPInsecureSetsGlobalProviderAndResource(t *testing.T) {
	prev := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	reader := sdkmetric.NewManualReader()
	p, err := New(
		withReader(reader),
		WithHTTP(true),
		WithInsecure(true),
		WithEndpoint("127.0.0.1:4318"),
		WithServiceName("svc-http"),
		WithServiceVersion("v1.2.3"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.exporter == nil {
		t.Error("exporter is nil")
	}
	if p.mp == nil {
		t.Fatal("mp is nil")
	}
	if got, ok := otel.GetMeterProvider().(*sdkmetric.MeterProvider); !ok || got != p.mp {
		t.Errorf("global MeterProvider = %T %p, want *sdkmetric.MeterProvider %p",
			otel.GetMeterProvider(), otel.GetMeterProvider(), p.mp)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	attrs := rm.Resource.Attributes()
	want := map[attribute.Key]string{
		semconv.ServiceNameKey:    "svc-http",
		semconv.ServiceVersionKey: "v1.2.3",
	}
	for _, kv := range attrs {
		delete(want, kv.Key)
		if kv.Key == semconv.ServiceNameKey && kv.Value.AsString() != "svc-http" {
			t.Errorf("service.name = %q", kv.Value.AsString())
		}
		if kv.Key == semconv.ServiceVersionKey && kv.Value.AsString() != "v1.2.3" {
			t.Errorf("service.version = %q", kv.Value.AsString())
		}
	}
	for k := range want {
		t.Errorf("resource missing attribute %q", k)
	}
}

// 默认 gRPC 分支（含 insecure 追加选项）也能构造成功；Close 关停 provider。
func TestNew_GRPCBranchAndClose(t *testing.T) {
	prev := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	p, err := New(
		withReader(sdkmetric.NewManualReader()),
		WithEndpoint("127.0.0.1:4317"),
		WithInsecure(true),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.exporter == nil {
		t.Error("exporter is nil")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// 无注入 reader 时走 PeriodicReader：起一个进程内 OTLP/gRPC collector，
// 验证指标按导出间隔真实推送、Close 冲刷尾批。
func TestNew_PeriodicReaderExports(t *testing.T) {
	prev := otel.GetMeterProvider()
	t.Cleanup(func() { otel.SetMeterProvider(prev) })

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	collector := &metricCollector{}
	colmetricpb.RegisterMetricsServiceServer(srv, collector)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	p, err := New(
		WithEndpoint(lis.Addr().String()),
		WithInsecure(true),
		WithExportInterval(10*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.Counter(context.Background(), "periodic_total", 1, nil)

	deadline := time.Now().Add(5 * time.Second)
	for collector.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if collector.calls.Load() == 0 {
		t.Fatal("collector received no export before deadline")
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if names := collector.metricNames(); !names["periodic_total"] {
		t.Fatalf("exported metrics = %v, want periodic_total", names)
	}
}

// metricCollector 是进程内 OTLP collector，记录导出次数与指标名。
type metricCollector struct {
	colmetricpb.UnimplementedMetricsServiceServer

	calls atomic.Int32
	mu    sync.Mutex
	names map[string]struct{}
}

func (c *metricCollector) Export(_ context.Context, req *colmetricpb.ExportMetricsServiceRequest) (*colmetricpb.ExportMetricsServiceResponse, error) {
	c.calls.Add(1)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names == nil {
		c.names = make(map[string]struct{})
	}
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				c.names[m.GetName()] = struct{}{}
			}
		}
	}
	return &colmetricpb.ExportMetricsServiceResponse{}, nil
}

func (c *metricCollector) metricNames() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]bool, len(c.names))
	for name := range c.names {
		out[name] = true
	}
	return out
}

// 非法 endpoint 使 exporter 构造失败：gRPC 与 HTTP 两条路径都要返回
// 包装错误且不设置全局 provider。
func TestNew_ExporterCreationErrors(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
	}{
		{"grpc", []Option{WithEndpoint("\x00")}},
		{"http", []Option{WithHTTP(true), WithEndpoint("::bad::")}},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			p, err := New(append(c.opts, withReader(sdkmetric.NewManualReader()))...)
			if err == nil {
				t.Fatalf("expected error, got provider %+v", p)
			}
			if !strings.Contains(err.Error(), "failed to create OTLP metric exporter") {
				t.Errorf("error = %v, want exporter wrap", err)
			}
			if p != nil {
				t.Errorf("provider = %+v, want nil on error", p)
			}
		})
	}
}

// errMeter 让四个同步 instrument 的构造失败，覆盖 Provider 的防御分支。
// 嵌入 metric.Meter 以满足接口的 embedded 部分，仅覆写用到的四个方法。
type errMeter struct {
	metric.Meter
}

var errInstrument = errors.New("instrument unavailable")

func (errMeter) Float64Counter(string, ...metric.Float64CounterOption) (metric.Float64Counter, error) {
	return nil, errInstrument
}

func (errMeter) Float64Histogram(string, ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	return nil, errInstrument
}

func (errMeter) Float64Gauge(string, ...metric.Float64GaugeOption) (metric.Float64Gauge, error) {
	return nil, errInstrument
}

func (errMeter) Float64UpDownCounter(string, ...metric.Float64UpDownCounterOption) (metric.Float64UpDownCounter, error) {
	return nil, errInstrument
}

func newErrProvider() *Provider {
	return &Provider{
		meter:      errMeter{},
		counters:   make(map[string]metric.Float64Counter),
		histograms: make(map[string]metric.Float64Histogram),
		gauges:     make(map[string]metric.Float64Gauge),
		gaugeAdds:  make(map[string]metric.Float64UpDownCounter),
	}
}

// instrument 构造失败时静默丢弃测量，且不缓存半成品。
func TestInstrumentCreationErrors(t *testing.T) {
	p := newErrProvider()
	ctx := context.Background()

	p.Counter(ctx, "c", 1, nil)
	p.Histogram(ctx, "h", 1, nil)
	p.Gauge(ctx, "g", 1, nil)
	p.GaugeAdd(ctx, "ga", 1, nil)

	if len(p.counters) != 0 || len(p.histograms) != 0 ||
		len(p.gauges) != 0 || len(p.gaugeAdds) != 0 {
		t.Fatalf("failed instruments cached: %d/%d/%d/%d",
			len(p.counters), len(p.histograms), len(p.gauges), len(p.gaugeAdds))
	}
}

// 反复使用同一名字只构造一次 instrument（命中缓存分支）。
func TestInstrumentCacheReuse(t *testing.T) {
	p, reader := newTestProvider(t)
	ctx := context.Background()

	p.Counter(ctx, "c", 1, nil)
	p.Histogram(ctx, "h", 1, nil)
	p.Gauge(ctx, "g", 1, nil)
	p.GaugeAdd(ctx, "ga", 1, nil)
	p.Counter(ctx, "c", 1, nil)
	p.Histogram(ctx, "h", 1, nil)
	p.Gauge(ctx, "g", 2, nil)
	p.GaugeAdd(ctx, "ga", 1, nil)

	if len(p.counters) != 1 || len(p.histograms) != 1 ||
		len(p.gauges) != 1 || len(p.gaugeAdds) != 1 {
		t.Fatalf("cache size = %d/%d/%d/%d, want 1 each",
			len(p.counters), len(p.histograms), len(p.gauges), len(p.gaugeAdds))
	}

	ms := collect(t, reader)
	for _, name := range []string{"c", "h", "g", "ga"} {
		findMetric(t, ms, name)
	}
}

// toAttrs: nil / 空 map 生成零属性，按键值全量转换。
func TestToAttrs(t *testing.T) {
	if got := toAttrs(nil); len(got) != 0 {
		t.Errorf("toAttrs(nil) = %v, want empty", got)
	}
	got := toAttrs(map[string]string{"a": "1", "b": "2"})
	if len(got) != 2 {
		t.Fatalf("toAttrs = %v, want 2 attrs", got)
	}
	seen := map[string]string{}
	for _, kv := range got {
		seen[string(kv.Key)] = kv.Value.AsString()
	}
	if seen["a"] != "1" || seen["b"] != "2" {
		t.Errorf("attrs = %v", seen)
	}
}

// 结构性零覆盖（登记备查）：
//   - otel.go:155 resource.New 错误分支不可达。New 只用 WithAttributes 装配
//     service.name/version，未挂任何 detector；resource.Detect 的 error 只可能
//     来自 detector，因此该分支在现有构造下永不触发。
