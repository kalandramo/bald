// Package greetv1 是 greet 业务域（判据 ① 的活体：新增一个业务域只手写
// 3 个文件——proto/greet.proto（契约，已存在）、greetv1/greet.go（biz/model
// + 传输适配）、greetv1/domain.go（域声明值）——装配零改动）。
//
// 域在装配期自我接线（Domain v2 目标形态）：
//   - Declare：声明域自有节点（domain.greet.biz、domain.greet.routes），非
//     Opaque——计划可检视、可排序（设计 §4.6）；biz 节点声明业务配置段
//     biz.greet 并标 ReloadReconcile——业务配置因此进入期望态（设计 Q8）；
//   - ApplyNode：引擎在域窗口内按计划序逐节点调用——biz 节点读自己的配置段
//     （Settings 快照的 biz.greet.* 子树）、构造自己的能力（greeter）、持有
//     自己的端口；routes 节点把 greet 的 REST 路由挂上应用 HTTP 面（默认构建）；
//   - Reconcile：配置热更时引擎按节点 Key 调用，biz 节点按新快照重建端口
//     （期望态摘要取自 biz.greet 子树，配置一变即触发）；
//   - Services：延迟注册回调（server 构建时执行）——把 GreetService 注册进
//     gRPC 面，实现端口由 biz 节点构造。服务注册的时点不在域窗口（grpc.Server
//     彼时还不存在），故它保持独立槽位，不经过 ApplyNode。
//
// 域值的消费方是 main 的域清单（domains.gen.go——bald gen domain 生成），
// 经 appkit.MustServe 变参直传。
package greetv1

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// Domain 是 greet 域的装配声明值（Engine 在域窗口内按计划序调用 ApplyNode，
// 时点 = 业务 BeforeStart 之后、server 构建之前；Services 在 server 构建时
// 调用）。
//
// 全部构造都在声明节点里完成，域不再产生 Opaque 兼容节点——示例计划的逃逸舱
// 只剩框架自身的两个（程序化 registry、std server）。
var Domain = appkit.Domain{
	Name:      "greet",
	Declare:   declare,
	ApplyNode: applyNode,
	Reconcile: reconcileNode,
	Services:  registerService,
	// RouteTable 是域自持 REST 面实际挂载的路由（可检视的计划数据）。原生
	// gin 面只挂 POST /v1/greet（GET /v1/greet/{name} 走 grpc-gateway 转码，
	// 由 proto 注解提供，不在此表）；见 registerRoutes。
	RouteTable: []appkit.Route{
		{Method: "POST", Path: "/v1/greet", RPC: "Greet"},
	},
}

// declare 声明域的拓扑：biz 节点恒有；HTTP 面存在（业务 handler 非网关让位）
// 才有路由节点。服务注册不声明节点——它由 Services 槽位在 server 构建时兑现。
//
// biz 节点挂业务配置段 biz.greet 并标 ReloadReconcile：计划期从合并 Settings
// 快照算期望态摘要，biz.greet 子树一变即走协调（重载策略 + 摘要口径见设计
// §4.8 / Q8）。
func declare(_ context.Context, s appkit.PlanScope) ([]appkit.Node, error) {
	nodes := []appkit.Node{{
		Key:      "domain.greet.biz",
		Segments: []string{"biz.greet"},
		Phase:    appkit.PhaseWire,
		Priority: 10,
		Reload:   appkit.ReloadReconcile,
	}}
	if s.Decl.HTTPHandler != nil {
		nodes = append(nodes, appkit.Node{
			Key:       "domain.greet.routes",
			Segments:  []string{"server.http"},
			Phase:     appkit.PhaseServe,
			Priority:  10,
			DependsOn: []appkit.ResourceKey{"domain.greet.biz"},
		})
	}
	return nodes, nil
}

// applyNode 按节点 Key 执行域的构造：biz 构造能力与端口，routes 挂 REST 路由。
// 未知 Key 是声明与执行漂移——fail-fast，不静默跳过。
func applyNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.greet.biz":
		return wire(ctx, app)
	case "domain.greet.routes":
		return registerRoutes(app.HTTPHandler())
	}
	return fmt.Errorf("greetv1: unknown declared node %q", node.Key)
}

// reconcileNode 是热更协调体：biz 节点按新配置快照重建端口（整份 greeter
// 原子交换，读方无撕裂）；routes 节点静态（注册后不随配置变化），无需协调。
// 未知 Key 同样 fail-fast——声明与协调漂移不应静默。
func reconcileNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.greet.biz":
		return wire(ctx, app)
	case "domain.greet.routes":
		return nil
	}
	return fmt.Errorf("greetv1: unknown reconcile node %q", node.Key)
}
