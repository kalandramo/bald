// observability.go 实现可观测性（tracer / metrics 契约段）的装配注册表。
//
// 与 RegistrarRegistry 同模式：显式注册（不用 init()+blank import，主程序
// 在 main() 里逐个 MustRegister）；段存在但未注册 fail-fast；停机句柄挂
// Effect 逆序回放。tracer / metrics 段均按段内 type **单选**分发（契约语义
// 就是单选：tracer.type ∈ {"otlp"}，metrics.type ∈ {"prometheus","otlp"}）。
//
// 段缺省语义（契约级，框架保守缺省）：
//   - tracer 段缺省 → no-op trace（otel 全局默认，零配置可运行）；
//   - metrics 段缺省 → 不装配（无暴露端点）。业务要「缺省仍暴露 :9091」
//     （go-bald-admin T9 文档语义）时自行合成默认段再 Build。
//
// 生命周期：阶段 B（BeforeStart，契约装载校验后）Build——必须晚于配置装载
// （值来自契约），早于 servers 监听（BeforeStart 语义保证），使 Recorder /
// tracer 中间件在首个请求前接入全局 Provider。两段均不支持热更新
// （exporter/Provider 重建侵入大，与 registry 段同款决策）。
//
// 用法（FromBootstrap 路径）：
//
//	tr := appkit.NewTracerRegistry()
//	tr.MustRegister(otlpcontract.TracerType, otlpcontract.NewTracerProvider("my-app"))
//	mr := appkit.NewMetricsRegistry()
//	mr.MustRegister(otlpcontract.TypePrometheus, otlpcontract.NewPrometheusProvider("my-app"))
//	mr.MustRegister(otlpcontract.TypeOTLP, otlpcontract.NewOTLPProvider("my-app"))
//	app, err := appkit.FromBootstrap(cfg,
//	    appkit.WithTracerRegistry(tr),
//	    appkit.WithMetricsRegistry(mr), ...)
//
// New 构造路径（T7 模式）在业务 BeforeStart 里直接调 Build，停机自行挂接
// （见 go-bald-admin main.go setupObservability）。
package appkit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/log"
)

// ---------------------------------------------------------------------------
// Tracer
// ---------------------------------------------------------------------------

// TracerProvider 按契约 Tracer 段装配全局 trace 后端。
// 返回 shutdown（flush 缓冲 span，进程退出前调用），由 FromBootstrap 挂
// 停机 Effect 逆序回放（最后执行，收集全部尾批 span）。未开启远端的实现
// 返回 no-op shutdown。
type TracerProvider func(ctx context.Context, cfg *bootstrapv1.Tracer) (func(context.Context) error, error)

// TracerRegistry 是 trace 后端 Provider 的显式注册表（tracer.type 单选）。
// 实例非并发安全（main 装配期使用）；Build 线程安全。
type TracerRegistry struct {
	mu        sync.Mutex
	providers map[string]TracerProvider
}

// NewTracerRegistry 创建空注册表。
func NewTracerRegistry() *TracerRegistry {
	return &TracerRegistry{providers: make(map[string]TracerProvider)}
}

