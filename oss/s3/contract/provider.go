// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 注册
// func，region 必填 + fail-closed 拦截构造失败）保留不动；本文件只**新增**
// 对 pkg/appkit 段驱动装配的接入面：AppProvider() 消费契约 storage.s3 段，
// 复用 Provider 的构造逻辑，产物落引擎 storage 资源面。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（s3 包保持零契约依赖），root 包反向 import 本包——段驱动导出只能
// 落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	s3 "github.com/kalandramo/bald/oss/s3"
	"github.com/kalandramo/bald/pkg/appkit"
)

// S3 是 storage.s3 资源的类型化句柄（消费侧经 S3.Resource(app) 取 *s3.Storage）。
var S3 = appkit.NewKey[*s3.Storage]("storage.s3")

// AppProvider 返回消费契约 storage.s3 段的 appkit.Provider（段路径
// "storage.s3"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（bootstrap.StorageProvider
// 形状的注册 func）占用 Provider 标识符且保留不动，故段驱动装配的导出名
// 取 AppProvider。
//
// Build 语义：storage.s3 段在 → 经既有 Provider 构造（region 必填、SDK
// 构造失败 fail-closed）→ 产物 map[string]any{"s3": storage} 落引擎
// storage 资源面。cleanup 为 nil：aws-sdk 客户端无显式 Close 语义（与旧
// 12 段流一致）。
func AppProvider() appkit.Provider { return appProvider{} }

// appProvider 消费 storage.s3 段。
type appProvider struct{}

func (appProvider) Segments() []string { return []string{"storage.s3"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, appProvider{}) }

// Plan 声明段节点（storage.s3；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "storage.s3",
		Segments:   []string{"storage.s3"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityStorage,
		EffectName: "appkit:storage-clients",
	}}, nil
}

// Build 复用既有 Provider 构造 S3 兼容客户端并落资源容器。
func (appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	storageSec := s.Config.GetStorage()
	if storageSec == nil {
		return nil, nil, fmt.Errorf("oss/s3: storage section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	st, cleanup, err := Provider(ctx, storageSec)
	if err != nil {
		return nil, nil, err
	}
	return map[string]any{"s3": st}, adaptCleanup(cleanup), nil
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
