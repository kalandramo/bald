// standard.go 是框架自有后端的内置 Provider 实现（std.Standard() 的成员；
// 装配层评估 §4.2 具体入口，批 2 W2b）。
//
// 实现落在 appkit（而非 std 包）的原因：两者都深度消费 appkit 内部件——
// server Provider 消费 Decl 能力声明与探针包装（withProbes），audit 产物
// 经引擎 storeProduct 全局注入——且包内测试需要不经 import 环引用它们。
// pkg/appkit/std 以 Standard() 打包导出（对账表见该包文档）；两个导出构造
// 器同时允许高级用户单独取用或覆盖单个默认。
//
// 成员清单（对账基准 = 原 12 段手工流的实际默认接线，不多不少）：
//   - StandardServerProvider：server.http / server.grpc——能力声明在代码
//     （WithHTTP/WithGatewayRegister/WithGRPC），契约段驱动地址/TLS/driver；
//   - StandardAuditProvider：audit.backends 的 "log" 项——核心 LoggerAuditor
//     零依赖开箱即用。
//
// 其余各段（registry/database/.../config/tracer/metrics）在原 12 段流无
// 默认接线，不设内置 Provider（段存在而无 Provider = L3 启动期报错）。
package appkit

import (
	"context"
	"fmt"
	"net/http"

	"google.golang.org/grpc"

	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	"github.com/kalandramo/bald/health"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/transport"
)

// StandardServerProvider 返回框架内置协议服务器 Provider（server.http /
// server.grpc 段；std.Standard() 成员）。构建时机为迟后阶段（业务
// BeforeStart 钩子之后——register 回调与 handler 消费业务建好的运行期资源，
// wiring_order_test 钉住该时序）。
func StandardServerProvider() Provider { return stdServerProvider{} }

// StandardAuditProvider 返回框架内置审计 Provider（audit 段 "log" 后端；
// std.Standard() 成员）。第三方 store/stream 后端经用户 audit Provider
// 覆盖（段路径末者胜）。
func StandardAuditProvider() Provider { return stdAuditProvider{} }

// ---------------------------------------------------------------------------
// server Provider：server.http / server.grpc
// ---------------------------------------------------------------------------

// stdServerProvider 消费 server.http / server.grpc 段（引擎按顶层段
// "server" 归入迟后阶段）。原 assemble 内部「能力声明 → 注册 provider →
// BuildServers」流的 Provider 化，行为逐条一致。
type stdServerProvider struct{}

