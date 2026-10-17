// provider.go —— 段驱动装配的加法式 Provider 导出（批 2 第三波：生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 契约
// 工厂，addr 必填 + 契约字段 → sse.Option 映射 + after-build 钩子）保留
// 不动；本文件只**新增**对 pkg/appkit 段驱动装配的接入面：AppProvider()
// 消费契约 server.sse 段，产物为 transport.Server 进服务器列表。
//
// server 段 co-provider：与 std 的 http/grpc 并存（不同段路径并存是引擎
// 的既有语义，W2b 测试已证明）；server 段在业务 BeforeStart 钩子之后构建
// （phaseLate，服务器构造消费业务建好的运行期资源——授权钩子 / 订阅回调
// 经 WithServerOptions / WithAfterBuild 注入，时序与旧逃生舱等价）。
//
// 本包的 Option（WithServerOptions / WithAfterBuild）经 AppProvider 的
// 变参透传，业务接线入口不变。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（sse 包保持零契约依赖），root 包反向 import 本包——段驱动导出只能
// 落在这里，复用既有 Provider 而非复制映射。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/transport"
)

// AppProvider 返回消费契约 server.sse 段的 appkit.Provider（段路径
// "server.sse"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约驱动的工厂函数）
// 占用 Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：server.sse 段在 → 经既有工厂构造（addr 必填，配段不给地址
// 即 fail-fast）→ 产物 transport.Server 落服务器列表 → cleanup 透传
// （sse 工厂当前不产停机清理，nil 合法）。
func AppProvider(opts ...Option) appkit.Provider {
	factory := Provider(opts...)
	return appProvider{factory: factory}
}

// appProvider 消费 server.sse 段。
type appProvider struct {
	factory func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error)
}

func (appProvider) Segments() []string { return []string{"server.sse"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
// 追加的是携带 factory 的接收者本身——工厂经 AppProvider 变参构造，落声明
// 时不能丢。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Build 复用既有工厂构造 SSE 服务器。
func (p appProvider) Build(ctx context.Context, s appkit.BuildScope) (any, appkit.Cleanup, error) {
	serverSec := s.Config.GetServer()
	if serverSec == nil {
		return nil, nil, fmt.Errorf("transport/sse: server section is missing " +
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
