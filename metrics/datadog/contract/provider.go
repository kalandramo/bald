// provider.go —— 契约 instrumentation.datadog 子段的段驱动装配。
//
// 接入形态：
//
//	appkit.MustServe(ctx,
//	    std.Standard(),
//	    ddcontract.AppProvider(),            // instrumentation.datadog → 资源面
//	    ...)
//	// 消费侧：m, _ := ddcontract.Datadog.Resource(app)
package contract

import (
	"context"

	"github.com/kalandramo/bald/metrics/datadog"
	"github.com/kalandramo/bald/pkg/appkit"
)

// Datadog 是 instrumentation.datadog 资源的类型化句柄（具体类型，同时满足
// metrics.Metrics 与 metrics.Closer）。
var Datadog = appkit.NewKey[*datadog.Provider]("instrumentation.datadog")

// AppProvider 返回消费 instrumentation.datadog 段的 appkit.Provider：段缺失
// 即被引擎跳过，段存在即构造并落资源面；cleanup 冲刷并关闭 statsd 连接。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 instrumentation.datadog 子段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"instrumentation.datadog"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点（Priority 沿用 PriorityMetrics）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "instrumentation.datadog",
		Segments:   []string{"instrumentation.datadog"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityMetrics,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:instrumentation-datadog",
	}}, nil
}

// Build 按契约子段构造后端，产物 map[string]any{"datadog": *} 经引擎展开
// 落成 instrumentation.datadog 资源。
func (appProvider) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	sec := s.Config.GetInstrumentation().GetDatadog()
	p, cleanup, err := Provider(sec)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"datadog": p}, appkit.CleanupFrom(cleanup), nil
}

var _ appkit.Provider = appProvider{}
