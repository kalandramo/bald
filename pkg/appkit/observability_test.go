// observability_test.go —— tracer/metrics 段装配契约（批 2 W2b 迁移：原
// TracerRegistry/MetricsRegistry 的注册表单测随注册表删除——type 单选分发
// 收进用户侧 obsProvider；本文件保留 StartMetricsServer 工具契约与引擎
// cleanup 槽位的编排断言）。
package appkit

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// TestAssemble_ObservabilityProviderLifecycle 用户 obsProvider（tracer +
// metrics 双段）经引擎编排：Build 拿到契约段、cleanup 进 canonical 槽位、
// 停机逆序回放兑现（tracer-shutdown / metrics-server Effect 名保持）。
func TestAssemble_ObservabilityProviderLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap() // 预填 server 段豁免（无能力 ⇒ 不绑端口，L3 不报错）
	cfg.Tracer = &bootstrapv1.Tracer{Type: "otlp"}
	cfg.Metrics = &bootstrapv1.Metrics{Type: "prometheus"}

	var (
		tracerSD, metricsFlush atomic.Bool
	)
	obs := segProvider{segs: []string{"tracer", "metrics"}, build: func(ctx context.Context, s BuildScope) (any, Cleanup, error) {
		if s.Config.GetTracer() == nil || s.Config.GetMetrics() == nil {
			t.Fatal("obs provider must see both contract sections")
		}
		// metrics：暴露端真起独立端口（StartMetricsServer 工具契约）。
		srv := StartMetricsServer("127.0.0.1:0", "/metrics", http.NotFoundHandler())
		return nil, func(context.Context) error {
			_ = srv.Shutdown(context.Background())
			metricsFlush.Store(true)
			return nil
		}, nil
	}}
	// tracer 单独一个 Provider：验证同段多路径并存与独立 cleanup。
	tracer := segProvider{segs: []string{"tracer"}, build: func(context.Context, BuildScope) (any, Cleanup, error) {
		return nil, func(context.Context) error { tracerSD.Store(true); return nil }, nil
	}}

	a := assembleForTest(t, cfg, ConfigNamespace(testConfigNamespace), obs, tracer)
	runBriefly(t, a, 50*time.Millisecond)

	if !tracerSD.Load() {
		t.Fatal("tracer cleanup not invoked on stop (tracer-shutdown slot)")
	}
	if !metricsFlush.Load() {
		t.Fatal("metrics cleanup not invoked on stop (metrics-server slot)")
	}
}

func TestStartMetricsServer(t *testing.T) {
	// 占随机端口（关闭后复用，竞态窗口可忽略）。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("metrics-stub"))
	})
	srv := StartMetricsServer(addr, "/custom-metrics", handler)
	defer func() { _ = srv.Shutdown(context.Background()) }()

	deadline := time.Now().Add(2 * time.Second)
	var resp *http.Response
	for {
		resp, err = http.Get("http://" + addr + "/custom-metrics")
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}

	// 默认值：空 addr/path 走 :9091 / /metrics（仅断言常量，不起真端口避冲突）。
	if defaultMetricsAddr != ":9091" || defaultMetricsPath != "/metrics" {
		t.Fatalf("defaults changed: %q %q", defaultMetricsAddr, defaultMetricsPath)
	}
}
