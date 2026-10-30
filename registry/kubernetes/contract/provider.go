// provider.go —— 段驱动装配的 Provider 导出（范本 contrib/store-gorm 的
// AppProvider）。既有构造逻辑（契约字段 → kubernetes.Option 映射 + New）
// 保留不动；本文件只新增对 pkg/appkit 段驱动装配的接入面：AppProvider()
// 消费契约 registry.kubernetes 段，产物为 registry.Registrar 落引擎注册
// 中心位。
//
// 集群外无法自建 clientset（New 走 rest.InClusterConfig），此时用
// NewWithClient 注入后走 appkit.Registrar(...) 显式实例路径。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProvider 返回消费契约 registry.kubernetes 段的 appkit.Provider（段路径
// "registry.kubernetes"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约段构造 func）占用
// Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 registry.kubernetes 段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"registry.kubernetes"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, appProvider{}) }

// Plan 声明段节点（registry.kubernetes；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "registry.kubernetes",
		Segments:   []string{"registry.kubernetes"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityRegistry,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:registrar-client",
	}}, nil
}

// Build 复用既有 Provider 构造 kubernetes Registrar。
func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	regSec := s.Config.GetRegistry()
	if regSec == nil {
		return nil, nil, fmt.Errorf("registry/kubernetes: registry section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	r, cleanup, err := Provider(ctx, regSec)
	if err != nil {
		return nil, nil, err
	}
	return r, adaptCleanup(cleanup), nil
}

// adaptCleanup 把既有无参清理闭包适配为 appkit.Cleanup（nil 透传：无资源）。
func adaptCleanup(cleanup func()) appkit.Cleanup {
	if cleanup == nil {
		return nil
	}
	return func(context.Context) error { cleanup(); return nil }
}

// 编译期断言：appProvider 满足 appkit.Provider（含 Option 嵌入面）。
var _ appkit.Provider = appProvider{}
