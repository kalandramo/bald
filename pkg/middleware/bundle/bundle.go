// Package bundle 是横切关注点门面（见 docs/devel/zh-CN/Bald 中间件设计.md）。
//
// 解决的问题：pkg/middleware/{gin,grpc} 的中间件/拦截器若散装手挂，
// 正确链序（Error 最外、Authn→Audit→Authz）只活在文档与纪律里，是最大的
// 「隐性知识税」。Bundle 一次构造、双传输产出、链序由框架固化：
//
//	b := bundle.New(
//	    bundle.Authn(authenticator),   // 依赖实例经构造器注入（全局 audit 兜底已删除）
//	    bundle.Authz(authorizer),
//	    bundle.Audit(auditor),
//	    bundle.Metrics(recorder),
//	    bundle.Normalized(),          // 内置传输中立归一化默认（双命名空间回归从结构上杜绝）
//	)
//	router.Use(b.Gin()...)
//	grpcSrv := server.NewGRPCServerWithRegister(grpcCfg, b.GRPCChain(), register)
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
	"context"
	"errors"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/log"
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

	// normalized 开启传输中立归一化：HTTP 侧接 authz.DefaultHTTPObject/
	// DefaultHTTPAction，gRPC 侧接 authz.DefaultGRPCObject/DefaultGRPCAction，
	// 使 REST 与 gRPC 在同一份 RBAC 策略/审计语义下工作。
	normalized bool

	// public 显式声明「有意不做认证」（见 Public）。与 authenticator==nil 一起
	// 决定认证姿态：两者都没给 = 未决定的公开服务，[Bundle.Verify] 报错。
	public bool

	// gin 专属（gRPC 无对应语义）。
	corsCfg *ginmw.CORSConfig
	secure  bool

	// 契约中间件段的配置化覆盖（零值 = 用既有默认，见 Gin()）。
	recoveryCfg ginmw.RecoveryConfig
	requestID   ginmw.RequestIDConfig
	loggingCfg  ginmw.LoggingConfig
	timeoutCfg  *ginmw.TimeoutConfig

	// tracing 为 true 时挂 span-only 追踪层（契约 tracing 段存在即启用）。
	tracing bool

	// rateLimiter 是配置驱动的限流层（gin 专属）。契约
	// server.http.middleware.rate_limit 经 RateLimitFromMiddleware 构造后经
	// RateLimit 注入；nil 表示未启用。gRPC 链无对应段（契约 Grpc.Middleware
	// 不含 rate_limit）。
	rateLimiter *ginmw.RateLimiter

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

// Normalized 显式开启归一化默认（authz.Default{HTTP,GRPC}{Object,Action}）。
// 归一化默认开启（核心收益——根治 REST/gRPC 双命名空间 RBAC——不应
// 依赖调用方记得 opt-in），本 Option 保留为显式声明（幂等）。
func Normalized() Option { return func(b *Bundle) { b.normalized = true } }

// Public 声明「本服务有意不做认证」。
//
// 为什么需要它：未注入 Authn 时链里就没有认证层（零开销设计的代价），
// 「忘了配」与「有意公开」在链上完全同形，出一个无认证服务器不需要任何
// 决定。声明 Public 把后者变成显式表态，Verify 也因此能区分两者（见 [Bundle.Verify]）。
func Public() Option { return func(b *Bundle) { b.public = true } }

// Raw 关闭归一化，回到原始语义（HTTP object=path、gRPC object=FullMethod），
// 供依赖该语义的存量部署 opt-out（配套）。
func Raw() Option { return func(b *Bundle) { b.normalized = false } }

// CORS 附加 gin 跨域中间件（gRPC 链无对应层）。传 nil 等同不设置。
func CORS(cfg *ginmw.CORSConfig) Option { return func(b *Bundle) { b.corsCfg = cfg } }

