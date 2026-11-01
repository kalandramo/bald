// standard.go 是框架自有后端的内置 Provider 实现（std.Standard() 的成员）。
//
// 实现落在 appkit（而非 std 包）的原因：两者都深度消费 appkit 内部件——
// server Provider 消费 Decl 能力声明与探针包装（withProbes），audit 产物
// 经引擎 storeProduct 全局注入——且包内测试需要不经 import 环引用它们。
// pkg/appkit/std 以 Standard() 打包导出（对账表见该包文档）；两个导出构造
// 器同时允许高级用户单独取用或覆盖单个默认。
//
// 成员清单（框架内置默认，不多不少）：
//   - StandardHTTPProvider / StandardGRPCProvider：server.http 与 server.grpc
//     各一个节点——能力声明在代码（WithHTTP/WithGRPC），契约段驱动地址/TLS；
//     分两个 Provider 是为了让第三方转码面能只接管 server.http 而不挤掉 gRPC
//     面（见 stdHTTPProvider 上方注释）；
//   - StandardAuditProvider：audit.backends 的 "log" 项——核心 LoggerAuditor
//     零依赖开箱即用；
//   - StandardLoggerProvider：logger 段——bslog（标准库 slog）后端，阶段 A
//     引导 logger 的持有者（默认日志分派的唯一内置通道）。
//
// 其余各段（registry/database/.../config/tracer/metrics）没有内置默认接线，
// 不设内置 Provider（段存在而无 Provider = L3 启动期报错）。
package appkit

import (
	"context"
	"fmt"

	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/transport"
)

// StandardAuditProvider 返回框架内置审计 Provider（audit 段 "log" 后端；
// std.Standard() 成员）。第三方 store/stream 后端经用户 audit Provider
// 覆盖（段路径末者胜）。
func StandardAuditProvider() Provider { return stdAuditProvider{} }

// StandardLoggerProvider 返回框架内置日志 Provider（logger 段；std.Standard()
// 成员）。这是日志装配的唯一内置通道（App 层不再有第二个日志路径）：
//   - 阶段 A（Config=nil，契约装载前）：产出引导 bslog——框架持有的
//     「引导 logger」，保证启动日志可见；阶段 B 用本 Provider 的契约结果替换；
//   - 阶段 B/热更：按契约 logger.backends 逐项构造（type=slog 直构，多项
//     MultiLogger 合并，出口包 FilterLogger）；非 slog 项 fail-fast
//     教学报错；backends 为空 fail-fast。
//
// deco 是装饰钩子（bslog.Option），在阶段 A 与阶段 B 的每个 slog 构造点统一
// 应用——Attrs 脱敏（bslog.WithFilter）与固定结构化属性等关注点不必再复制
// 整份分派逻辑。装饰只需替换默认 Provider：std.Standard() 后追传本构造器
// 即可（Provider 即 Option，段路径末者胜）。
func StandardLoggerProvider(deco ...bslog.Option) Provider { return stdLoggerProvider{deco: deco} }

// stdLoggerProvider 消费 logger 段。
type stdLoggerProvider struct{ deco []bslog.Option }

func (stdLoggerProvider) Segments() []string { return []string{"logger"} }

// Apply 把自身追加进声明（Provider 即 Option）。
func (p stdLoggerProvider) Apply(d *Decl) { d.Providers = append(d.Providers, p) }

// Plan 声明日志节点：阶段 B 引导位（PriorityLogger 先于其它阶段 B 资源）；
// 停机 Effect 名 appkit:bootstrap-logger。
//
// 非 Opaque：期望态完全由 logger 契约段派生（ReloadReconcile + DesiredHash），
// 计划能看到「装配什么」（后端列表/过滤键）——只有后端构造细节是 Provider
// 内部实现，不构成逃逸舱。
func (stdLoggerProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{
		Key:        "logger",
		Kind:       "logger",
		Segments:   []string{"logger"},
		Phase:      PhaseBootstrap,
		Priority:   PriorityLogger,
		Reload:     ReloadReconcile,
		EffectName: "appkit:bootstrap-logger",
		Origin:     OriginStd,
	}}, nil
}

