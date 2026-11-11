// provider.go —— 契约 instrumentation.prometheus 子段的段驱动装配。
//
// 接入形态：
//
//	appkit.MustServe(ctx,
//	    std.Standard(),
//	    promcontract.AppProvider(),          // instrumentation.prometheus → 资源面
//	    ...)
//	// 消费侧：m, _ := promcontract.Prometheus.Resource(app)
//	// 注入埋点口：tcpserver.WithMetrics(m)
package contract

import (
	"context"

	prom "github.com/kalandramo/bald/metrics/prometheus"
	"github.com/kalandramo/bald/pkg/appkit"
)

// Prometheus 是 instrumentation.prometheus 资源的类型化句柄。产物是具体类型
// （非 metrics.Metrics 接口）以便消费侧直接取 Registry() 挂暴露端；该类型恒
// 满足 metrics.Metrics，需要通用埋点口时按接口接收即可。
var Prometheus = appkit.NewKey[*prom.Provider]("instrumentation.prometheus")

// AppProvider 返回消费 instrumentation.prometheus 段的 appkit.Provider：段
// 缺失即被引擎跳过，段存在即构造并落资源面。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 instrumentation.prometheus 子段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"instrumentation.prometheus"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点。Priority 沿用 PriorityMetrics（埋点客户端与请求级 Recorder
// 同为「服务器构建前须就绪」这一层）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "instrumentation.prometheus",
		Segments:   []string{"instrumentation.prometheus"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityMetrics,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:instrumentation-prometheus",
	}}, nil
}

// Build 按契约子段构造后端，产物 map[string]any{"prometheus": *} 经引擎展开
// 落成 instrumentation.prometheus 资源。
func (appProvider) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	sec := s.Config.GetInstrumentation().GetPrometheus()
	p, cleanup, err := Provider(sec)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"prometheus": p}, appkit.CleanupFrom(cleanup), nil
}

var _ appkit.Provider = appProvider{}
