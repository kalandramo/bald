package contract

// mapping_offline_test.go —— instrumentation.otlp 六行的**运行期**闸门：契约字段
// 必须能被远端观测面读到，落进 config struct 不算。
//
// 离线观测面有三把，各自判一条轴：
//   - httptest 服务端 = OTLP/HTTP 收集器：判 endpoint / use_http / service_name /
//     service_version / export_interval（POST 的路径与解码后的 protobuf 字节）；
//   - 真 gRPC 收集器（MetricService.Export）：判 use_http=false + insecure=true
//     ——能收到并解析 ExportMetricsServiceRequest 就同时证明了「走 gRPC」和
//     「明文」，比只看连接前缀强；
//   - 裸 TCP 监听 = gRPC 落点：只判 insecure=false。这一格在没有真 CA 的前提下
//     只能看首字节（TLS ClientHello 0x16 0x03 而非 h2 preface）——客户端会因为
//     证书校验失败断开，所以断连本身不是判据，发出的第一个字节才是。
//
// 一条实测口径：otlpmetrichttp 默认 gzip 压缩上报体，所以匹配前先探测 gzip
// magic（0x1f 0x8b）再决定是否解压——不解压直接搜字节会判红，而那看着像
// 「字段没生效」。
//
// 另一条实测口径（决定本文件的窗口形状）：cleanup() 会走 Provider.Close →
// MeterProvider.Shutdown 的末次上报，而**落点不回话时这次上报会一直重试到
// Close 自己的 10s 上限**。同一段代码实测：落点正常应答 cleanup=2ms，明文 gRPC
// 打到只读字节的裸监听 cleanup=10.001s，OTLP/HTTP 打到没人听的端口 cleanup=1ms
// （连接被拒立即返回错误）。所以只有「落点永不应答」那一行天然带 10s，其余行都
// 让落点真应答，把时间花在判据上而不是花在停机上。

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	otelmetrics "github.com/kalandramo/bald/metrics/otel"
	collectormetricsv1 "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
)

// export 是一次上报的观测值。
type export struct {
	method string
	path   string
	body   string // 已解压
}

// newHTTPCollector 起一个假装自己是 OTLP/HTTP collector 的本机服务，返回
// host:port（契约 endpoint 的形状）与上报通道。
func newHTTPCollector(t *testing.T) (string, chan export) {
	t.Helper()
	ch := make(chan export, 256)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			return
		}
		ch <- export{method: r.Method, path: r.URL.Path, body: maybeGunzip(t, raw)}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	// httptest 的地址带 scheme，契约要的是 host:port
	return strings.TrimPrefix(strings.TrimPrefix(srv.URL, "http://"), "https://"), ch
}

func maybeGunzip(t *testing.T, raw []byte) string {
	t.Helper()
	if len(raw) < 2 || raw[0] != 0x1f || raw[1] != 0x8b {
		return string(raw)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("gzip reader: %v（上报体形状变了，判据需要重审）", err)
	}
	defer func() { _ = zr.Close() }()
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip: %v", err)
	}
	return string(out)
}

// awaitExports 在 window 内收满 want 次上报；收不满直接判红并报告已收到几条。
func awaitExports(t *testing.T, ch chan export, want int, window time.Duration) []export {
	t.Helper()
	var got []export
	deadline := time.After(window)
	for len(got) < want {
		select {
		case e := <-ch:
			got = append(got, e)
		case <-deadline:
			t.Fatalf("window %v 内只收到 %d 次上报, want %d", window, len(got), want)
		}
	}
	return got
}

// countExports 在 window 内最多收 want 次：want>0 时收满立刻返回（早退），否则
// 走满整个窗口再返回条数。静默侧判据必须传 want=0 走完窗口——提前返回会让
// 「一条都没等到」和「窗口太短还没等到」分不开。
func countExports(t *testing.T, ch chan export, want int, window time.Duration) int {
	t.Helper()
	n := 0
	deadline := time.After(window)
	for {
		select {
		case <-ch:
			n++
			if want > 0 && n >= want {
				return n
			}
		case <-deadline:
			return n
		}
	}
}