// Secure 附加 gin 安全响应头中间件（gRPC 链无对应层）。
func Secure() Option { return func(b *Bundle) { b.secure = true } }

// RateLimit 附加配置驱动的限流层（gin 专属）。rl 通常来自
// [RateLimitFromMiddleware]（契约 server.http.middleware.rate_limit → 中间件）。
// 传 nil 等同不设置（与 CORS 惯例一致：缺省不装配）。
//
// 链序：限流挂在业务 handler 之前、认证/授权之后（见 Gin 的链序说明）——
// 先认证再限流，避免匿名流量耗尽配额拖垮已认证用户。
//
// 生命周期：Bundle 不持有 rl 的关闭职责（与 CORS 同）——调用方在停机时
// 调 rl.Close()（RateLimiter.Close 幂等）。
func RateLimit(rl *ginmw.RateLimiter) Option { return func(b *Bundle) { b.rateLimiter = rl } }

// Recovery 配置 panic 恢复层（契约 middleware.recovery 段）。
// 不设时保留既有默认（同款恢复行为、不含栈）。
func Recovery(cfg ginmw.RecoveryConfig) Option {
	return func(b *Bundle) { b.recoveryCfg = cfg }
}

// RequestID 配置请求 ID 层（契约 middleware.request_id 段）。
// 不设时用默认头名 X-Request-ID。
func RequestID(cfg ginmw.RequestIDConfig) Option {
	return func(b *Bundle) { b.requestID = cfg }
}

// Logging 配置请求日志层（契约 middleware.logging 段，追加跳过路径）。
// 不设时仅内置跳过表。
func Logging(cfg ginmw.LoggingConfig) Option {
	return func(b *Bundle) { b.loggingCfg = cfg }
}

// Timeout 附加请求超时层（契约 middleware.timeout 段）。传 nil 等同不设置。
// 链序：挂在内层（业务 handler 之前、限流之后）——超时应覆盖业务处理本身。
func Timeout(cfg *ginmw.TimeoutConfig) Option {
	return func(b *Bundle) { b.timeoutCfg = cfg }
}

// Tracing 附加 span-only 追踪层（契约 middleware.tracing 段，空 message、存在即
// 启用）。与 Logging 的区别：只起 span/写 trace_id，不写请求日志。二者可并存
// （tracing 在外层，见 Gin 的链序）。
func Tracing() Option { return func(b *Bundle) { b.tracing = true } }

// NoLogging 关闭请求日志/可观测中间件（默认开启）。
func NoLogging() Option { return func(b *Bundle) { b.logging = false } }

// New 构造 Bundle。归一化默认开启——REST/gRPC 共用同一份 RBAC 语义是
// 默认正确行为；需旧原始语义时显式 Raw() opt-out。
//
// 认证姿态不在这里 fail-fast：链是构造期产物、也是零开销省略的载体，构造即
// panic 会让「先起链、再决定」的用法无处安放。姿态由 [Bundle.Verify] 判定，
// [Bundle.Gin]/[Bundle.GRPCInterceptors] 在产出链时对未决定的姿态告警一次。
func New(opts ...Option) *Bundle {
	b := &Bundle{logging: true, normalized: true}
	for _, o := range opts {
		o(b)
	}
	return b
}

// errUndeclaredAuthn 是「认证姿态未决定」的错误（风险 6.1）。
//
// 用 errors.New 而非 berrors：这是给接手者的接线错误，不面向最终用户，也不经
// 传输收口；berrors.Error 的 Error() 刻意不含文案（见 berrors/errors.go），
// 而这里要的正是日志与返回值里都能读出两条出路。
var errUndeclaredAuthn = errors.New("bundle: AUTHN_UNDECLARED —— 未注入 Authn 也未声明 Public：" +
	"链中无认证层，服务等同公开。要么 bundle.Authn(<authenticator>) 接认证，" +
	"要么 bundle.Public() 显式声明有意公开")

