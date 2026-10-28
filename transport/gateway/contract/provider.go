// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 契约
// 工厂，addr/backend_grpc_addr 解析 + NewGatewayServer）保留不动；本文件
// 只**新增**对 pkg/appkit 段驱动装配的接入面：AppProvider() 消费契约
// server.gateway 段，产物为 transport.Server 进服务器列表。
//
// server 段 co-provider：与 std 的 http/grpc 并存（「gin 主面 + 独立转码面
// 并存」的声明式表达，不同段路径并存是引擎的既有语义）；server 段在业务
// BeforeStart 钩子之后构建（phaseLate）。转码注册回调（RegisterFunc）是
// 业务内容，契约段表达不了——经 AppProvider 的唯一参数显式注入，为 nil
// 时构造期 fail-fast（与既有工厂同判据）。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（gateway 包保持零契约依赖），root 包反向 import 本包——段驱动导出
// 只能落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/transport"
)

// AppProvider 返回消费契约 server.gateway 段的 appkit.Provider（段路径
// "server.gateway"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约驱动的工厂函数）
// 占用 Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：server.gateway 段在 → 经既有工厂构造（register 必供、addr
// 必填、backend_grpc_addr 缺省回退 server.grpc.addr，均 fail-fast）→
// 产物 transport.Server 落服务器列表 → cleanup 透传（gateway 工厂当前不产
// 停机清理，nil 合法；连接生命周期归 Start/Stop）。
func AppProvider(register RegisterFunc) appkit.Provider {
	factory := Provider(register)
	return appProvider{factory: factory}
}

// appProvider 消费 server.gateway 段。
type appProvider struct {
	factory func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error)
}

func (appProvider) Segments() []string { return []string{"server.gateway"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
// 追加的是携带 factory 的接收者本身——工厂经 AppProvider 变参构造，落声明
// 时不能丢。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点（server.gateway；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "server.gateway",
		Segments: []string{"server.gateway"},
		Phase:    appkit.PhaseServe,
	}}, nil
}

// Build 复用既有工厂构造 gateway 服务器。
func (p appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	serverSec := s.Config.GetServer()
	if serverSec == nil {
		return nil, nil, fmt.Errorf("transport/gateway: server section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	srv, cleanup, err := p.factory(ctx, serverSec)
	if err != nil {
		return nil, nil, err
	}
	return srv, adaptCleanup(cleanup), nil
}

// adaptCleanup 把既有无参清理闭包适配为 appkit.Cleanup（nil 透传：无资源）。
func adaptCleanup(cleanup func()) appkit.Cleanup {
	if cleanup == nil {
		return nil
	}
	return func(context.Context) error { cleanup(); return nil }
}
