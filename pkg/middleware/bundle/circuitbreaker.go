package bundle

import (
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

// CircuitBreakerFromMiddleware 从契约 `server.http.middleware` 段构造应用级熔断器。
//
// 与 [RateLimitFromMiddleware] 同桥同形：`server.http.middleware.circuit_breaker`
// 的五个字段（error_threshold/min_requests/open_duration_ms/window_ms/
// status_code）在这里翻译成 [ginmw.CircuitBreakerConfig]，交给
// [ginmw.NewCircuitBreaker]；否则契约声明了熔断段、解析得到值却无人使用。
//
// 语义：
//   - mw 为 nil 或不含 circuit_breaker 段 → (nil, nil)，调用方按不挂这一层处理
//     （熔断是 opt-in：默认开会让「一次 5xx」在没人要求的场合变成整站拒绝）；
//   - 字段为 0 → 取底层默认（proto3 无字段存在性，0 与「没写」不可区分，见
//     [ginmw.CircuitBreakerConfig]）；
//   - 非法值（阈值越界、负数、状态码非 5xx）→ 错误，启动期 fail-fast。
//
// 生命周期：返回的 *ginmw.CircuitBreaker 由调用方持有，停机时须 Close。Close
// 之后 hystrix 永久拒绝，所以只在服务停止接收流量之后调它。
//
// 典型装配（与限流同款：构造期读契约、经 bundle 挂链）：
//
//	mw := cfg.GetServer().GetHttp().GetMiddleware()
//	cb, err := bundle.CircuitBreakerFromMiddleware(mw)
//	if err != nil { return err }
//	if cb != nil { defer cb.Close() }
//	b := bundle.New(bundle.CircuitBreaker(cb))
//	router.Use(b.Gin()...)
func CircuitBreakerFromMiddleware(mw *bootstrapv1.Server_Http_Middleware) (*ginmw.CircuitBreaker, error) {
	c := mw.GetCircuitBreaker()
	if c == nil {
		return nil, nil
	}
	return newCircuitBreaker(c.GetErrorThreshold(), c.GetMinRequests(),
		c.GetOpenDurationMs(), c.GetWindowMs(), c.GetStatusCode())
}

// GRPCCircuitBreakerFromMiddleware 从契约 `server.grpc.middleware.circuit_breaker`
// 段构造**只喂 gRPC 面**的熔断器（经 [GRPCCircuitBreaker] 注入）。缺段 → (nil, nil)。
//
// 字段与校验口径与 HTTP 侧逐条相同（同一个 newCircuitBreaker），差在少一个
// status_code：那是 HTTP 专属概念，gRPC 的拒绝码固定是 UNAVAILABLE（同
// [GRPCRateLimitFromMiddleware] 里 rate_limit 的字段对称性，也和 timeout 段
// 不带 status_code 是同一个理由）。
func GRPCCircuitBreakerFromMiddleware(mw *bootstrapv1.Server_Grpc_Middleware) (*ginmw.CircuitBreaker, error) {
	c := mw.GetCircuitBreaker()
	if c == nil {
		return nil, nil
	}
	return newCircuitBreaker(c.GetErrorThreshold(), c.GetMinRequests(),
		c.GetOpenDurationMs(), c.GetWindowMs(), 0)
}

// newCircuitBreaker 把一段 circuit_breaker 配置建成熔断器；status_code 由调用方
// 给出（gRPC 面传 0 = 不用这个字段）。两面共用，保证字段口径不会各自漂移。
func newCircuitBreaker(errorThreshold float64, minRequests int32, openDurationMs, windowMs int64, statusCode int32) (*ginmw.CircuitBreaker, error) {
	return ginmw.NewCircuitBreaker(ginmw.CircuitBreakerConfig{
		ErrorThreshold: errorThreshold,
		MinRequests:    int(minRequests),
		OpenDurationMs: openDurationMs,
		WindowMs:       windowMs,
		StatusCode:     int(statusCode),
	})
}
