// provider.go —— 段驱动装配的加法式 Provider 导出：AppProvider() 消费契约
// server.graphql 段，产物为 transport.Server 进服务器列表。
//
// server 段 co-provider：与 std 的 http/grpc 并存（不同段路径并存是引擎的既有
// 语义）；server 段在业务 BeforeStart 钩子之后构建（phaseLate，服务器构造消费
// 业务建好的运行期资源——schema 路由 / 中间件经 WithServerOptions /
// WithAfterBuild 注入）。
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

// AppProvider 返回消费契约 server.graphql 段的 appkit.Provider（段路径
// "server.graphql"；段缺失即被引擎跳过，合法）。
//
// Build 语义：server.graphql 段在 → 经既有工厂构造（addr 必填，配段不给地址即
// fail-fast；tls 段在则按契约加载证书，Endpoint 随之切成 https）→ 产物
// transport.Server 落服务器列表 → cleanup 透传（graphql 工厂不产停机清理，
// 服务器由 appkit 的 Stop 阶段回收）。
func AppProvider(opts ...Option) appkit.Provider {
	return appProvider{factory: Provider(opts...)}
}

// appProvider 消费 server.graphql 段。
type appProvider struct {
	factory func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error)
}

func (appProvider) Segments() []string { return []string{"server.graphql"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 Serve 变参项）。
// 追加的是携带工厂的接收者本身——工厂经 AppProvider 变参构造，落声明时不能丢。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点（server.graphql；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "server.graphql",
		Segments: []string{"server.graphql"},
		Phase:    appkit.PhaseServe,
	}}, nil
}

// Build 复用既有工厂构造 GraphQL 服务器。
func (p appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	serverSec := s.Config.GetServer()
	if serverSec == nil {
		return nil, nil, fmt.Errorf("transport/graphql: server section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	srv, cleanup, err := p.factory(ctx, serverSec)
	if err != nil {
		return nil, nil, err
	}
	return srv, appkit.CleanupFrom(cleanup), nil
}
