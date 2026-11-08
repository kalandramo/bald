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
//     Authn → Audit → Authz → RateLimit → CircuitBreaker → Timeout
//   - gRPC（slice 序即外→内）：Error → Recovery → RequestID → Observability →
//     Authn → Audit → Authz → RateLimit → CircuitBreaker → Timeout
//
// 两条链的 RateLimit/CircuitBreaker/Timeout 由同一组 Option 驱动（零开销省略），
// 面不对称已经收口；gRPC 面另有 GRPCRateLimit/GRPCCircuitBreaker/GRPCTimeout
// 三个只作用于自己的 Option，对应契约 server.grpc.middleware 的同名段（给了它们，
// gRPC 换用自己的桶/熔断器/预算，gin 面照旧用 RateLimit/CircuitBreaker/Timeout——
// 见这几个 Option 的注释）。
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
	"time"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/metrics"
	"github.com/kalandramo/bald/pkg/middleware"
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

	// rateLimiter 是配置驱动的限流层，gin 与 gRPC 共用同一个 limiter
	// （gRPC 侧经 Limiter() 取底层实例，见 GRPCInterceptors）。契约
	// server.http.middleware.rate_limit 经 RateLimitFromMiddleware 构造后由
	// RateLimit 注入；nil 表示两条链都不启用。
	rateLimiter *ginmw.RateLimiter

	// grpcRateLimiter / grpcTimeout 只作用于 gRPC 两面（契约
	// server.grpc.middleware 的同名段经 FromGRPCMiddleware 注入）。非 nil /
	// >0 时**替换** gRPC 面上 rateLimiter / timeoutCfg 的位置，gin 面不受影响。
	// 见 [Bundle.grpcLimiter] / [Bundle.grpcBudget]。
	grpcRateLimiter *ginmw.RateLimiter
	grpcTimeout     time.Duration

	// circuitBreaker 是配置驱动的熔断层。与 rateLimiter 同款「一面给全部」：
	// 不另给 [GRPCCircuitBreaker] 时 gRPC unary 复用同一个熔断器实例（同一份
	// 滑窗）。契约 server.http.middleware.circuit_breaker 经
	// [CircuitBreakerFromMiddleware] 构造后由本 Option 注入；nil = 两面都不挂。
	circuitBreaker *ginmw.CircuitBreaker

	// grpcCircuitBreaker 只作用于 gRPC unary（契约 server.grpc.middleware
	// .circuit_breaker）。非 nil 时**替换** gRPC 面上 circuitBreaker 的位置，
	// gin 面不受影响。见 [Bundle.grpcBreaker]。
	grpcCircuitBreaker *ginmw.CircuitBreaker

	// logging 控制请求日志/可观测中间件（默认开启）。
	logging bool

	// unifiedCfg 是契约统一中间件段（server.unified_middleware，方案 S）：
	// 经 WithUnifiedMiddleware 注入，三座链头预置统一链。nil = 段缺省，不挂。
	unifiedCfg *bootstrapv1.Server_UnifiedMiddleware

	// unifiedValidator 是统一段 validate 策略的校验回调（代码依赖，实例注入）。
	unifiedValidator middleware.MessageValidator
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

// RateLimit 附加配置驱动的限流层（gin 与 gRPC 共用同一个 limiter）。rl 通常来自
// [RateLimitFromMiddleware]（契约 server.http.middleware.rate_limit → 中间件）。
// 传 nil 等同不设置（与 CORS 惯例一致：缺省不装配）。
//
// 链序：限流挂在业务 handler 之前、认证/授权之后（见 Gin 的链序说明）——
// 先认证再限流，避免匿名流量耗尽配额拖垮已认证用户。gRPC 链同序。
//
// 生命周期：Bundle 不持有 rl 的关闭职责（与 CORS 同）——调用方在停机时
// 调 rl.Close()（RateLimiter.Close 幂等）。
//
// 与 [GRPCRateLimit] 的关系：本 Option 是「一面给全部」——不另给 GRPCRateLimit
// 时，gRPC 的 unary 与 stream 都从同一个桶扣配额。
func RateLimit(rl *ginmw.RateLimiter) Option { return func(b *Bundle) { b.rateLimiter = rl } }

