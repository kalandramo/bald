// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 注册
// func，契约字段 → etcd.Option 映射 + New）保留不动；本文件把段驱动接入面
// 收敛到 pkg/appkit/segment：AppProvider() 消费契约 registry.etcd 段，
// 节点形状由段族表派生，人只写 Build 主体，产物为 registry.Registrar 落
// 引擎注册中心位。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（provider 根包保持零契约依赖），root 包反向 import 本包——段驱动导出
// 只能落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/appkit/segment"
)

// AppProvider 返回消费契约 registry.etcd 段的 appkit.Provider（段路径
// "registry.etcd"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约段构造 func）占用
// Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：registry.etcd 段在 → 经既有 Provider 构造（endpoints 必填，
// clientv3 拨号失败 fail-fast）→ 产物 registry.Registrar 落引擎注册中心位
// （storeProduct 的顶层段约定）→ cleanup 关闭 client（Deregister 先于它
// 回放，顺序安全）。
//
// 启用的表达是**子段存在性**（registry.<backend> 段在即建），「不启用」即
// 移除对应子段——与 registry.type 字段无关。
func AppProvider() appkit.Provider {
	// 节点形状（registry 族：Bootstrap/PriorityRegistry/ReloadRestart/
	// effect appkit:registrar-client）由段族表派生，与迁移前手写四件套
	// 逐字段一致；Deregister 先于关 client 的回放序由家族 Effect 名保证。
	return segment.New("registry.etcd", func(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
		regSec := s.Config.GetRegistry()
		if regSec == nil {
			return nil, nil, fmt.Errorf("registry/etcd: registry section is missing " +
				"(provider matched by segment path; report this as a bug)")
		}
		r, cleanup, err := Provider(ctx, regSec)
		if err != nil {
			return nil, nil, err
		}
		return r, appkit.CleanupFrom(cleanup), nil
	})
}
