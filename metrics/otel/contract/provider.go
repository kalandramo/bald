// provider.go —— 契约 instrumentation.otlp 子段的段驱动装配。
//
// 接入形态：
//
//	appkit.MustServe(ctx,
//	    std.Standard(),
//	    otelcontract.AppProvider(),          // instrumentation.otlp → 资源面
//	    ...)
//	// 消费侧：m, _ := otelcontract.Otlp.Resource(app)
package contract

import (
	"context"

	otelmetrics "github.com/kalandramo/bald/metrics/otel"
	"github.com/kalandramo/bald/pkg/appkit"
)

// Otlp 是 instrumentation.otlp 资源的类型化句柄（具体类型，同时满足
// metrics.Metrics 与 metrics.Closer）。
var Otlp = appkit.NewKey[*otelmetrics.Provider]("instrumentation.otlp")

// AppProvider 返回消费 instrumentation.otlp 段的 appkit.Provider：段缺失即被
// 引擎跳过，段存在即构造并落资源面；cleanup 冲刷并关闭 exporter。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 instrumentation.otlp 子段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"instrumentation.otlp"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点（Priority 沿用 PriorityMetrics）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "instrumentation.otlp",
		Segments:   []string{"instrumentation.otlp"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityMetrics,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:instrumentation-otlp",
	}}, nil
}

// Build 按契约子段构造后端；service_name/service_version 留空时回落到
// app.name/app.version，回落口径见 contract.go。
func (appProvider) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	sec := s.Config.GetInstrumentation().GetOtlp()
	p, cleanup, err := Provider(sec, s.Config.GetApp().GetName(), s.Config.GetApp().GetVersion())
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"otlp": p}, adaptCleanup(cleanup), nil
}

// adaptCleanup 把既有无参清理闭包适配为 appkit.Cleanup（nil 透传：无资源）。
func adaptCleanup(cleanup func()) appkit.Cleanup {
	if cleanup == nil {
		return nil
	}
	return func(context.Context) error { cleanup(); return nil }
}

var _ appkit.Provider = appProvider{}