// newGRPCCollector 起一个真的 OTLP/gRPC 收集器（只实现 MetricsService.Export），
// 返回 host:port 与收到并解析后的上报通道。判「走 gRPC 且明文」时，能被 gRPC
// 服务端解析成 ExportMetricsServiceRequest 就是终局证据——裸监听只能看到连接
// 前缀，看不到上报是否成功。
func newGRPCCollector(t *testing.T) (string, chan *collectormetricsv1.ExportMetricsServiceRequest) {
	t.Helper()
	ch := make(chan *collectormetricsv1.ExportMetricsServiceRequest, 64)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	srv := grpc.NewServer()
	collectormetricsv1.RegisterMetricsServiceServer(srv, grpcMetricSink{ch: ch})
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(srv.Stop)
	return ln.Addr().String(), ch
}

// grpcMetricSink 是收集器的服务端实现：收下请求、原样回一个空 OK。
type grpcMetricSink struct {
	collectormetricsv1.UnimplementedMetricsServiceServer
	ch chan *collectormetricsv1.ExportMetricsServiceRequest
}

func (s grpcMetricSink) Export(_ context.Context, req *collectormetricsv1.ExportMetricsServiceRequest) (*collectormetricsv1.ExportMetricsServiceResponse, error) {
	select {
	case s.ch <- req:
	default: // 通道满只影响观测侧，不能让导出失败——上报方会重试，判据不该被它掩盖。
	}
	return &collectormetricsv1.ExportMetricsServiceResponse{}, nil
}

// awaitGRPCExports 在 window 内收满 want 次 gRPC 上报，收不满判红并报告已收到几条。
func awaitGRPCExports(t *testing.T, ch chan *collectormetricsv1.ExportMetricsServiceRequest, want int, window time.Duration) []*collectormetricsv1.ExportMetricsServiceRequest {
	t.Helper()
	var got []*collectormetricsv1.ExportMetricsServiceRequest
	deadline := time.After(window)
	for len(got) < want {
		select {
		case req := <-ch:
			got = append(got, req)
		case <-deadline:
			t.Fatalf("window %v 内只收到 %d 次 gRPC 上报, want %d", window, len(got), want)
		}
	}
	return got
}

// exportedNames 取一次上报里全部指标的名字（resource→scope→metric 三层展开）。
func exportedNames(req *collectormetricsv1.ExportMetricsServiceRequest) []string {
	var names []string
	for _, rm := range req.GetResourceMetrics() {
		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				names = append(names, m.GetName())
			}
		}
	}
	return names
}

// resourceAttr 取 resource 上的字符串属性（service.name / service.version 落点）。
func resourceAttr(req *collectormetricsv1.ExportMetricsServiceRequest, key string) string {
	for _, rm := range req.GetResourceMetrics() {
		for _, kv := range rm.GetResource().GetAttributes() {
			if kv.GetKey() == key {
				return kv.GetValue().GetStringValue()
			}
		}
	}
	return ""
}

// newRawListener 起一个不解析协议的 TCP 监听，把首条连接的前 24 字节送进通道。
// 判 gRPC/TLS 两条轴时，能观察的就是这串前导字节。
func newRawListener(t *testing.T) (string, chan []byte) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan []byte, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer func() { _ = c.Close() }()
				buf := make([]byte, 24)
				n, _ := bufio.NewReader(c).Read(buf)
				if n > 0 {
					ch <- buf[:n]
				}
			}(c)
		}
	}()
	return ln.Addr().String(), ch
}

func mustProvider(t *testing.T, sec *bootstrapv1.Instrumentation_Otlp, appName, appVersion string) (*otelmetrics.Provider, func()) {
	t.Helper()
	p, cleanup, err := Provider(sec, appName, appVersion)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup == nil {
		t.Fatal("cleanup = nil, want non-nil（exporter/MeterProvider 要关）")
	}
	return p, cleanup
}

// endpoint + use_http 行：HTTP 轴的上报真的落到配置的 host:port，且走
// OTLP/HTTP 的标准路径 /v1/metrics。
func TestMappingOffline_EndpointAndUseHTTPLand(t *testing.T) {
	addr, ch := newHTTPCollector(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, UseHttp: true, Insecure: true, ExportIntervalSeconds: 1,
	}, "gate-app", "v1")
	defer cleanup()

	p.Counter(context.Background(), "otel_gate_counter_total", 1, nil)
	e := awaitExports(t, ch, 1, 10*time.Second)[0]
	if e.method != http.MethodPost || e.path != "/v1/metrics" {
		t.Fatalf("上报不是 OTLP/HTTP 的形状: %s %s", e.method, e.path)
	}
	if !strings.Contains(e.body, "otel_gate_counter_total") {
		t.Fatal("上报体里没有本行的指标名（落点或编码不对）")
	}
}

