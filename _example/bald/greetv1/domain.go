// Package greetv1 是 greet 业务域：域声明值与 biz/model + 传输适配。
//
// 域在装配期自我接线：Declare 声明域自有节点，ApplyNode/Reconcile 按计划执行，
// Services 在 server 构建时注册 gRPC service。
package greetv1

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/pkg/appkit"
)

// Domain 是 greet 域的装配声明值。
var Domain = appkit.Domain{
	Name:      "greet",
	Declare:   declare,
	ApplyNode: applyNode,
	Reconcile: reconcileNode,
	Services:  registerService,
	// 原生 gin 面只挂 POST /v1/greet；GET /v1/greet/{name} 走 grpc-gateway 转码。
	RouteTable: []appkit.Route{
		{Method: "POST", Path: "/v1/greet", RPC: "Greet"},
	},
}

// declare 声明域拓扑：biz 节点恒有；HTTP 面存在（非网关让位）才有路由节点。
// biz.greet 标 ReloadReconcile：该子树变化即触发协调。
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

func applyNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.greet.biz":
		return wire(ctx, app)
	case "domain.greet.routes":
		return registerRoutes(app.HTTPHandler())
	}
	return fmt.Errorf("greetv1: unknown declared node %q", node.Key)
}

func reconcileNode(ctx context.Context, app *appkit.App, node appkit.Node) error {
	switch node.Key {
	case "domain.greet.biz":
		return wire(ctx, app)
	case "domain.greet.routes":
		return nil
	}
	return fmt.Errorf("greetv1: unknown reconcile node %q", node.Key)
}
