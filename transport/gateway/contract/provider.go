// provider.go —— 段驱动装配的加法式 Provider 导出（生态接入，
// 范本 contrib/store-gorm 的 AppProvider）。既有构造逻辑（Provider 契约
// 工厂，addr/backend_grpc_addr 解析 + NewGatewayServer）保留不动；本文件
// 只**新增**对 pkg/appkit 段驱动装配的接入面：AppProvider() 消费契约
// server.gateway 段，产物为 transport.Server 进服务器列表。
//
// server 段 co-provider：与 std 的 http/grpc 并存（「gin 主面 + 独立转码面
// 并存」的声明式表达，不同段路径并存是引擎的既有语义）；server 段在业务
// BeforeStart 钩子之后构建（phaseLate）。转码注册回调（RegisterFunc）是
// 业务内容，契约段表达不了——经 AppProvider 的唯一参数显式注入，为 nil
// 时构造期 fail-fast（与既有工厂同判据）。
//
// 放置说明（与 store-gorm 的结构差异）：本模块的契约映射逻辑在 contract
// 包（gateway 包保持零契约依赖），root 包反向 import 本包——段驱动导出
// 只能落在这里，复用既有 Provider 而非复制映射。
//
// 两个面的分工：[AppProvider] 消费 server.gateway 段（独立转码面，与 gin
// 主面并存）；[HTTPProvider] 接管 server.http 段（同端口单面：HTTP 端口即
// 转码面）。bootstrap 不硬编 grpc-gateway，同端口模式只能由本段 Provider
// 表达（声明序末者胜：业务在 std.Standard() 之后声明即接管 server.http 的
// HTTP 面）。
package contract

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/health"
	"github.com/kalandramo/bald/pkg/appkit"
	gateway "github.com/kalandramo/bald/transport/gateway"
)

// AppProvider 返回消费契约 server.gateway 段的 appkit.Provider（段路径
// "server.gateway"；段缺失即被引擎跳过，合法）。
//
// 命名说明（store-gorm 同例）：本包既有 Provider（契约驱动的工厂函数）
// 占用 Provider 标识符且保留不动，故段驱动装配的导出名取 AppProvider。
//
// Build 语义：server.gateway 段在 → 经既有工厂构造（register 必供、addr
// 必填、backend_grpc_addr 缺省回退 server.grpc.addr，均 fail-fast）→
// 产物 transport.Server 落服务器列表 → cleanup 透传（gateway 工厂当前不产
// 停机清理，nil 合法；连接生命周期归 Start/Stop）。
//
// backend_grpc_addr 写成 discovery:///<服务名> 时经装配作用域自动取发现来源，
// 消费方只改一行配置（见 [discoveryScopeOption]）。派生要看 scope，故本
// Provider 保存构造参数（register + opts）而不是预绑工厂。
func AppProvider(register RegisterFunc, opts ...gateway.Option) appkit.Provider {
	return appProvider{register: register, opts: opts}
}

// appProvider 消费 server.gateway 段。
type appProvider struct {
	register RegisterFunc
	opts     []gateway.Option
}

