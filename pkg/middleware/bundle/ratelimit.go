package bundle

import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

// RateLimitFromMiddleware 从契约 `server.http.middleware` 段构造应用级限流器。
//
// 背景：`server.http.middleware.rate_limit`（字段 rate/burst/wait）需要真正的
// 消费者——否则契约里声明了限流段，解析得到值却无人使用，业务只能自持配置段
// 绕行（bald-admin 的 `login.rate_limit.*` 即此绕行的产物）。本函数是
// 「契约段 → 中间件」的正式桥。
//
// 语义：
//   - mw 为 nil 或不含 rate_limit 段 → 返回 (nil, nil)，调用方按禁用处理
//     （与既有中间件段「缺省不装配」的保守约定一致）；
//   - 含 rate_limit 但 rate/burst 非正 → 返回错误，启动期 fail-fast
//     （显式配置了限流却静默不生效是最坏结果）。
//
// 生命周期：返回的 *ginmw.RateLimiter 由调用方持有，停机时须 Close。
//
// 典型装配（与 CORS 同款：构造期读契约、经 bundle 挂链）：
//
//	mw := cfg.GetServer().GetHttp().GetMiddleware()
//	rl, err := bundle.RateLimitFromMiddleware(mw)
//	if err != nil { return err }          // 启动期 fail-fast
//	if rl != nil { defer rl.Close() }     // 停机释放
//	b := bundle.New(bundle.RateLimit(rl)) // rl 为 nil 时不挂该层
//	router.Use(b.Gin()...)
func RateLimitFromMiddleware(mw *bootstrapv1.Server_Http_Middleware) (*ginmw.RateLimiter, error) {
	rl := mw.GetRateLimit()
	if rl == nil {
		return nil, nil
	}
	return newRateLimiter(rl.GetRate(), rl.GetBurst(), rl.GetWait())
}

// GRPCRateLimitFromMiddleware 从契约 `server.grpc.middleware.rate_limit` 段构造
// **只喂 gRPC 面**的限流器（经 [GRPCRateLimit] 注入）。缺段 → (nil, nil)。
//
// 字段、校验与缺省语义与 HTTP 侧逐条相同（同一个 newRateLimiter）——两面差在
// 「桶归谁」，不差在配置口径。注意它与 [RateLimitFromMiddleware] 各造一个
// limiter：两个段都声明时是两个互不相干的桶。
func GRPCRateLimitFromMiddleware(mw *bootstrapv1.Server_Grpc_Middleware) (*ginmw.RateLimiter, error) {
	rl := mw.GetRateLimit()
	if rl == nil {
		return nil, nil
	}
	return newRateLimiter(rl.GetRate(), rl.GetBurst(), rl.GetWait())
}

// newRateLimiter 把一段 rate_limit 配置建成 limiter；非法值返回错误（启动期
// fail-fast）。两面共用，保证 HTTP 与 gRPC 的字段口径不会各自漂移。
func newRateLimiter(rate float64, burst int32, wait bool) (*ginmw.RateLimiter, error) {
	return ginmw.NewRateLimit(ginmw.RateLimitConfig{Rate: rate, Burst: float64(burst), Wait: wait})
}
