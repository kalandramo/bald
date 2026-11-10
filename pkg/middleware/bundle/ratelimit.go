package bundle

import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/ratelimit"
	"github.com/kalandramo/bald/ratelimit/tokenbucket"
)

// RateLimitFromMiddleware 从契约 `server.http.middleware` 段构造应用级限流层。
//
// 背景：`server.http.middleware.rate_limit`（字段 rate/burst/wait）需要真正的
// 消费者——否则契约里声明了限流段，解析得到值却无人使用，业务只能自持配置段
// 绕行（bald-admin 的 `login.rate_limit.*` 即此绕行的产物）。本函数是
// 「契约段 → 中间件」的正式桥。
//
// 语义：
//   - mw 为 nil 或不含 rate_limit 段 → 返回 (nil, false, nil)，调用方按禁用处理
//     （与既有中间件段「缺省不装配」的保守约定一致）；
//   - 含 rate_limit 但 rate/burst 非正 → 返回错误，启动期 fail-fast
//     （显式配置了限流却静默不生效是最坏结果）。
//
// 返回 (limiter, wait, err)：统一件 middleware.RateLimit(l, wait) 的两个参数——
// 方案 S 批次 4 后限流是统一件（一份实现双协议共用），不再是 ginmw 包装器。
//
// 生命周期：返回的 limiter 由调用方持有，停机时须 Close。
//
// 典型装配（与 CORS 同款：构造期读契约、经 bundle 挂链）：
//
//	mw := cfg.GetServer().GetHttp().GetMiddleware()
//	l, wait, err := bundle.RateLimitFromMiddleware(mw)
//	if err != nil { return err }                       // 启动期 fail-fast
//	if l != nil { defer l.Close() }                    // 停机释放
//	b := bundle.New(bundle.RateLimit(l, wait))         // l 为 nil 时不挂该层
//	router.Use(b.Gin()...)
func RateLimitFromMiddleware(mw *bootstrapv1.Server_Http_Middleware) (ratelimit.Limiter, bool, error) {
	rl := mw.GetRateLimit()
	if rl == nil {
		return nil, false, nil
	}
	return newRateLimiter(rl.GetRate(), rl.GetBurst(), rl.GetWait())
}

// GRPCRateLimitFromMiddleware 从契约 `server.grpc.middleware.rate_limit` 段构造
// **只喂 gRPC 面**的限流层（经 [GRPCRateLimit] 注入）。缺段 → (nil, false, nil)。
//
// 字段、校验与缺省语义与 HTTP 侧逐条相同（同一个 newRateLimiter）——两面差在
// 「桶归谁」，不差在配置口径。注意它与 [RateLimitFromMiddleware] 各造一个
// limiter：两个段都声明时是两个互不相干的桶。
func GRPCRateLimitFromMiddleware(mw *bootstrapv1.Server_Grpc_Middleware) (ratelimit.Limiter, bool, error) {
	rl := mw.GetRateLimit()
	if rl == nil {
		return nil, false, nil
	}
	return newRateLimiter(rl.GetRate(), rl.GetBurst(), rl.GetWait())
}

// newRateLimiter 把一段 rate_limit 配置建成统一件所需的对：tokenbucket limiter
// 与 wait 姿态；非法值返回错误（启动期 fail-fast）。两面共用，保证 HTTP 与 gRPC
// 的字段口径不会各自漂移。wait 为 false 时 Limiter.Wait 不会被统一件调用。
func newRateLimiter(rate float64, burst int32, wait bool) (ratelimit.Limiter, bool, error) {
	l, err := tokenbucket.New(rate, float64(burst))
	if err != nil {
		return nil, false, err
	}
	return l, wait, nil
}
