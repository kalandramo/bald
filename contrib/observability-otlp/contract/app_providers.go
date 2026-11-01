// app_providers.go 提供开箱的 tracer / metrics 段 Provider：应用不再需要
// 自写 type 分派样板。
//
//	appkit.MustServe(ctx, std.Standard(), otlpcontract.AppProviders("my-app"), ...)
//
// tracer 与 metrics 是两个独立节点（引擎要求每个 Provider 恰好产出一个节点），
// 故 AppProviders 返回打包两者的 Option。应用仍可按需只取其一，或自写
// Provider 覆盖（段路径末者胜）。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProviders 返回绑定 serviceName 的 tracer / metrics 段 Provider 集合。
//
// Build 内按契约 type 单选分发——tracer.type=otlp 走 NewTracerProvider，
// metrics.type ∈ {prometheus, otlp} 走 NewPrometheusProvider / NewOTLPProvider；
// 未知 type 与端点矛盾声明均 fail-fast（错误语义与函数级构造函数一致）。
// 生命周期与停机序（tracer 在 logger/metrics 之后、metrics 在 logger 之后
// flush）随节点声明交给引擎。
func AppProviders(serviceName string) appkit.Option {
	return appkit.Providers(
		tracerAppProvider{serviceName: serviceName},
		metricsAppProvider{serviceName: serviceName},
	)
}

// tracerAppProvider 消费 tracer 段。
type tracerAppProvider struct{ serviceName string }

func (tracerAppProvider) Segments() []string { return []string{"tracer"} }

func (p tracerAppProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

func (tracerAppProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:           "tracer",
		Kind:          "tracer",
		Segments:      []string{"tracer"},
		Phase:         appkit.PhaseBootstrap,
		Priority:      appkit.PriorityTracer,
		TeardownAfter: []appkit.ResourceKey{"logger", "metrics"},
		EffectName:    "appkit:tracer-shutdown",
		Reload:        appkit.ReloadRestart,
	}}, nil
}

func (p tracerAppProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	tc := s.Config.GetTracer()
	if tc == nil {
		return nil, nil, nil // 段缺省：不装配
	}
	if tc.GetType() != TracerType {
		return nil, nil, fmt.Errorf("tracer: unknown tracer.type %q (registered: %q)", tc.GetType(), TracerType)
	}
	shutdown, err := NewTracerProvider(p.serviceName)(ctx, tc)
	if err != nil {
		return nil, nil, fmt.Errorf("tracer: build otlp backend: %w", err)
	}
	return nil, func(ctx context.Context) error {
		if shutdown == nil {
			return nil
		}
		return shutdown(ctx)
	}, nil
}

// metricsAppProvider 消费 metrics 段。
type metricsAppProvider struct{ serviceName string }

func (metricsAppProvider) Segments() []string { return []string{"metrics"} }

func (p metricsAppProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

func (metricsAppProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:           "metrics",
		Kind:          "metrics",
		Segments:      []string{"metrics"},
		Phase:         appkit.PhaseBootstrap,
		Priority:      appkit.PriorityMetrics,
		TeardownAfter: []appkit.ResourceKey{"logger"},
		EffectName:    "appkit:metrics-server",
		Reload:        appkit.ReloadRestart,
	}}, nil
}

func (p metricsAppProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	mc := s.Config.GetMetrics()
	if mc == nil {
		return nil, nil, nil // 段缺省：不装配
	}
	var build appkit.MetricsProvider
	switch mc.GetType() {
	case TypePrometheus:
		build = NewPrometheusProvider(p.serviceName)
	case TypeOTLP:
		build = NewOTLPProvider(p.serviceName)
	default:
		return nil, nil, fmt.Errorf("metrics: unknown metrics.type %q (registered: %q, %q)",
			mc.GetType(), TypePrometheus, TypeOTLP)
	}
	exposure, flush, err := build(ctx, mc)
	if err != nil {
		return nil, nil, fmt.Errorf("metrics: build %s backend: %w", mc.GetType(), err)
	}
	return exposure, func(ctx context.Context) error {
		if flush == nil {
			return nil
		}
		return flush(ctx)
	}, nil
}
