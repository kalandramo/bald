// Package contract 提供 grpc-gateway 转码面的契约装配：bootstrapv1.Server 的
// gateway 段 → gateway.NewGatewayServer + 契约驱动的 ServerProvider。
//
// 单独成包的原因：transport/gateway 保持零契约依赖（核心不依赖 grpc-gateway），
// 只有本包 import bconf——业务按需 import，依赖图全程可见。
//
// ## 为什么需要这个子包（本该用 server.http.driver 的场景）
//
// 契约 server.http.driver = "grpc-gateway" 是「**同一端口二选一**」模式：HTTP
// 端口即转码面，gin 主面被替代。但「gin 主面 + 独立转码面并存」的拓扑（如
// bald-admin 的 :8080 gin + :8081 转码）表达不了，此前只能走 WithExtraServers
// 逃生舱。server.gateway 段（独立 addr + backend_grpc_addr）为此拓扑提供声明式
// 表达——本 Provider 即其装配实现。
//
// 与 bootstrap 的关系：本包**不** import bootstrap（Provider 返回结构化兼容签名，
// 用 var _ 守卫）。
package contract

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport"
	gateway "github.com/kalandramo/bald/transport/gateway"
)

// Type 是契约 server 段中 gateway 后端的段名。
const Type = "gateway"

// RegisterFunc 是 grpc-gateway 转码注册回调：业务在此建 runtime.ServeMux 并组合
// pb.RegisterXxxHandler。与 bootstrap.HTTPServerOption 的 WithGatewayRegister 同签名。
type RegisterFunc func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error)

// Provider 返回契约驱动的 gateway 工厂函数。
//
// 返回**未命名函数类型**（与 cache/redis/contract.Provider 同惯例）——调用点可直接
// 赋给 bootstrap.ServerProvider，无需显式转换，且本包不必 import bootstrap。
//
// register 必供：它是转码面的**业务内容**（哪些 service 暴露为 REST），契约段
// 的声明式字段表达不了。为 nil 时构造期 fail-fast——配置声明了 gateway 段却
// 无转码面，属配置与能力不匹配（显式 > 隐式）。
//
// 段后端地址解析：sec.backend_grpc_addr 非空时用它，否则回退 cfg.server.grpc.addr
// ——后者覆盖「gateway 与 gRPC 同进程」的常见拓扑（无需重复声明地址）。
func Provider(register RegisterFunc) func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
	return func(_ context.Context, cfg *bootstrapv1.Server) (transport.Server, func(), error) {
		sec := cfg.GetGateway()
		if sec == nil {
			return nil, nil, nil // 未配置 gateway 段，跳过
		}
		if register == nil {
			return nil, nil, fmt.Errorf("gateway: server.gateway configured but no register func provided (transcoding face undefined)")
		}
		if sec.GetAddr() == "" {
			return nil, nil, fmt.Errorf("gateway: server.gateway.addr is required")
		}

		backend := sec.GetBackendGrpcAddr()
		if backend == "" {
			g := cfg.GetGrpc()
			if g == nil || g.GetAddr() == "" {
				return nil, nil, fmt.Errorf("gateway: server.gateway.backend_grpc_addr is empty and server.grpc.addr is not set")
			}
			backend = g.GetAddr()
		}

		httpCfg := &bootstrapv1.Server_Http{Addr: sec.GetAddr(), Tls: sec.GetTls()}
		backendCfg := &bootstrapv1.Server_Grpc{Addr: backend}

		gw, err := gateway.NewGatewayServer(httpCfg, backendCfg, register)
		if err != nil {
			return nil, nil, fmt.Errorf("gateway: build: %w", err)
		}
		return gw, nil, nil
	}
}

// 类型守卫：Provider 返回的签名与 bootstrap.ServerProvider 结构化兼容。
var _ func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) = Provider(nil)
