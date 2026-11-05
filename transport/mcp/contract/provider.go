// provider.go —— 段驱动装配的加法式 Provider 导出：AppProvider() 消费契约
// server.mcp 段，产物为 transport.Server 进服务器列表。
//
// server 段 co-provider：与 std 的 http/grpc 并存（不同段路径并存是引擎的既有
// 语义）；server 段在业务 BeforeStart 钩子之后构建（phaseLate，服务器构造消费
// 业务建好的运行期资源——工具 / 资源注册经 WithAfterBuild 注入）。
//
// 本包的 Option（WithServerOptions / WithAfterBuild）经 AppProvider 的变参透传，
// 业务接线入口不变。
package contract

import (
	"context"
	"fmt"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/transport"
)

// AppProvider 返回消费契约 server.mcp 段的 appkit.Provider（段路径 "server.mcp"；
// 段缺失即被引擎跳过，合法）。
//
// Build 语义：server.mcp 段在 → 经既有工厂构造（address 必填，配段不给地址即
// fail-fast；形态按契约固定为 HTTP）→ 产物 transport.Server 落服务器列表 →
// cleanup 透传（mcp 工厂不产停机清理，服务器由 appkit 的 Stop 阶段回收）。
//
// **Start 非阻塞是这一族的特例**：mcp 的 HTTP/SSE 形态在 Start 里同步绑端口后
// 把 Serve 放进后台 goroutine，随即返回 nil（stdio 形态同理后台阻塞）。appkit 的
// 主等待挂在 gctx 上（只有 Start 返错才取消），所以「Start 先返回」不会让进程
// 退出；等待端点那一步读到的是 Start 已填好的真实端口。
func AppProvider(opts ...Option) appkit.Provider {
	return appProvider{factory: Provider(opts...)}
}

// appProvider 消费 server.mcp 段。
type appProvider struct {
	factory func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error)
}

func (appProvider) Segments() []string { return []string{"server.mcp"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 Serve 变参项）。
// 追加的是携带工厂的接收者本身——工厂经 AppProvider 变参构造，落声明时不能丢。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点（server.mcp；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "server.mcp",
		Segments: []string{"server.mcp"},
		Phase:    appkit.PhaseServe,
	}}, nil
}

// Build 复用既有工厂构造 MCP 服务器。
func (p appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	serverSec := s.Config.GetServer()
	if serverSec == nil {
		return nil, nil, fmt.Errorf("transport/mcp: server section is missing " +
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
