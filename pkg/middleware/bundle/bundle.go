// Package bundle 是横切关注点门面（P10，见 docs/devel/zh-CN/架构优化路线.md）。
//
// 解决的问题：pkg/middleware/{gin,grpc} 的中间件/拦截器此前只能散装手挂，
// 正确链序（Error 最外、Authn→Audit→Authz）只活在文档与纪律里，是坑清单中
// 最大的「隐性知识税」。Bundle 一次构造、双传输产出、链序由框架固化：
//
//	b := bundle.New(
//	    bundle.Authn(authenticator),   // 依赖实例经构造器注入（SetAuditor 等全局退化为兜底）
//	    bundle.Authz(authorizer),
//	    bundle.Audit(auditor),
//	    bundle.Metrics(recorder),
//	    bundle.Normalized(),          // 内置 P9 归一化默认（双命名空间回归从结构上杜绝）
//	)
//	router.Use(b.Gin()...)
//	grpcSrv := server.NewGRPCServerWithRegister(grpcCfg, b.GRPCChain(), register, ready)
//
// 零破坏：散装手挂路径完整保留，Bundle 只是预组装。
//
// 链序契约（由测试固化，见 bundle_test.go）：
//   - gin（注册序即外→内）：Recovery → RequestID → Logging → CORS → Secure →
//     Authn → Audit → Authz
//   - gRPC（slice 序即外→内）：Error → Recovery → RequestID → Observability →
//     Authn → Audit → Authz
//
// 为什么 Audit 夹在 Authn 与 Authz 之间：
//   - 在 Authn 内侧（注册序靠后）才能从 ctx 读到 Authn 注入的 subject/tenant；
//   - 在 Authz 外侧（注册序靠前）才能在 c.Next()/handler 返回后捕获 deny 的
//     最终 result。
//
// 为什么 Error 必须 gRPC 最外层：handler/内层拦截器返回的 *berrors.Error 若不经
// grpcerr.ToStatus 收口，会被 status.Convert 兜底成空 Unknown（见
// pkg/middleware/grpc/error.go 注释）。
package bundle

import (
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/metrics"
	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
)

// Bundle 是双传输横切关注点门面。构造后只读，可安全共享。
type Bundle struct {
	authenticator authn.Authenticator
	authorizer    authz.Authorizer
	auditor       audit.Auditor
	recorder      metrics.Recorder

	// normalized 开启 P9 传输中立归一化：HTTP 侧接 authz.DefaultHTTPObject/
	// DefaultHTTPAction，gRPC 侧接 authz.DefaultGRPCObject/DefaultGRPCAction，
	// 使 REST 与 gRPC 在同一份 RBAC 策略/审计语义下工作。
	normalized bool

	// gin 专属（gRPC 无对应语义）。
	corsCfg *ginmw.CORSConfig
	secure  bool

	// logging 控制请求日志/可观测中间件（默认开启）。
	logging bool
}

// Option 配置 Bundle。
type Option func(*Bundle)

// Authn 注入认证器（Authenticator）。不设置则 gin/gRPC 链中无认证层（等同公开服务）。
func Authn(a authn.Authenticator) Option { return func(b *Bundle) { b.authenticator = a } }

// Authz 注入授权器（Authorizer）。不设置则无授权层。
func Authz(a authz.Authorizer) Option { return func(b *Bundle) { b.authorizer = a } }

// Audit 注入审计后端（Auditor）。与 Metrics 至少设一项才会挂审计层。
func Audit(a audit.Auditor) Option { return func(b *Bundle) { b.auditor = a } }

// Metrics 注入指标记录器（Recorder）。与 Audit 至少设一项才会挂审计层
// （审计中间件是指标埋点的载体，两者同源 emit）。
func Metrics(r metrics.Recorder) Option { return func(b *Bundle) { b.recorder = r } }

