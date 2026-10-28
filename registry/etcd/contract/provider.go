// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 注册
// func，契约字段 → etcd.Option 映射 + New）保留不动；本文件只**新增**对
// pkg/appkit 段驱动装配的接入面：AppProvider() 消费契约 registry.etcd 段，
// 产物为 registry.Registrar 落引擎注册中心位。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（provider 根包保持零契约依赖），root 包反向 import 本包——段驱动导出
// 只能落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// AppProvider 返回消费契约 registry.etcd 段的 appkit.Provider（段路径
// "registry.etcd"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（RegistrarRegistry 形状
// 的注册 func）占用 Provider 标识符且保留不动，故段驱动装配的导出名取
// AppProvider。
//
// Build 语义：registry.etcd 段在 → 经既有 Provider 构造（endpoints 必填，
// clientv3 拨号失败 fail-fast）→ 产物 registry.Registrar 落引擎注册中心位
// （storeProduct 的顶层段约定）→ cleanup 关闭 client（Deregister 先于它
// 回放，顺序安全）。
//
// 与旧 RegistrarRegistry 的语义差异：旧流按 registry.type == "etcd" 分派，
// 段驱动按**子段存在性**分派（registry.etcd 段在即建）——「不启用」的表达
// 是移除对应子段。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 registry.etcd 段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"registry.etcd"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, appProvider{}) }

// Plan 声明段节点（registry.etcd；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "registry.etcd",
		Segments:   []string{"registry.etcd"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityRegistry,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:registrar-client",
	}}, nil
}

// Build 复用既有 Provider 构造 etcd Registrar。
func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	regSec := s.Config.GetRegistry()
	if regSec == nil {
		return nil, nil, fmt.Errorf("registry/etcd: registry section is missing " +
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