// service_name / service_version 行：两条都作为 resource 属性进 protobuf 正文。
func TestMappingOffline_ServiceIdentityReachesPayload(t *testing.T) {
	addr, ch := newHTTPCollector(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, UseHttp: true, Insecure: true, ExportIntervalSeconds: 1,
		ServiceName: "otlp-gate-svc", ServiceVersion: "9.8.7",
	}, "ignored-app", "v0")
	defer cleanup()

	p.Counter(context.Background(), "otel_gate_id_total", 1, nil)
	body := awaitExports(t, ch, 1, 10*time.Second)[0].body
	if !strings.Contains(body, "otlp-gate-svc") {
		t.Fatal("service_name 没落到 resource 属性上")
	}
	if !strings.Contains(body, "9.8.7") {
		t.Fatal("service_version 没落到 resource 属性上")
	}
	if strings.Contains(body, "ignored-app") {
		t.Fatal("显式 service_name 被应用名覆盖（回落方向反了）")
	}
}

// 回落行：契约两格留空时用 app.name / app.version——回落发生在契约层，
// 不是库哨兵（"bald-app"/"v0.0.1"）。
func TestMappingOffline_AppIdentityFallback(t *testing.T) {
	addr, ch := newHTTPCollector(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, UseHttp: true, Insecure: true, ExportIntervalSeconds: 1,
	}, "fallback-app", "v3.2.1")
	defer cleanup()

	p.Counter(context.Background(), "otel_gate_fb_total", 1, nil)
	body := awaitExports(t, ch, 1, 10*time.Second)[0].body
	if !strings.Contains(body, "fallback-app") || !strings.Contains(body, "v3.2.1") {
		t.Fatal("app.name/app.version 没有作为 service 身份回落进 payload")
	}
	if strings.Contains(body, "bald-app") || strings.Contains(body, "v0.0.1") {
		t.Fatal("产物仍带库哨兵身份（回落没接上）")
	}
}

// export_interval_seconds 行：节拍真的挂到 PeriodicReader 上。
// 两半合起来才排除「字段被忽略」：库默认 60s，配 1s 后 5s 窗口内应到 ≥2 次；
// 不配时 2.5s 窗口内一次都不该到。
func TestMappingOffline_ExportIntervalDrivesCadence(t *testing.T) {
	addrFast, chFast := newHTTPCollector(t)
	pFast, cFast := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addrFast, UseHttp: true, Insecure: true, ExportIntervalSeconds: 1,
	}, "gate-app", "v1")
	defer cFast()
	pFast.Counter(context.Background(), "otel_gate_fast_total", 1, nil)
	if n := countExports(t, chFast, 2, 5*time.Second); n < 2 {
		t.Fatalf("export_interval_seconds=1 时 5s 内只上报 %d 次, want ≥2", n)
	}

	addrSlow, chSlow := newHTTPCollector(t)
	pSlow, cSlow := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addrSlow, UseHttp: true, Insecure: true,
	}, "gate-app", "v1")
	defer cSlow()
	pSlow.Counter(context.Background(), "otel_gate_slow_total", 1, nil)
	if n := countExports(t, chSlow, 0, 2500*time.Millisecond); n != 0 {
		t.Fatalf("未配节拍时 2.5s 内就上报了 %d 次（库默认应为 60s，字段被写死成小值会让这条判绿）", n)
	}
}

// cleanup 行：Close() 触发末次上报，未落地的累加不会随进程消失。
// 落点是会应答的 HTTP 收集器，所以这一行的 cleanup 只花毫秒级——末次上报成功
// 返回，不会撞上 Close 的 10s 上限（见文件头的实测口径）。
func TestMappingOffline_CleanupFlushesLastExport(t *testing.T) {
	addr, ch := newHTTPCollector(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, UseHttp: true, Insecure: true, ExportIntervalSeconds: 60,
	}, "gate-app", "v1")
	p.Counter(context.Background(), "otel_gate_last_total", 1, nil)
	if n := countExports(t, ch, 0, 2*time.Second); n != 0 {
		t.Fatalf("60s 节拍下运行期就上报了 %d 次", n)
	}
	cleanup()
	if n := countExports(t, ch, 1, 10*time.Second); n == 0 {
		t.Fatal("Close() 没有触发末次上报（停机丢数）")
	}
}