func (appProvider) Segments() []string { return []string{"server.gateway"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
// 追加的是携带 register/opts 的接收者本身——构造参数经 AppProvider 变参收集，
// 落声明时不能丢。
func (p appProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明段节点（server.gateway；阶段由段族决定）。
func (appProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "server.gateway",
		Segments: []string{"server.gateway"},
		Phase:    appkit.PhaseServe,
	}}, nil
}

// Build 复用既有工厂构造 gateway 服务器；发现来源由当次 scope 补上，排在调用方
// opts 之前（显式注入的 WithDiscovery 优先）。
func (p appProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	serverSec := s.Config.GetServer()
	if serverSec == nil {
		return nil, nil, fmt.Errorf("transport/gateway: server section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	opts := append([]gateway.Option{discoveryScopeOption(s)}, p.opts...)
	srv, cleanup, err := Provider(p.register, opts...)(ctx, serverSec)
	if err != nil {
		return nil, nil, err
	}
	return srv, appkit.CleanupFrom(cleanup), nil
}

// ---------------------------------------------------------------------------
// server.http 段 Provider：同端口单面的 grpc-gateway 转码面
// ---------------------------------------------------------------------------

// HTTPProvider 返回接管契约 server.http 段的 appkit.Provider：HTTP 端口即
// REST → gRPC 转码面（gin 主面被替代）。
//
// 接管语义（声明序末者胜）：业务在 std.Standard() **之后**声明本 Provider 即
// 覆盖 std 的 HTTP 面；gRPC 面由独立的 server.grpc Provider 承载，不受影响。
//
// register 必供（转码面是业务内容，契约段表达不了）；nil 时构造期 fail-fast。
func HTTPProvider(register RegisterFunc, opts ...gateway.Option) appkit.Provider {
	return httpProvider{register: register, opts: opts}
}

// httpProvider 消费 server.http 段（driver 必须为 grpc-gateway 或留空）。
type httpProvider struct {
	// register 转码注册回调；WithHealth 声明时在外层补探针。
	register RegisterFunc
	opts     []gateway.Option
}

func (httpProvider) Segments() []string { return []string{"server.http"} }

// Apply 把自身追加进声明（Provider 即 Option）。
func (p httpProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明 HTTP 服务器节点（同 std HTTP 面的阶段：业务 BeforeStart 之后）。
//
// Opaque 与 std 的 server 节点同理：转码路由是回调内容，计划只能看到边界。
func (httpProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "server.http",
		Kind:     "server",
		Segments: []string{"server.http"},
		Phase:    appkit.PhaseServe,
		Reload:   appkit.ReloadStatic,
		Opaque:   true,
	}}, nil
}

// Build 按 server.http 段构造转码面服务器。
//
// driver 校验在此（显式 > 隐式）：非 grpc-gateway 的显式值说明业务把转码能力
// 接到了不消费它的面上，属配置与能力不匹配，构造期失败而非静默降级。
//
// 上游地址取契约 server.grpc.addr（同进程 gRPC 面）——与原 bootstrap 的
// gateway 模式同参：httpCfg 定面与 TLS、grpc 段定上游。
//
// 探针：WithHealth 声明时把双探针包在转码 handler 外层——协议实现不注册框架
// 路由，探针由面 Provider 负责（与 appkit 的 HTTP 面同源）。
func (p httpProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	serverSec := s.Config.GetServer()
	if serverSec == nil {
		return nil, nil, fmt.Errorf("transport/gateway: server section is missing " +
			"(provider matched by segment path; report this as a bug)")
	}
	httpCfg := serverSec.GetHttp()
	if httpCfg == nil {
		return nil, nil, fmt.Errorf("gateway: HTTPProvider declared but contract server.http is nil")
	}
	if d := httpCfg.GetDriver(); d != "" && d != DriverGRPCGateway {
		return nil, nil, fmt.Errorf("gateway: HTTPProvider declared but server.http.driver = %q, want %q or empty",
			d, DriverGRPCGateway)
	}
	if p.register == nil {
		return nil, nil, fmt.Errorf("gateway: HTTPProvider declared with a nil register func (transcoding face undefined)")
	}
	register := p.register
	if h := s.Decl.Health; h != nil {
		register = withProbes(register, h, s.Decl.ProbeLivePath, s.Decl.ProbeReadyPath)
	}
	srv, err := gateway.NewGatewayServer(httpCfg, serverSec.GetGrpc(), register, p.opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("gateway: build: %w", err)
	}
	return srv, nil, nil
}

// withProbes 在转码 handler 外层挂双探针（liveness 常量 + readiness 取健康源）。
func withProbes(register RegisterFunc, h *health.Health, live, ready string) RegisterFunc {
	return func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
		business, err := register(ctx, conn)
		if err != nil {
			return nil, err
		}
		mux := http.NewServeMux()
		mux.Handle(live, health.NewLivenessHandler())
		mux.Handle(ready, health.NewHandler(h))
		if business != nil {
			mux.Handle("/", business)
		}
		return mux, nil
	}
}
