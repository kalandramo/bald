// Package greetv1 是 greet 业务域（判据 ① 的活体：新增一个业务域只手写
// 3 个文件——proto/greet.proto（契约，已存在）、greetv1/greet.go（biz/model
// + 传输适配）、greetv1/domain.go（域声明值）——装配零改动）。
//
// 域在装配期自我接线（Domain v2 目标形态）：
//   - Declare：声明域自有节点（domain.greet.biz、domain.greet.routes），非
//     Opaque——计划可检视、可排序（设计 §4.6）；
//   - ApplyNode：引擎在域窗口内按计划序逐节点调用——biz 节点读自己的配置段
//     （Settings 快照的 greet.* 子树）、构造自己的能力（greeter）、持有自己
//     的端口；routes 节点把 greet 的 REST 路由挂上应用 HTTP 面（默认构建）；
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
	Services:  registerService,
}

// declare 声明域的拓扑：biz 节点恒有；HTTP 面存在（业务 handler 非网关让位）
// 才有路由节点。服务注册不声明节点——它由 Services 槽位在 server 构建时兑现。
func declare(_ context.Context, s appkit.PlanScope) ([]appkit.Node, error) {
	nodes := []appkit.Node{{
		Key:      "domain.greet.biz",
		Phase:    appkit.PhaseWire,
		Priority: 10,
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