// Build 构造日志后端。产物为 log.Logger（引擎 loggerSource 协议消费；
// cleanup 交 curCleanup 原子换槽——文件/轮转句柄的释放与热更重建不泄漏）。
func (p stdLoggerProvider) Build(_ context.Context, s ApplyScope, _ Node) (any, Cleanup, error) {
	// l==nil：阶段 A（契约装载前）或契约无 logger 段——回退引导 bslog。
	l := s.Config.GetLogger()
	if l == nil {
		return bslog.New(baldbootstrap.LogOptions(nil), p.deco...), nil, nil
	}
	bs := l.GetBackends()
	if len(bs) == 0 {
		return nil, nil, fmt.Errorf("appkit: logger.backends is empty (declare at least one backend, e.g. type \"slog\")")
	}
	loggers := make([]log.Logger, 0, len(bs))
	var cleanups []func()
	for i, b := range bs {
		if b.GetType() == "slog" {
			lg, c := bslog.NewWithCleanup(baldbootstrap.LogOptions(b), p.deco...)
			loggers = append(loggers, lg)
			cleanups = append(cleanups, c)
			continue
		}
		return nil, nil, fmt.Errorf("appkit: logger.backends[%d] type %q: default path supports only \"slog\";\n"+
			"  remote/custom backends need a logger provider:\n"+
			"    appkit.MustServe(ctx, std.Standard(), myLoggerProvider()) // Segments()=[\"logger\"]", i, b.GetType())
	}
	// 单项直通：不包 MultiLogger 广播层（出口零多余包装）；多项经
	// MultiLogger 合并。
	var out log.Logger = loggers[0]
	if len(loggers) > 1 {
		out = log.NewMultiLogger(loggers...)
	}
	// cleanup 按构造序正放释放各 slog 项的文件/轮转句柄（lumberjack 缓冲
	// 冲刷落盘、热更新重建不泄漏——恒非 nil）。
	merged := func() {
		for _, c := range cleanups {
			c()
		}
	}
	return log.NewFilterLogger(out, l.GetFilterKeys()...), noopCleanup(merged), nil
}

// ---------------------------------------------------------------------------
// server Providers：server.http 与 server.grpc 各自一个节点
// ---------------------------------------------------------------------------

// stdServerProvider 拆成两个面 Provider 的原因：段归属是**整 Provider** 级的
// 末者胜（见 provider.go），单一 Provider 同时声明 server.http + server.grpc
// 时，第三方转码面想接管 HTTP 端口就会把 gRPC 面一起挤掉。拆开后「同端口单面
// 的 grpc-gateway 拓扑」只接管 server.http，gRPC 面留在 std——这是 bootstrap
// 不再硬编 grpc-gateway（模块环边）后该拓扑唯一的表达通道。

// StandardHTTPProvider 返回框架内置 HTTP 面 Provider（server.http 段）。
func StandardHTTPProvider() Provider { return stdHTTPProvider{} }

// StandardGRPCProvider 返回框架内置 gRPC 面 Provider（server.grpc 段）。
func StandardGRPCProvider() Provider { return stdGRPCProvider{} }

// stdHTTPProvider 消费 server.http 段：能力声明（WithHTTP）决定构不构面，
// 契约段驱动地址/TLS。
type stdHTTPProvider struct{}

func (stdHTTPProvider) Segments() []string { return []string{"server.http"} }

// Apply 把自身追加进声明（Provider 即 Option，可直接作为 MustServe 变参项）。
func (stdHTTPProvider) Apply(d *Decl) { d.Providers = append(d.Providers, stdHTTPProvider{}) }

// Plan 声明 HTTP 服务器节点：迟后阶段（PhaseServe）——业务 BeforeStart 钩子
// 之后构造，handler 消费业务建好的运行期资源。
//
// 保持 Opaque：server 段只声明地址/TLS/driver，真正装进去的 REST 路由由
// WithHTTP 的 handler 承载，计划看不见；这里的逃逸舱是诚实标记而非待办
// （生成式路由见设计 Q5）。
func (stdHTTPProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{
		Key:      "server.http",
		Kind:     "server",
		Segments: []string{"server.http"},
		Phase:    PhaseServe,
		Reload:   ReloadStatic,
		Opaque:   true,
		Origin:   OriginStd,
	}}, nil
}

// Build 按能力声明构造 HTTP 服务器。
//
// 健康检查默认装配在此落地：探针包在业务 handler 外层（协议实现不注册任何
// 框架路由），见《Bald 健康检查装配设计》。
func (stdHTTPProvider) Build(ctx context.Context, s ApplyScope, _ Node) (any, Cleanup, error) {
	d := s.Decl
	if d.HTTPHandler == nil {
		return nil, nil, nil // 无 HTTP 能力声明：跳过构造
	}
	handler := d.HTTPHandler
	if d.Health != nil {
		handler = withProbes(handler, d.Health, d.ProbeLivePath, d.ProbeReadyPath)
	}
	srv, closer, err := baldbootstrap.HttpServerProvider(
		baldbootstrap.WithHTTPHandler(handler))(ctx, s.Config.GetServer())
	if err != nil {
		return nil, nil, err
	}
	var cleanup Cleanup
	if closer != nil {
		cleanup = func(context.Context) error { closer(); return nil }
	}
	return serverList(srv), cleanup, nil
}