// Normalized 显式开启 P9 归一化默认（authz.Default{HTTP,GRPC}{Object,Action}）。
// D7 起归一化默认开启（P9 的核心收益——根治 REST/gRPC 双命名空间 RBAC——不应
// 依赖调用方记得 opt-in），本 Option 保留为显式声明（幂等）。
func Normalized() Option { return func(b *Bundle) { b.normalized = true } }

// Raw 关闭归一化，回到 P7 原始语义（HTTP object=path、gRPC object=FullMethod），
// 供依赖旧双命名空间语义的存量部署 opt-out（D7 配套）。
func Raw() Option { return func(b *Bundle) { b.normalized = false } }

// CORS 附加 gin 跨域中间件（gRPC 链无对应层）。传 nil 等同不设置。
func CORS(cfg *ginmw.CORSConfig) Option { return func(b *Bundle) { b.corsCfg = cfg } }

// Secure 附加 gin 安全响应头中间件（gRPC 链无对应层）。
func Secure() Option { return func(b *Bundle) { b.secure = true } }

// NoLogging 关闭请求日志/可观测中间件（默认开启）。
func NoLogging() Option { return func(b *Bundle) { b.logging = false } }

// New 构造 Bundle。归一化默认开启（D7）——REST/gRPC 共用同一份 RBAC 语义是
// 默认正确行为；需旧原始语义时显式 Raw() opt-out。
func New(opts ...Option) *Bundle {
	b := &Bundle{logging: true, normalized: true}
	for _, o := range opts {
		o(b)
	}
	return b
}

// Gin 返回链序固化的 gin 中间件链（外→内）：
//
//	Recovery → RequestID → Logging → CORS → Secure → Authn → Audit → Authz
//
// 未注入的依赖对应层直接省略（零开销），如未设 Authn 则链中无认证层。
func (b *Bundle) Gin() []gin.HandlerFunc {
	chain := []gin.HandlerFunc{ginmw.Recovery(), ginmw.RequestIDMiddleware()}
	if b.logging {
		chain = append(chain, ginmw.Logging())
	}
	if b.corsCfg != nil {
		chain = append(chain, ginmw.CORS(b.corsCfg))
	}
	if b.secure {
		chain = append(chain, ginmw.Secure())
	}
	if b.authenticator != nil {
		chain = append(chain, b.ginAuthn())
	}
	if b.auditor != nil || b.recorder != nil {
		chain = append(chain, b.ginAudit())
	}
	if b.authorizer != nil {
		chain = append(chain, b.ginAuthz())
	}
	return chain
}

// GRPCInterceptors 返回链序固化的 unary 拦截器链（slice 序即外→内）：
//
//	Error → Recovery → RequestID → Observability → Authn → Audit → Authz
//
// Recovery 紧随 Error（第二外层）：handler/内层拦截器 panic 被捕获并转为
// *berrors.Internal，由最外层 Error 收口为 gRPC status（与 gin 链首道
// Recovery 对称，D5）。
//
// 需自行组装 *grpc.Server 时用：
//
//	grpc.NewServer(grpc.ChainUnaryInterceptor(b.GRPCInterceptors()...))
func (b *Bundle) GRPCInterceptors() []grpc.UnaryServerInterceptor {
	chain := []grpc.UnaryServerInterceptor{grpcmw.ErrorInterceptor()} // 必须最外层
	chain = append(chain, grpcmw.RecoveryInterceptor())               // 第二外层：panic 兜底
	chain = append(chain, grpcmw.RequestIDInterceptor())
	if b.logging {
		chain = append(chain, grpcmw.UnaryObservability())
	}
	if b.authenticator != nil {
		chain = append(chain, b.grpcAuthn())
	}
	if b.auditor != nil || b.recorder != nil {
		chain = append(chain, b.grpcAudit())
	}
	if b.authorizer != nil {
		chain = append(chain, b.grpcAuthz())
	}
	return chain
}

// GRPCChain 返回可直接传给 server.NewGRPCServerWithRegister 的 []grpc.ServerOption
// （内含 ChainUnaryInterceptor 装配的整条链）。
func (b *Bundle) GRPCChain() []grpc.ServerOption {
	return []grpc.ServerOption{grpc.ChainUnaryInterceptor(b.GRPCInterceptors()...)}
}