// GRPCRateLimit 附加**只作用于 gRPC**（unary + stream 两面）的限流层。rl 通常
// 来自 [GRPCRateLimitFromMiddleware]（契约 server.grpc.middleware.rate_limit）。
// 传 nil 等同不设置。
//
// 为什么要有它：契约的 rate_limit 段原本只有 server.http 下有，「给 gRPC 配
// 限流」只能靠代码注入，而 gRPC 与 HTTP 的流量形状本就不同（一条长流 vs 一次
// 短请求），共用一个桶时无法各自设容量。
//
// 覆盖关系（这是本 Option 的全部意义，务必看清）：
//   - 只给 [RateLimit]：gin 与 gRPC 共用那个 limiter（既有行为，不变）。
//   - 只给 GRPCRateLimit：gRPC 两面用它，**gin 面没有限流**——各面独立，
//     不互相兜底。
//   - 两个都给：gRPC 用 GRPCRateLimit（更具体的段管更具体的面），gin 用
//     RateLimit，两个桶互不影响。不存在「后写的 Option 静默盖掉前一个」。
//
// stream 面的计费口径（每条流建立扣一个令牌）见 [Bundle.GRPCStreamInterceptors]。
func GRPCRateLimit(rl *ginmw.RateLimiter) Option {
	return func(b *Bundle) { b.grpcRateLimiter = rl }
}

// CircuitBreaker 附加配置驱动的熔断层（gin 与 gRPC unary 共用同一个熔断器）。
// cb 通常来自 [CircuitBreakerFromMiddleware]（契约
// server.http.middleware.circuit_breaker → 中间件）。传 nil 等同不设置。
//
// 它与限流的分工：限流判「这个主体还有没有配额」，熔断判「这个服务现在值不
// 值得真做」——后者只在下游真的在坏的时候开窗，所以两层的拒绝码不同（429/
// RESOURCE_EXHAUSTED vs 503/UNAVAILABLE）。
//
// 链序：授权之后、限流之内、超时之外（见 [Bundle.Gin] 的链序说明）。三条判据
// 少一条都会让这一层失真：
//   - 更外（认证之前）：匿名流量就能把失败率打满、替所有人开窗；
//   - 更内（限流之前）：被限流拒掉的请求会先进熔断，两层的拒绝互相喂养；
//   - 更内（超时之内）：内层 Timeout 写出的 503 就在这一层之下，熔断只会看到
//     「handler 都成功返回」——而「handler 总是超时」正是熔断该抓的形状。
//
// 生命周期：Bundle 不持有 cb 的关闭职责（与 [RateLimit] 同）——调用方在停机时
// 调 cb.Close()（幂等）。注意 hystrix 的 Close 之后本层对**所有**请求回开态
// 状态码，所以 closer 只在服务已停止接收流量之后才有意义。
//
// 与 [GRPCCircuitBreaker] 的关系同 [RateLimit]：不给后者时，gRPC unary 从同一个
// 熔断器进出（HTTP 面的 5xx 与 gRPC 面的服务侧错误一起计入那份滑窗）。
func CircuitBreaker(cb *ginmw.CircuitBreaker) Option {
	return func(b *Bundle) { b.circuitBreaker = cb }
}

// GRPCCircuitBreaker 附加**只作用于 gRPC unary** 的熔断层。cb 通常来自
// [GRPCCircuitBreakerFromMiddleware]（契约 server.grpc.middleware.circuit_breaker）。
// 传 nil 等同不设置。
//
// 为什么要有它、覆盖关系是什么，与 [GRPCRateLimit] 逐条相同：只给 [CircuitBreaker]
// → 两面共用一个熔断器；只给它 → gin 面没有熔断；两个都给 → 各用自己的实例、
// 各判各的失败率，不存在「后写的 Option 静默盖掉前一个」。
//
// **stream 面不挂熔断**，且不是漏掉：一条流的「失败」没有一致答案（按建流算则
// 开窗与真实退化无关，按流终态算则一条长流会长期占住半开探针、把其它流全拒）。
// 见 [Bundle.GRPCStreamInterceptors] 与 grpcmw.CircuitBreakerInterceptor。
func GRPCCircuitBreaker(cb *ginmw.CircuitBreaker) Option {
	return func(b *Bundle) { b.grpcCircuitBreaker = cb }
}

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

// Timeout 附加请求超时层（契约 middleware.timeout 段；段本身在 server.http 下，
// 未另给 [GRPCTimeout] 时 gRPC unary 同样吃这份配置里的 default_timeout_ms，其
// status_code 字段是 HTTP 专属、gRPC 无对应落点）。传 nil 等同两条链都不挂。
// 链序：挂在最内层（业务 handler 之前）——超时应覆盖业务处理本身。
//
// DefaultTimeoutMs <= 0 时两面的既有语义不同，这里都不去动它：gin 侧照旧挂上
// 一个「立即超时」的层（配置写了段却没给时长在 HTTP 面就是 503，属既定行为），
// gRPC 侧省略该层。要两面对齐得先决定是否给 TimeoutConfig 加 Validate，那是
// 独立的 API 决策。
func Timeout(cfg *ginmw.TimeoutConfig) Option {
	return func(b *Bundle) { b.timeoutCfg = cfg }
}

