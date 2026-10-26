//go:build grpcgw

// 本文件演示 bald 的 grpc-gateway transcoding 接线（应用级网关面）。
//
// 它依赖 protoc 生成的代码（见 proto/ 目录与下方 import 的 baldv1 包），
// 因此用 build tag `grpcgw` 保护：默认 `go build ./...` 不会编译它，
// 仓库在无 protoc 环境也能正常构建。本地装好工具链后：
//
//	cd _example/bald
//	task proto          # buf generate 生成 gen/*.go
//	go run -tags grpcgw ./cmd/bald-demo            # 配置随示例自带，自动加载
//
// 此时 GreetService 同时可通过 gRPC 与 REST 访问（见 proto 的 google.api.http
// 注解）。REST 面由契约 server.http.driver=grpc-gateway 选择（gatewayRegister
// 回调声明网关能力），占用 server.http 端口，gin 演示路由让位（greet 域的
// Routes 槽位同规则让位——同端口单面语义）。
//
// 域边界说明：gRPC service 注册与契约校验已随 greet 域迁入 greetv1 包
// （域自持契约——Domain.Services / greetv1.Validator）；本文件只剩应用级的
// 网关转码面接线（face 选择是应用策略，非域知识）。
package bootstrap

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"

	// 由 `task proto` 生成（protoc-gen-go / protoc-gen-go-grpc /
	// protoc-gen-grpc-gateway）。未生成前本文件因 build tag 不参与编译。
	//
	// 路径说明：_example/bald 是独立 module
	// github.com/kalandramo/bald/example/bald，故前缀是 .../example/bald/gen。
	baldv1 "github.com/kalandramo/bald/example/bald/gen"
)

// init 注入 grpc-gateway 转码注册回调（assemble.go 的 RoleAPI 分支经
// WithGatewayRegister 声明网关能力，配合契约 server.http.driver=grpc-gateway
// 接入网关转码面）。
func init() {
	gatewayRegister = registerGateway
}

// registerGateway 是 grpc-gateway 转码注册回调：把 GreetService 的 REST 面
// 注册到 runtime.ServeMux（grpc-gateway v2 的 mux，非标准库 http.ServeMux），
// 并把它作为 http.Handler 交回给 GatewayServer。
//
// conn 是到本进程 gRPC 服务的连接（契约 server.grpc.addr），由 GatewayServer
// 在 Start 时建立。返回 http.Handler 而非在入参 mux 上注册，是为了让
// transport 不必依赖 grpc-gateway（见 NewGatewayServer 的注释）。
func registerGateway(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux()
	if err := baldv1.RegisterGreetServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}