// ginAuthn 组装 gin 认证中间件：把 bundle 的 auditor 注入 authn 层——审计中间件
// 注册在 Authn 内侧，认证失败 abort 后不再执行，authn abort 路径的审计事件由
// authn 层显式发出（与请求审计同一后端，D3）。
func (b *Bundle) ginAuthn() gin.HandlerFunc {
	var opts []ginmw.AuthnOption
	if b.auditor != nil {
		opts = append(opts, ginmw.AuthnWithAuditor(b.auditor))
	}
	return ginmw.AuthnMiddleware(b.authenticator, opts...)
}

// grpcAuthn 组装 gRPC 认证拦截器（auditor 注入语义同 ginAuthn）。
func (b *Bundle) grpcAuthn() grpc.UnaryServerInterceptor {
	var opts []grpcmw.AuthnOption
	if b.auditor != nil {
		opts = append(opts, grpcmw.AuthnWithAuditor(b.auditor))
	}
	return grpcmw.AuthnInterceptor(b.authenticator, opts...)
}

// ginAuthz 组装 gin 授权中间件（按需注入 P9 归一化 resolver）。
func (b *Bundle) ginAuthz() gin.HandlerFunc {
	var opts []ginmw.AuthzOption
	if b.normalized {
		opts = append(opts,
			ginmw.WithObjectResolver(authz.DefaultHTTPObject),
			ginmw.WithActionResolver(authz.DefaultHTTPAction),
		)
	}
	return ginmw.AuthzMiddleware(b.authorizer, opts...)
}

// ginAudit 组装 gin 审计中间件。auditor 未注入时显式接 NopAuditor（Bundle
// 语义是完全显式注入，不吃 audit.GetAuditor() 全局兜底，避免测试间全局污染）。
func (b *Bundle) ginAudit() gin.HandlerFunc {
	var opts []ginmw.AuditOption
	if b.normalized {
		opts = append(opts,
			ginmw.AuditWithObjectResolver(authz.DefaultHTTPObject),
			ginmw.AuditWithActionResolver(authz.DefaultHTTPAction),
		)
	}
	if b.auditor != nil {
		opts = append(opts, ginmw.AuditWithAuditor(b.auditor))
	} else {
		opts = append(opts, ginmw.AuditWithAuditor(audit.NopAuditor()))
	}
	if b.recorder != nil {
		opts = append(opts, ginmw.AuditWithMetrics(b.recorder))
	}
	return ginmw.AuditMiddleware(opts...)
}

// grpcAuthz 组装 gRPC 授权拦截器（按需注入 P9 归一化 resolver）。
func (b *Bundle) grpcAuthz() grpc.UnaryServerInterceptor {
	var opts []grpcmw.AuthzOption
	if b.normalized {
		opts = append(opts,
			grpcmw.WithObjectResolver(authz.DefaultGRPCObject),
			grpcmw.WithActionResolver(authz.DefaultGRPCAction),
		)
	}
	return grpcmw.AuthzInterceptor(b.authorizer, opts...)
}

// grpcAudit 组装 gRPC 审计拦截器。auditor 未注入时显式接 NopAuditor（同 ginAudit）。
func (b *Bundle) grpcAudit() grpc.UnaryServerInterceptor {
	var opts []grpcmw.AuditOption
	if b.normalized {
		opts = append(opts,
			grpcmw.AuditWithObjectResolver(authz.DefaultGRPCObject),
			grpcmw.AuditWithActionResolver(authz.DefaultGRPCAction),
		)
	}
	if b.auditor != nil {
		opts = append(opts, grpcmw.AuditWithAuditor(b.auditor))
	} else {
		opts = append(opts, grpcmw.AuditWithAuditor(audit.NopAuditor()))
	}
	if b.recorder != nil {
		opts = append(opts, grpcmw.AuditWithMetrics(b.recorder))
	}
	return grpcmw.AuditInterceptor(opts...)
}
