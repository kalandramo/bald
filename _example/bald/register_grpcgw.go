//go:build grpcgw

// 本文件演示 bald 的 grpc-gateway transcoding 接线。
//
// 它依赖 protoc 生成的代码（见 proto/ 目录与下方 import 的 baldv1 包），
// 因此用 build tag `grpcgw` 保护：默认 `go build ./...` 不会编译它，
// 仓库在无 protoc 环境也能正常构建。本地装好工具链后：
//
//	cd _example/bald
//	make proto          # buf generate 生成 gen/baldv1/*.go
//	cd ../..
//	go run -tags grpcgw ./_example/bald --config=_example/bald/configs/bald-demo.yaml
//
// 此时 GreetService 同时可通过 gRPC 与 REST 访问（见 proto 的 google.api.http 注解）。
package main

import (
	"context"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/server"
	baldoptions "github.com/kalandramo/bald/pkg/options"

	// 以下两个包由 `make proto` 生成（protoc-gen-go / protoc-gen-go-grpc /
	// protoc-gen-grpc-gateway）。未生成前本文件因 build tag 不参与编译。
	baldv1 "github.com/kalandramo/bald/_example/bald/gen/baldv1"
)

// Greet 是 biz 层业务函数：被 gin 的 web.HandleJSONRequest 与 gRPC 的
// GreetService 同时调用，实证「gin 与 grpc-gateway 复用同一 biz 层」。
// 若要彻底统一，可把 exampleRoutes 里的内联 greet 闭包也改为调用本函数。
func Greet(ctx context.Context, name string) (string, error) {
	if name == "" {
		name = "world"
	}
	return "hello, " + name, nil
}

// greetService 实现 protoc 生成的 baldv1.GreetServiceServer 接口。
type greetService struct{}

func (s *greetService) Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error) {
	greet, err := Greet(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &baldv1.GreetResponse{Greet: greet}, nil
}

func (s *greetService) GreetGet(ctx context.Context, req *baldv1.GreetGetRequest) (*baldv1.GreetResponse, error) {
	greet, err := Greet(ctx, req.GetName())
	if err != nil {
		return nil, err
	}
	return &baldv1.GreetResponse{Greet: greet}, nil
}

// registerGRPCService 把 gRPC service 实现注册进 *grpc.Server，
// 直接传给 server.NewGRPCServerWithRegister 的 register 回调。
func registerGRPCService(s *grpc.Server) {
	baldv1.RegisterGreetServiceServer(s, &greetService{})
}

// registerGateway 把 grpc-gateway 的 HTTP handler 注册进 *http.ServeMux，
// 直接传给 server.NewGatewayServer 的 register 回调。conn 是到本进程
// gRPC 服务（grpcOpts.Addr）的连接，由 GatewayServer 内部建立。
func registerGateway(ctx context.Context, mux *http.ServeMux, conn *grpc.ClientConn) error {
	return baldv1.RegisterGreetServiceHandler(ctx, mux, conn)
}

// 以下两个构造函数由 main.go 在启用 grpcgw tag 时调用，替换默认的空壳注册。
// 为保持默认编译零依赖，它们放在本 build-tag 文件内；启用时请把 main.go
// 第 105 行的 NewGRPCServerWithRegister 的 register 改为 registerGRPCService，
// 并在 HTTP 服务旁用 NewGatewayServer 挂载一个网关服务器（见文档示例）。

func newGRPCServerWithGreet(opts *baldoptions.GRPCOptions, unary []grpc.ServerOption, ready server.ReadinessFunc) *server.GRPCServer {
	return server.NewGRPCServerWithRegister(opts, unary, registerGRPCService, ready)
}

func newGatewayWithGreet(
	httpOpts *baldoptions.SecureServingOptions,
	grpcAddr string,
	ready server.ReadinessFunc,
) (*server.GatewayServer, error) {
	return server.NewGatewayServer(httpOpts, grpcAddr, registerGateway, ready)
}