// GRPCTimeout 附加**只作用于 gRPC unary** 的服务端默认超时。d <= 0 等同不设置。
// 通常来自 FromGRPCMiddleware（契约 server.grpc.middleware.timeout）。
//
// 为什么参数是 time.Duration 而不是 *ginmw.TimeoutConfig：TimeoutConfig 里的
// status_code 在 gRPC 上没有落点，把它端给 gRPC 配置只会多一个「写了不生效」
// 的字段（见 [Timeout]）。
//
// 语义与 gin 侧不同，别当同一个东西：本层只给 handler 一个会到期的 ctx，
// 不缩短调用方已带的 deadline，也不硬性提前返回响应（见 grpcmw.
// TimeoutInterceptor）。**stream 面不挂超时**——流的生命周期就是整条流，
// 把请求级预算套上去会静默变成「流的最大存活时长」，长订阅到点被砍且没有
// 可区分的错误（见 [Bundle.GRPCStreamInterceptors]）。
//
// 与 [Timeout] 的覆盖关系同 GRPCRateLimit：两个都给时 gRPC 用本 Option 的值，
// gin 用 Timeout 的值；只给本 Option 时 gin 面没有超时层。
func GRPCTimeout(d time.Duration) Option {
	return func(b *Bundle) { b.grpcTimeout = d }
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
//
// Deprecated: 旧协议耦合链门面，方案 S 批次 4 移除。请改用统一中间件契约
// （[WithUnifiedMiddleware] 声明 + middleware.GinHandler 桥接）：横切件写一遍、
// 路由级挂接进契约。本方法保留一个版本期供血，内部已在链头预置统一段。
func (b *Bundle) Gin() []gin.HandlerFunc {
	b.warnIfUndeclaredAuthn()
	// 统一段（方案 S）：双面链头预置统一链（opt-in，段缺省不挂）。规则错误
	// fail-fast——声明了却静默不生效是最坏结果。
	matcher, err := b.unifiedMatcher()
	if err != nil {
		panic(err)
	}
	chain, err := prependUnifiedGin(nil, matcher)
	if err != nil {
		panic(err)
	}
	// 契约段的 chain 位置（外→内）：
	//   [统一段规则] → Recovery → RequestID → Tracing → Logging → CORS → Secure
	//   → Authn → Audit → Authz → RateLimit → CircuitBreaker → Timeout
	// Tracing 在 Logging 之外：若二者并存，logging 起的 span 成为 tracing span 的
	// child（外层先起的 span 是父）；只追踪不打日志时就只挂 Tracing。
	chain = append(chain, ginmw.Recovery(b.recoveryCfg), ginmw.RequestIDMiddleware(b.requestID))
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
	if b.circuitBreaker != nil {
		// 限流之内、超时之外：配额先拒（便宜），熔断只判「这次还值不值得真做」；
		// 而内层 Timeout 写出的 503 必须被这一层记成失败——「handler 总是超时」
		// 正是熔断该抓的形状。代价与限流同：不保护认证/授权层自身。
		chain = append(chain, b.circuitBreaker.Handler())
	}
	if b.timeoutCfg != nil {
		// 超时在最内层（业务 handler 之前）：只计业务处理耗时，不把上层中间件
		// 的开销算进超时预算。
		chain = append(chain, ginmw.Timeout(*b.timeoutCfg))
	}
	return chain
}

// grpcLimiter 返回 gRPC 两面实际使用的限流器：[GRPCRateLimit] 优先，否则回落到
// [RateLimit]（那时与 gin 同桶）。两者都没给返回 nil = 不挂层。
func (b *Bundle) grpcLimiter() *ginmw.RateLimiter {
	if b.grpcRateLimiter != nil {
		return b.grpcRateLimiter
	}
	return b.rateLimiter
}

// grpcBudget 返回 gRPC unary 实际使用的超时预算：[GRPCTimeout] 优先（<=0 视为
// 未设置，回落），否则取 [Timeout] 的 default_timeout_ms。返回 0 = 不挂层。
// gin 侧非正值仍挂「立即 503」的层，那是 [Timeout] 记下的既有差异，本函数不管。
func (b *Bundle) grpcBudget() time.Duration {
	if b.grpcTimeout > 0 {
		return b.grpcTimeout
	}
	if b.timeoutCfg == nil || b.timeoutCfg.DefaultTimeoutMs <= 0 {
		return 0
	}
	return time.Duration(b.timeoutCfg.DefaultTimeoutMs) * time.Millisecond
}

// GRPCInterceptors 返回链序固化的 unary 拦截器链（slice 序即外→内）：
//
//	Error → Recovery → RequestID → Observability → Authn → Audit → Authz
//	→ RateLimit → CircuitBreaker → Timeout
//
// Recovery 紧随 Error（第二外层）：handler/内层拦截器 panic 被捕获并转为
// *berrors.Internal，由最外层 Error 收口为 gRPC status（与 gin 链首道
// Recovery 对称）。
//
// RateLimit/CircuitBreaker/Timeout 与 gin 侧同序：都只在对应 Option 给了之后才挂
// （零开销省略），限流在授权之后、超时在最内层，熔断夹在两者之间。默认两面同源
// （同一个 limiter、同一个熔断器、同一份 default_timeout_ms），给了
// GRPCRateLimit/GRPCCircuitBreaker/GRPCTimeout 则 gRPC 用自己的那份。
// 两面的语义差别：gin 的 Timeout 会硬性提前返回 503，gRPC 的只给 handler 一个
// 会到期的 ctx（见 grpcmw.TimeoutInterceptor）；gin 的拒绝是 429 / 503，gRPC 的
// 是 RESOURCE_EXHAUSTED / UNAVAILABLE；gin 的熔断按响应码 >=500 判失败，gRPC 的
// 按 status code 集合判（见 grpcmw.IsFailureStatus）。
//
// 需自行组装 *grpc.Server 时用：
//
//	grpc.NewServer(grpc.ChainUnaryInterceptor(b.GRPCInterceptors()...))
//
// 与 Gin 一样：认证姿态未决定（既无 Authn 也未 Public）时先告警再出链。
//
// Deprecated: 旧协议耦合链门面，方案 S 批次 4 移除。请改用统一中间件契约
// （[WithUnifiedMiddleware] 声明 + middleware.UnaryServerInterceptor 桥接）：
// 横切件写一遍、路由级挂接进契约。本方法保留一个版本期供血，内部已在链头预置统一段。
func (b *Bundle) GRPCInterceptors() []grpc.UnaryServerInterceptor {
	b.warnIfUndeclaredAuthn()
	// 统一段（方案 S）：双面链头预置统一链（opt-in，段缺省不挂）。
	matcher, err := b.unifiedMatcher()
	if err != nil {
		panic(err)
	}
	chain, err := prependUnifiedUnary(nil, matcher)
	if err != nil {
		panic(err)
	}
	chain = append(chain, grpcmw.ErrorInterceptor())    // 必须最外层
	chain = append(chain, grpcmw.RecoveryInterceptor()) // 第二外层：panic 兜底
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
	if rl := b.grpcLimiter(); rl != nil {
		// 与 gin 同序：授权之后、超时之前。默认与 gin 共用同一个 limiter
		// （gin 侧的 RateLimiter 把底层 limiter 经 Limiter() 暴露出来），
		// 给了 GRPCRateLimit 才换成 gRPC 自己的桶。
		chain = append(chain, grpcmw.RateLimitInterceptor(rl.Limiter(), rl.Wait()))
	}
	if br := b.grpcBreaker(); br != nil {
		// 与 gin 同序：限流之后、超时之前。默认与 gin 共用同一个熔断器
		// （给了 GRPCCircuitBreaker 才换成 gRPC 自己的实例）。失败口径由
		// status code 判，与 HTTP 面的 >=500 不同（见 grpcmw.failureCodes）；
		// 拒绝码是 UNAVAILABLE 而不是 503——那是 gRPC 里「服务暂时不可用」
		// 的唯一正确表达，也是客户端库会重试的那个 code。
		chain = append(chain, grpcmw.CircuitBreakerInterceptor(br.Breaker()))
	}
	if d := b.grpcBudget(); d > 0 {
		// 最内层。gRPC 侧的超时只约束 handler 可见的 ctx（不硬中断响应），
		// 与 gin 的 Timeout 语义差别写在 grpcmw.TimeoutInterceptor 的注释里。
		// StatusCode 是 HTTP 专属字段，这里无从落地——gRPC 用 status code。
		chain = append(chain, grpcmw.TimeoutInterceptor(d))
	}
	return chain
}

// grpcBreaker 返回 gRPC unary 实际使用的熔断器：[GRPCCircuitBreaker] 优先，
// 否则回落到 [CircuitBreaker]（那时与 gin 同一个实例、同一份滑窗）。两者都没给
// 返回 nil = 不挂层。与 [Bundle.grpcLimiter] 同款回落，两面不会出现在 HTTP 已
// 开窗、gRPC 还裸奔的半边漏风形状。
//
// stream 面不经本函数取熔断器——理由写在 [Bundle.GRPCStreamInterceptors] 与
// grpcmw.CircuitBreakerInterceptor 的注释里（一条流的失败没有一致答案）。
func (b *Bundle) grpcBreaker() *ginmw.CircuitBreaker {
	if b.grpcCircuitBreaker != nil {
		return b.grpcCircuitBreaker
	}
	return b.circuitBreaker
}

// GRPCStreamInterceptors 返回 stream 面的拦截器链（外→内），与
// [Bundle.GRPCInterceptors] 共用同一份依赖：
//
//	Error → RateLimit（仅当 gRPC 面有限流器：RateLimit 或 GRPCRateLimit）
//
// 只有这两层，且这是刻意的口径，三条都得知道：
//   - **Error 在最外层不是可选的**：*berrors.Error 没实现 `GRPCStatus()`，
//     grpc-go 对流返回的 error 做 `status.Convert` 会把 Code/Reason 全丢成
//     `Unknown`。少了它，被限流的调用方看不出自己是「被限流」而不是「服务坏了」。
//   - **限流按「每条流建立一次」计费**，流内后续消息不再消耗令牌。它挡的是
//     建流洪水，挡不了一条流里狂发（见 grpcmw.RateLimitStreamInterceptor）。
//     用的是 [Bundle.grpcLimiter]，与 unary 面同一个桶，所以 gRPC 的配额不会被
//     两条链各扣一份。
//   - **超时不在这里**：stream handler 的生命周期就是整条流，把 unary 式的
//     默认超时套上去会把它静默变成「流的最大存活时长」，长订阅/watch 到点被砍
//     且没有可区分的错误。要这个语义请显式挂
//     grpcmw.TimeoutStreamInterceptor（自己组 grpc.ChainStreamInterceptor）。
//   - 认证/授权/审计**不在 stream 链上**——本包的 Authn/Authz/Audit 只有 unary
//     形态（stream 版拦截器至今不存在）。这是 §6.2 记录下来的既有边界，不是
//     这次引入的：此前 stream 面整条链都是空的，同样没有认证。
//   - **熔断也不在 stream 链上**，且这一条是新决定而非既有边界：契约
//     server.grpc.middleware.circuit_breaker 只作用于 unary。一条流的失败没有
//     一致答案（见 grpcmw.CircuitBreakerInterceptor），而限流按「建流一次」计费
//     至少还有个明确形状。
//
// 返回空 slice 表示没有任何 stream 层需要挂，[Bundle.GRPCChain] 据此省略
// ChainStreamInterceptor 这个 ServerOption，保持零配置时的选项数不变。
func (b *Bundle) GRPCStreamInterceptors() []grpc.StreamServerInterceptor {
	// 统一段（方案 S）：流面链头也预置统一链（opt-in）——流上的 recovery/
	// request_id 等经 wrapped stream ctx 生效；rate_limit 按「建流一次」计费。
	matcher, err := b.unifiedMatcher()
	if err != nil {
		panic(err)
	}
	chain, err := prependUnifiedStream(nil, matcher)
	if err != nil {
		panic(err)
	}

	rl := b.grpcLimiter()
	if rl != nil {
		chain = append(chain,
			grpcmw.ErrorStreamInterceptor(), // 必须最外层：收口内层产生的 *berrors.Error
			grpcmw.RateLimitStreamInterceptor(rl.Limiter(), rl.Wait()),
		)
	}
	return chain
}

// GRPCChain 返回可直接传给 server.NewGRPCServerWithRegister 的 []grpc.ServerOption
// （内含 ChainUnaryInterceptor 装配的整条 unary 链）。
//
// gRPC 面有限流器（RateLimit 或 GRPCRateLimit）时额外附带一条
// ChainStreamInterceptor（内容见 [Bundle.GRPCStreamInterceptors]）；没有时选项
// 数量与以前完全一致，现有部署不受影响。
func (b *Bundle) GRPCChain() []grpc.ServerOption {
	opts := []grpc.ServerOption{grpc.ChainUnaryInterceptor(b.GRPCInterceptors()...)}
	if stream := b.GRPCStreamInterceptors(); len(stream) > 0 {
		opts = append(opts, grpc.ChainStreamInterceptor(stream...))
	}
	return opts
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