// Verify 检查认证姿态是否已被决定：注入了 Authn（有认证层）或显式声明了
// Public（有意公开）二者之一。两者都没给 → 返回错误——此时链里静默地没有
// 认证层，等于一个无认证服务器。
//
// 调用点：业务在 main/装配期调它并 fail-fast（推荐），或交给 Gin/
// GRPCInterceptors 产出链时告警（见 warnIfUndeclaredAuthn）。
func (b *Bundle) Verify() error {
	if b.authenticator != nil || b.public {
		return nil
	}
	return errUndeclaredAuthn
}

// warnIfUndeclaredAuthn 在产出链时对未决定的认证姿态告警一次。
func (b *Bundle) warnIfUndeclaredAuthn() {
	if err := b.Verify(); err != nil {
		log.Warn(context.Background(), err.Error())
	}
}

// Gin 返回链序固化的 gin 中间件链（外→内）：
//
//	Recovery → RequestID → Logging → CORS → Secure → Authn → Audit → Authz
//
// 未注入的依赖对应层直接省略（零开销），如未设 Authn 则链中无认证层——但
// 前提是该姿态已被决定（Authn 或 Public，见 [Bundle.Verify]），否则这里先告警。
func (b *Bundle) Gin() []gin.HandlerFunc {
	b.warnIfUndeclaredAuthn()
	// 契约段的 chain 位置（外→内）：
	//   Recovery → RequestID → Tracing → Logging → CORS → Secure → Authn → Audit
	//   → Authz → RateLimit → Timeout
	// Tracing 在 Logging 之外：若二者并存，logging 起的 span 成为 tracing span 的
	// child（外层先起的 span 是父）；只追踪不打日志时就只挂 Tracing。
	chain := []gin.HandlerFunc{ginmw.Recovery(b.recoveryCfg), ginmw.RequestIDMiddleware(b.requestID)}
	if b.tracing {
		chain = append(chain, ginmw.Tracing())
	}
	if b.logging {
		chain = append(chain, ginmw.Logging(b.loggingCfg))
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
	if b.rateLimiter != nil {
		// 限流在授权之后、超时之前：目标是对「业务操作」限流，且天然可见认证后
		// 的 subject（为将来按用户限流留位）。代价是它不保护认证/授权层自身——
		// 若需在认证前限流以保护认证层，业务可自行把 ginmw.RateLimit 挂到链首。
		chain = append(chain, b.rateLimiter.Handler())
	}
	if b.timeoutCfg != nil {
		// 超时在最内层（业务 handler 之前）：只计业务处理耗时，不把上层中间件
		// 的开销算进超时预算。
		chain = append(chain, ginmw.Timeout(*b.timeoutCfg))
	}
	return chain
}

// GRPCInterceptors 返回链序固化的 unary 拦截器链（slice 序即外→内）：
//
//	Error → Recovery → RequestID → Observability → Authn → Audit → Authz
//
// Recovery 紧随 Error（第二外层）：handler/内层拦截器 panic 被捕获并转为
// *berrors.Internal，由最外层 Error 收口为 gRPC status（与 gin 链首道
// Recovery 对称）。
//
// 需自行组装 *grpc.Server 时用：
//
//	grpc.NewServer(grpc.ChainUnaryInterceptor(b.GRPCInterceptors()...))
//
// 与 Gin 一样：认证姿态未决定（既无 Authn 也未 Public）时先告警再出链。
func (b *Bundle) GRPCInterceptors() []grpc.UnaryServerInterceptor {
	b.warnIfUndeclaredAuthn()
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
// authn 层显式发出（与请求审计同一后端）。
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

// ginAuthz 组装 gin 授权中间件（按需注入归一化 resolver）。
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
// 语义是完全显式注入，不依赖任何全局审计句柄，避免测试间全局污染）。
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

// grpcAuthz 组装 gRPC 授权拦截器（按需注入归一化 resolver）。
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