// stdGRPCProvider 消费 server.grpc 段：能力声明（WithGRPC/WithGRPCOptions）
// 决定构不构面，契约段驱动地址/TLS/reflection。
type stdGRPCProvider struct{}

func (stdGRPCProvider) Segments() []string { return []string{"server.grpc"} }

// Apply 把自身追加进声明（Provider 即 Option）。
func (stdGRPCProvider) Apply(d *Decl) { d.Providers = append(d.Providers, stdGRPCProvider{}) }

// Plan 声明 gRPC 服务器节点（与 HTTP 面同阶段；节点序由 Priority/Key 决定，
// gRPC 排在 HTTP 前以保持既有的服务器列表序与停机聚合序）。
func (stdGRPCProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{
		Key:      "server.grpc",
		Kind:     "server",
		Segments: []string{"server.grpc"},
		Phase:    PhaseServe,
		Priority: -1,
		Reload:   ReloadStatic,
		Opaque:   true,
		Origin:   OriginStd,
	}}, nil
}

// Build 按能力声明构造 gRPC 服务器：service 注册 + 选项工厂（按登记顺序）+
// 健康状态。
func (stdGRPCProvider) Build(ctx context.Context, s ApplyScope, _ Node) (any, Cleanup, error) {
	d := s.Decl
	if d.GRPCRegister == nil {
		return nil, nil, nil // 无 gRPC 能力声明：跳过构造
	}
	grpcOpts := []baldbootstrap.GRPCServerOption{
		baldbootstrap.WithGRPCRegister(d.GRPCRegister),
	}
	// 工厂在 server 构造时逐个求值（见 bootstrap.GrpcServerProvider），彼时
	// 业务 beforeStart 已建好认证器；登记顺序即最终链序。
	for _, fn := range d.GRPCOptionFuncs {
		grpcOpts = append(grpcOpts, baldbootstrap.WithGRPCUnaryFunc(fn))
	}
	if d.Health != nil {
		grpcOpts = append(grpcOpts, baldbootstrap.WithGRPCHealth(d.Health, d.HealthInterval))
	}
	srv, closer, err := baldbootstrap.GrpcServerProvider(grpcOpts...)(ctx, s.Config.GetServer())
	if err != nil {
		return nil, nil, err
	}
	var cleanup Cleanup
	if closer != nil {
		cleanup = func(context.Context) error { closer(); return nil }
	}
	return serverList(srv), cleanup, nil
}

// serverList 把单个服务器产物包成既有的 []transport.Server 形态（引擎按切片
// 追加服务器列表；nil server = 契约段缺失，空切片即跳过）。
func serverList(srv transport.Server) []transport.Server {
	if srv == nil {
		return nil
	}
	return []transport.Server{srv}
}

// ---------------------------------------------------------------------------
// audit Provider：audit.backends 的 "log" 项
// ---------------------------------------------------------------------------

// stdAuditProvider 消费 audit 段。
type stdAuditProvider struct{}

func (stdAuditProvider) Segments() []string { return []string{"audit"} }

// Apply 把自身追加进声明（Provider 即 Option）。
func (stdAuditProvider) Apply(d *Decl) { d.Providers = append(d.Providers, stdAuditProvider{}) }

// Plan 声明审计节点：阶段 B 构建（audit_wiring_order_test 钉住它依赖
// database/cache 客户端就绪——PriorityAudit 排在二者之后）；TeardownAfter
// ["*"] 使停机回放最后执行：stream 尾批审计事件在指标/span flush 之后收集。
//
// 非 Opaque：后端列表由 audit 契约段派生（ReloadReconcile + DesiredHash），
// 计划能看到装配选择；store/stream 的第三方实现走用户 Provider（段路径末者
// 胜），其是否 Opaque 由那个 Provider 自己声明。
func (stdAuditProvider) Plan(context.Context, PlanScope) ([]Node, error) {
	return []Node{{
		Key:           "audit",
		Kind:          "audit",
		Segments:      []string{"audit"},
		Phase:         PhaseBootstrap,
		Priority:      PriorityAudit,
		Reload:        ReloadReconcile,
		TeardownAfter: []ResourceKey{"*"},
		EffectName:    "appkit:audit-restore",
		Origin:        OriginStd,
	}}, nil
}

// Build 按契约 audit.backends 列表装配审计后端（多选，列表序即广播序）。
// "log" 内置；其余项 fail-fast 教学报错（std 不含第三方后端——经用户
// audit Provider 接管）。多后端组装、失败逆序回滚收敛在 pkg/audit.Compose。产物为 audit.Auditor，
// 落资源面并作 appkit 自身审计事件的兜底 sink——见 storeProduct。
func (stdAuditProvider) Build(ctx context.Context, s ApplyScope, _ Node) (any, Cleanup, error) {
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