// Register 注册一个 trace 后端 Provider；重名/空名/nil 均 fail-fast。
func (r *TracerRegistry) Register(typ string, p TracerProvider) error {
	if typ == "" {
		return errors.New("appkit: tracer type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: tracer provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]TracerProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: tracer provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册 trace 后端 Provider，失败 panic（装配期错误应当炸在启动时）。
func (r *TracerRegistry) MustRegister(typ string, p TracerProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// Build 按契约 tracer.type 单选分发。段为 nil → no-op shutdown（缺省语义）；
// 段存在但 type 为空/未注册均 fail-fast（显式主开关契约：行为由声明决定）。
func (r *TracerRegistry) Build(ctx context.Context, cfg *bootstrapv1.Tracer) (func(context.Context) error, error) {
	if cfg == nil {
		return func(context.Context) error { return nil }, nil // 段缺省：no-op trace
	}
	typ := cfg.GetType()
	if typ == "" {
		return nil, errors.New(`appkit: tracer.type is required when tracer section is present (expect "otlp")`)
	}
	r.mu.Lock()
	p, ok := r.providers[typ]
	r.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("appkit: tracer provider %q not registered (import the backend contract package and MustRegister it)", typ)
	}
	return p(ctx, cfg)
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

// MetricsProvider 按契约 Metrics 段装配指标后端。
// 返回 (handler, shutdown)：handler 是 Prometheus 抓取端（挂独立端口，
// 由 StartMetricsServer / FromBootstrap 起 http.Server）；shutdown flush
// OTLP 直推通道并释放 MeterProvider（进程退出前调用）。
type MetricsProvider func(ctx context.Context, cfg *bootstrapv1.Metrics) (http.Handler, func(context.Context) error, error)

// MetricsRegistry 是指标后端 Provider 的显式注册表（metrics.type 单选：
// "prometheus"=仅本地抓取，"otlp"=抓取+直推双通道——同一后端实现的两种
// 声明形态，由业务分别注册）。
type MetricsRegistry struct {
	mu        sync.Mutex
	providers map[string]MetricsProvider
}

// NewMetricsRegistry 创建空注册表。
func NewMetricsRegistry() *MetricsRegistry {
	return &MetricsRegistry{providers: make(map[string]MetricsProvider)}
}

// Register 注册一个指标后端 Provider；重名/空名/nil 均 fail-fast。
func (r *MetricsRegistry) Register(typ string, p MetricsProvider) error {
	if typ == "" {
		return errors.New("appkit: metrics type is empty")
	}
	if p == nil {
		return fmt.Errorf("appkit: metrics provider for %q is nil", typ)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.providers == nil {
		r.providers = make(map[string]MetricsProvider)
	}
	if _, dup := r.providers[typ]; dup {
		return fmt.Errorf("appkit: metrics provider %q already registered", typ)
	}
	r.providers[typ] = p
	return nil
}

// MustRegister 注册指标后端 Provider，失败 panic。
func (r *MetricsRegistry) MustRegister(typ string, p MetricsProvider) {
	if err := r.Register(typ, p); err != nil {
		panic(err)
	}
}

// Build 按契约 metrics.type 单选分发。段为 nil → 不装配（nil, nil, nil）；
// 段存在但 type 为空/未注册均 fail-fast（与 TracerRegistry 同款主开关语义）。
func (r *MetricsRegistry) Build(ctx context.Context, cfg *bootstrapv1.Metrics) (http.Handler, func(context.Context) error, error) {
	if cfg == nil {
		return nil, nil, nil // 段缺省：不装配
	}
	typ := cfg.GetType()
	if typ == "" {
		return nil, nil, errors.New(`appkit: metrics.type is required when metrics section is present (expect "prometheus" or "otlp")`)
	}
	r.mu.Lock()
	p, ok := r.providers[typ]
	r.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("appkit: metrics provider %q not registered (import the backend contract package and MustRegister it)", typ)
	}
	return p(ctx, cfg)
}

// ---------------------------------------------------------------------------
// 暴露端 HTTP 生命周期
// ---------------------------------------------------------------------------

// 指标暴露端缺省值（T8 根治：与 gRPC :9090 错峰）。
const (
	defaultMetricsAddr = ":9091"
	defaultMetricsPath = "/metrics"
)

// StartMetricsServer 在独立端口起指标暴露端（Prometheus 抓取），返回
// *http.Server 供停机 Shutdown（与业务 server 生命周期解耦的独立监听面）。
// addr / path 空串走缺省（:9091 / /metrics）。监听失败异步记日志不 panic
// （旁路语义，与审计/指标一致——指标面挂了不该拖垮业务）。
func StartMetricsServer(addr, path string, h http.Handler) *http.Server {
	if addr == "" {
		addr = defaultMetricsAddr
	}
	if path == "" {
		path = defaultMetricsPath
	}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := &http.Server{Addr: addr, Handler: mux}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(context.Background(), "metrics server stopped", "addr", addr, "error", err.Error())
		}
	}()
	return srv
}

// ---------------------------------------------------------------------------
// FromBootstrap 集成（阶段 B 构建 + Effect 回放）
// ---------------------------------------------------------------------------

// observabilityState 承载阶段 B（BeforeStart）装配、停机期消费的句柄
// （构造期零值 + 运行期接线，T7 regCleanup 同模式）。
type observabilityState struct {
	traceShutdown func(context.Context) error
	metricsSrv    *http.Server
	metricsFlush  func(context.Context) error
}

// buildObservability 在阶段 B（契约装载校验后）按 tracer / metrics 段装配
// 可观测性：tracer Build 设全局 Provider；metrics Build 产出抓取端 handler，
// 经 StartMetricsServer 起独立端口。段缺失为 no-op；段存在但未接对应
// Registry 则 fail-fast（配置意图无法兑现）。返回停机句柄，由 FromBootstrap
// 挂 Effect（注册序早于 bootstrap-logger → 逆序回放最后执行：所有组件
// cleanup 完成后再 flush 尾批指标与 span）。
func buildObservability(cfg *bootstrapv1.BootstrapConfig, spec *bootstrapSpec) (*observabilityState, error) {
	st := &observabilityState{}

	if tcfg := cfg.GetTracer(); tcfg != nil {
		if spec.tracerRegistry == nil {
			return nil, errors.New("appkit: bootstrap.tracer present but no TracerRegistry wired (WithTracerRegistry missing)")
		}
		sd, err := spec.tracerRegistry.Build(context.Background(), tcfg)
		if err != nil {
			return nil, fmt.Errorf("appkit: build tracer: %w", err)
		}
		st.traceShutdown = sd
	}

	if mcfg := cfg.GetMetrics(); mcfg != nil {
		if spec.metricsRegistry == nil {
			return nil, errors.New("appkit: bootstrap.metrics present but no MetricsRegistry wired (WithMetricsRegistry missing)")
		}
		h, flush, err := spec.metricsRegistry.Build(context.Background(), mcfg)
		if err != nil {
			return nil, fmt.Errorf("appkit: build metrics: %w", err)
		}
		st.metricsSrv = StartMetricsServer(
			mcfg.GetPrometheus().GetAddr(),
			mcfg.GetPrometheus().GetPath(),
			h,
		)
		st.metricsFlush = flush
	}
	return st, nil
}