func (stdServerProvider) Segments() []string { return []string{"server.http", "server.grpc"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (stdServerProvider) Apply(d *Decl) { d.Providers = append(d.Providers, stdServerProvider{}) }

// Build 按能力声明构造协议服务器（gRPC 先于 HTTP——与旧 BuildServers 的
// 注册名排序 "grpc" < "http" 一致，保持服务器列表序与停机聚合序不变）。
// 产物为 []transport.Server（零能力声明 = 空切片，跳过构造合法：纯后台
// 进程）；cleanup 聚合逆序释放（后建先收，与 runClosers 栈式语义一致）。
func (p stdServerProvider) Build(ctx context.Context, s BuildScope) (any, Cleanup, error) {
	d := s.Decl
	if d.HTTPHandler == nil && d.GatewayRegister == nil && d.GRPCRegister == nil {
		return nil, nil, nil // 零能力声明：跳过构造（纯后台进程合法）
	}

	var (
		servers []transport.Server
		closers []func()
	)
	// gRPC：service 注册 + 拦截器链（静态在前）+ 运行期选项工厂 + 健康状态。
	if d.GRPCRegister != nil {
		grpcOpts := []baldbootstrap.GRPCServerOption{
			baldbootstrap.WithGRPCUnary(d.GRPCUnary...),
			baldbootstrap.WithGRPCRegister(d.GRPCRegister),
		}
		// WithGRPCOptions：运行期求值的选项工厂在 server 构造时调用（见
		// bootstrap.GrpcServerProvider），彼时业务 beforeStart 已建好认证器。
		if d.GRPCOptionsFn != nil {
			grpcOpts = append(grpcOpts, baldbootstrap.WithGRPCUnaryFunc(d.GRPCOptionsFn))
		}
		if d.Health != nil {
			grpcOpts = append(grpcOpts, baldbootstrap.WithGRPCHealth(d.Health, d.HealthInterval))
		}
		srv, closer, err := baldbootstrap.GrpcServerProvider(grpcOpts...)(ctx, s.Config.GetServer())
		if err != nil {
			runClosers(closers)
			return nil, nil, err
		}
		if srv != nil {
			servers = append(servers, srv)
		}
		if closer != nil {
			closers = append(closers, closer)
		}
	}
	// HTTP：业务 handler 或网关转码面（driver 由契约 server.http.driver 选择）。
	// 健康检查默认装配在此落地：探针包在业务 handler 外层（协议实现不注册
	// 任何框架路由），见《Bald 健康检查装配设计》。
	if d.HTTPHandler != nil || d.GatewayRegister != nil {
		var httpOpts []baldbootstrap.HTTPServerOption
		handler := d.HTTPHandler
		if d.Health != nil && handler != nil {
			handler = withProbes(handler, d.Health, d.ProbeLivePath, d.ProbeReadyPath)
		}
		httpOpts = append(httpOpts, baldbootstrap.WithHTTPHandler(handler))
		if d.GatewayRegister != nil {
			gatewayRegister := d.GatewayRegister
			if d.Health != nil {
				gatewayRegister = withGatewayProbes(gatewayRegister, d.Health, d.ProbeLivePath, d.ProbeReadyPath)
			}
			httpOpts = append(httpOpts, baldbootstrap.WithGatewayRegister(gatewayRegister))
		}
		srv, closer, err := baldbootstrap.HttpServerProvider(httpOpts...)(ctx, s.Config.GetServer())
		if err != nil {
			runClosers(closers)
			return nil, nil, err
		}
		if srv != nil {
			servers = append(servers, srv)
		}
		if closer != nil {
			closers = append(closers, closer)
		}
	}

	var cleanup Cleanup
	if len(closers) > 0 {
		cleanup = func(context.Context) error { runClosers(closers); return nil }
	}
	return servers, cleanup, nil
}

// runClosers 逆序执行清理函数：后构造的资源先释放（栈式释放）。
func runClosers(closers []func()) {
	for i := len(closers) - 1; i >= 0; i-- {
		closers[i]()
	}
}

// ---------------------------------------------------------------------------
// audit Provider：audit.backends 的 "log" 项
// ---------------------------------------------------------------------------

// stdAuditProvider 消费 audit 段。
type stdAuditProvider struct{}

func (stdAuditProvider) Segments() []string { return []string{"audit"} }

// Apply 把自身追加进声明（Provider 即 Option）。
func (stdAuditProvider) Apply(d *Decl) { d.Providers = append(d.Providers, stdAuditProvider{}) }

// Build 按契约 audit.backends 列表装配审计后端（多选，列表序即广播序）。
// "log" 内置；其余项 fail-fast 教学报错（std 不含第三方后端——经用户
// audit Provider 接管）。多后端组装、失败逆序回滚收敛在 pkg/audit.Compose
// （批 1 内化）。产物为 audit.Auditor，由引擎全局注入（SetAuditor）并在
// 停机恢复装配前全局——见 storeProduct。
func (stdAuditProvider) Build(ctx context.Context, s BuildScope) (any, Cleanup, error) {
	acfg := s.Config.GetAudit()
	if acfg == nil {
		return nil, nil, nil // 段缺省：不装配（引擎仅在段存在时调用，防御性）
	}
	backends := acfg.GetBackends()
	if len(backends) == 0 {
		return nil, nil, fmt.Errorf("appkit: audit.backends is required when audit section is present (expect %q, %q or %q)",
			AuditTypeLog, AuditTypeStore, AuditTypeStream)
	}
	backs := make([]audit.NamedBackend, 0, len(backends))
	for _, typ := range backends {
		if typ != AuditTypeLog {
			return nil, nil, fmt.Errorf(
				"appkit: audit backend %q is not available in std (std ships the built-in %q backend only; "+
					"declare an audit Provider consuming the audit segment to assemble store/stream backends)", typ, AuditTypeLog)
		}
		backs = append(backs, audit.NamedBackend{Name: typ, New: func(context.Context) (audit.Auditor, func(context.Context) error, error) {
			return audit.NewLoggerAuditor(), nil, nil // 内置：零依赖开箱即用
		}})
	}
	a2, cleanup, err := audit.Compose(ctx, backs...)
	if err != nil {
		return nil, nil, fmt.Errorf("appkit: build audit: %w", err)
	}
	return a2, cleanup, nil
}

// withGatewayProbes 包装 gateway 转码注册回调：转码 handler 由业务在 Start 期
// 才产出，探针包在返回值外层（与纯 HTTP 面复用 withProbes）。
func withGatewayProbes(
	register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error),
	h *health.Health,
	live, ready string,
) func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	return func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
		business, err := register(ctx, conn)
		if err != nil {
			return nil, err
		}
		return withProbes(business, h, live, ready), nil
	}
}
