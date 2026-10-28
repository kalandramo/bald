// Package greetv1 是 greet 业务域（判据 ① 的活体：新增一个业务域只手写
// 3 个文件——proto/greet.proto（契约，已存在）、greetv1/greet.go（biz/model
// + 传输适配）、greetv1/domain.go（域声明值）——装配零改动）。
//
// 域在装配期自我接线：
//   - Wire：读自己的配置段（Settings 快照的 greet.* 子树）、构造自己的能力
//     （greeter——问候语前缀由域配置供给）、持有自己的端口；
//   - Routes：把 greet 的 REST 路由挂上应用 HTTP 面（默认构建）；
//   - Services：把 GreetService 注册进 gRPC 面（实现端口由 Wire 构造）。
//
// 域值的消费方是 main 的域清单（domains.gen.go——bald gen domain 生成），
// 经 appkit.MustServe 变参直传。
package greetv1

import (
	"context"

	"github.com/kalandramo/bald/pkg/appkit"
)

// Domain 是 greet 域的装配声明值（三个钩子均由引擎在既定时点消费，见
// pkg/appkit/domain.go：Wire 时点 = 业务 BeforeStart 之后、server 构建之前）。
//
// Declare 让计划可检视（非 Opaque 的 routes/services 节点）；构造仍由三个钩子
// 完成——引擎把它们塌缩成一个 Opaque 的 domain.greet.wire 节点（Domain v2
// 迁移期形态，设计 §4.6）。
var Domain = appkit.Domain{
	Name:     "greet",
	Declare:  declare,
	Wire:     wire,
	Routes:   registerRoutes,
	Services: registerService,
}

// declare 声明域的拓扑：HTTP 面存在（业务 handler 非网关让位）才有路由节点，
// 契约有 server.grpc 才有服务节点；两者都依赖 domain.greet.wire。
func declare(_ context.Context, s appkit.PlanScope) ([]appkit.Node, error) {
	var nodes []appkit.Node
	if s.Decl.HTTPHandler != nil {
		nodes = append(nodes, appkit.Node{
			Key:       "domain.greet.routes",
			Segments:  []string{"server.http"},
			Phase:     appkit.PhaseServe,
			Priority:  10,
			DependsOn: []appkit.ResourceKey{"domain.greet.wire"},
		})
	}
	if s.Config.GetServer().GetGrpc() != nil {
		nodes = append(nodes, appkit.Node{
			Key:       "domain.greet.services",
			Segments:  []string{"server.grpc"},
			Phase:     appkit.PhaseServe,
			Priority:  5,
			DependsOn: []appkit.ResourceKey{"domain.greet.wire"},
		})
	}
	return nodes, nil
}