// use_http=false + insecure=true 轴：明文 gRPC 落点真的收到一个可被
// MetricsService.Export 解析的上报——协议、明文、指标名三件事一趟判完。
func TestMappingOffline_GRPCInsecureDeliversExportRequest(t *testing.T) {
	addr, ch := newGRPCCollector(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, Insecure: true, ExportIntervalSeconds: 1,
		ServiceName: "otlp-grpc-svc",
	}, "gate-app", "v1")
	defer cleanup()

	p.Counter(context.Background(), "otel_gate_grpc_total", 1, nil)
	req := awaitGRPCExports(t, ch, 1, 10*time.Second)[0]
	if !slices.Contains(exportedNames(req), "otel_gate_grpc_total") {
		t.Fatalf("gRPC 上报里没有本行的指标名: %v", exportedNames(req))
	}
	if got := resourceAttr(req, "service.name"); got != "otlp-grpc-svc" {
		t.Fatalf("resource 属性 service.name = %q, want otlp-grpc-svc（身份没随 gRPC 通道落地）", got)
	}
}

// insecure=false 轴：同一落点上换成 TLS，首字节变成 ClientHello 记录
// （0x16 握手 + 0x03 版本），而不再是 h2 preface——这一格唯一可离线判的形状。
// 本行无法用真收集器代劳：证书校验要能过必须往客户端塞 CA，而根包不暴露 dial
// 选项，所以只能停在字节层，并按文件头的实测口径付掉 cleanup 的 10s。
func TestMappingOffline_GRPCSecureStartsTLS(t *testing.T) {
	addr, ch := newRawListener(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, ExportIntervalSeconds: 1,
	}, "gate-app", "v1")
	defer cleanup()

	p.Counter(context.Background(), "otel_gate_tls_total", 1, nil)
	first := awaitFirstBytes(t, ch, 10*time.Second)
	if bytes.Contains(first, []byte("PRI * HTTP/2.0")) {
		t.Fatal("insecure=false 却发出了明文 h2 preface（TLS 开关没落到传输上）")
	}
	if len(first) < 2 || first[0] != 0x16 || first[1] != 0x03 {
		t.Fatalf("首字节不是 TLS 握手记录（0x16 0x03）: %x", first)
	}
}

func awaitFirstBytes(t *testing.T, ch chan []byte, window time.Duration) []byte {
	t.Helper()
	select {
	case b := <-ch:
		return b
	case <-time.After(window):
		t.Fatalf("window %v 内落点上没有任何连接——endpoint 没落到 exporter 上", window)
		return nil
	}
}

// 必填与越界行：构造期 fail-fast。endpoint 留空是唯一「看起来成功」的漏配
// （库会静默沿用 localhost:4317），所以它必须在建连之前挡住。
func TestMappingOffline_FailFastRows(t *testing.T) {
	addr, _ := newHTTPCollector(t)
	cases := []struct {
		name     string
		sec      *bootstrapv1.Instrumentation_Otlp
		wantSubs string
	}{
		{"nil 子段", nil, "子段为 nil"},
		{"endpoint 必填", &bootstrapv1.Instrumentation_Otlp{}, "endpoint 必填"},
		{"节拍负数", &bootstrapv1.Instrumentation_Otlp{Endpoint: addr, ExportIntervalSeconds: -1}, "export_interval_seconds=-1 非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, cleanup, err := Provider(tc.sec, "app", "v1")
			if err == nil {
				t.Fatalf("err = nil, want 含 %q", tc.wantSubs)
			}
			if !strings.Contains(err.Error(), tc.wantSubs) {
				t.Fatalf("err = %q, want 含 %q", err, tc.wantSubs)
			}
			if p != nil || cleanup != nil {
				t.Fatal("fail-fast 时仍产出了实例/cleanup")
			}
		})
	}
}

// 标签行：labels 进 attribute，落到上报体里（同一趟收包判一次）。
func TestMappingOffline_LabelsReachPayload(t *testing.T) {
	addr, ch := newHTTPCollector(t)
	p, cleanup := mustProvider(t, &bootstrapv1.Instrumentation_Otlp{
		Endpoint: addr, UseHttp: true, Insecure: true, ExportIntervalSeconds: 1,
	}, "gate-app", "v1")
	defer cleanup()

	p.Counter(context.Background(), "otel_gate_lbl_total", 1, map[string]string{"gate_label": "gate_value"})
	body := awaitExports(t, ch, 1, 10*time.Second)[0].body
	if !strings.Contains(body, "gate_label") || !strings.Contains(body, "gate_value") {
		t.Fatal("标签没进上报体的 attribute")
	}
}
