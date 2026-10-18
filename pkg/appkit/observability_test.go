// observability_test.go —— tracer/metrics 段装配契约（批 2 W2b 迁移；B7 再迁移：
// 暴露端监听面单测随 StartMetricsServer 下沉 observability/metricshttp 包，
// 本文件保留引擎对 MetricsExposure 产物的编排断言——cleanup 槽位合成
// 「先停暴露端再 flush」的停机序）。
package appkit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// fakeExposure 是 MetricsExposure 测试桩：记录 Stop 时序（与 flush 共用一个
// 单调序号器，断言「先停暴露端再 flush」）。
type fakeExposure struct {
	stopped atomic.Bool
	seq     *atomic.Int64
	at      atomic.Int64
}

func (f *fakeExposure) Stop(context.Context) error {
	f.stopped.Store(true)
	if f.seq != nil {
		f.at.Store(f.seq.Add(1))
	}
	return nil
}

// TestAssemble_ObservabilityProviderLifecycle 用户 Provider（tracer + metrics
// 各一段）经引擎编排：Build 拿到契约段、metrics 产物为可关闭暴露端、停机
// 逆序回放兑现且**先停暴露端再 flush**（metrics-server / tracer-shutdown
// Effect 名保持）。
func TestAssemble_ObservabilityProviderLifecycle(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap() // 预填 server 段豁免（无能力 ⇒ 不绑端口，L3 不报错）
	cfg.Tracer = &bootstrapv1.Tracer{Type: "otlp"}
	cfg.Metrics = &bootstrapv1.Metrics{Type: "prometheus"}

	var (
		tracerSD, metricsFlush atomic.Bool
		seq                    atomic.Int64
		flushAt                atomic.Int64
	)
	exp := &fakeExposure{seq: &seq}
	// metrics Provider：产物为暴露端 + flush cleanup（B7 契约形状）。
	metrics := segProvider{segs: []string{"metrics"}, build: func(_ context.Context, s BuildScope) (any, Cleanup, error) {
		if s.Config.GetMetrics() == nil {
			t.Fatal("metrics provider must see the metrics section")
		}
		return exp, func(context.Context) error {
			flushAt.Store(seq.Add(1))
			metricsFlush.Store(true)
			return nil
		}, nil
	}}
	// tracer 单独一个 Provider：验证同段族多 Provider 并存与独立 cleanup。
	tracer := segProvider{segs: []string{"tracer"}, build: func(context.Context, BuildScope) (any, Cleanup, error) {
		return nil, func(context.Context) error { tracerSD.Store(true); return nil }, nil
	}}

	a := assembleForTest(t, cfg, metrics, tracer)
	runBriefly(t, a, 50*time.Millisecond)

	if !tracerSD.Load() {
		t.Fatal("tracer cleanup not invoked on stop (tracer-shutdown slot)")
	}
	if !metricsFlush.Load() {
		t.Fatal("metrics flush not invoked on stop (metrics-server slot)")
	}
	if !exp.stopped.Load() {
		t.Fatal("exposure Stop not invoked on stop (engine-composed stop-then-flush)")
	}
	if exp.at.Load() == 0 || flushAt.Load() == 0 || exp.at.Load() > flushAt.Load() {
		t.Fatalf("stop must happen before flush: stopAt=%d flushAt=%d", exp.at.Load(), flushAt.Load())
	}
}
