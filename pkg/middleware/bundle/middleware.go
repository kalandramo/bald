package bundle

import (
	"fmt"
	"strings"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	ginmw "github.com/kalandramo/bald/pkg/middleware/gin"
)

// ---------------------------------------------------------------------------
// 契约桥：server.http.middleware.* → bundle Option
//
// 背景：契约 `server.http.middleware` 下七个子段
// （recovery/cors/logging/request_id/tracing/rate_limit/timeout）若无消费者，
// 后果分三类：
//
//  1. 能力在跑但**配置面未接线**（recovery/request_id/logging）：bundle.Gin()
//     无条件挂这三个中间件，但用的是硬编码/零参版本，契约字段
//     stack_trace / header_name / skip_paths 写了也不生效——最坏的一类，
//     因为「配置看起来生效了」而实际没有；
//  2. 能力与契约段都在、但**未挂链**（cors）：ginmw.CORS 存在，无人调用；
//  3. **能力完全不存在**（tracing/timeout）：连中间件都没有。
//
// 根因是缺一座桥——没有任何框架组件把契约段翻译成链。本文件提供它：
// FromMiddleware 一次翻译全部七段（逐段实现见各段注释）；
// FromGRPCMiddleware 翻译 gRPC 侧的 rate_limit/timeout 两段，产出只作用于
// gRPC 面的 Option（见该函数注释：另四段是空 message，无行为可翻译）。
// ---------------------------------------------------------------------------

// FromMiddleware 把契约 `server.http.middleware` 段翻译为一组 bundle Option。
//
// 返回：
//   - opts：可直接传给 bundle.New 的 Option（与业务自有的 Authn/Authz/Audit 等
//     选项组合使用）；
//   - close：释放桥构造的资源（如限流器、timeout 的 goroutine 池）。**总是非
//     nil**（无资源可释放时为 no-op），调用方在停机路径直接 defer/调用即可，
//     无需判空；
//   - err：配置非法。仅当**显式声明了某段却给了非法值**时报错（启动期
//     fail-fast）——「显式配置却不生效」是最坏结果。
//
// 缺省语义（向后兼容，与既有 bundle 行为一致）：
//   - recovery / request_id / logging：**段缺失时保持既有默认**（三者在
//     bundle.Gin() 中本就无条件挂载，缺段不会移除它们）；段存在时按其字段配置。
//   - cors / secure / rate_limit / tracing / timeout：段缺失即不挂（与
//     bundle.CORS/Secure 的既有「缺省不装配」约定一致）。
//
// mw 为 nil 时返回零 Option（等同未配置任何中间件段）。
func FromMiddleware(mw *bootstrapv1.Server_Http_Middleware) (opts []Option, close func() error, err error) {
	closers := []func() error{}
	close = func() error {
		var firstErr error
		for _, c := range closers {
			if e := c(); e != nil && firstErr == nil {
				firstErr = e
			}
		}
		return firstErr
	}
	if mw == nil {
		return nil, close, nil
	}

	// --- recovery ---
	if r := mw.GetRecovery(); r != nil {
		opts = append(opts, Recovery(ginmw.RecoveryConfig{StackTrace: r.GetStackTrace()}))
	}

	// --- request_id ---
	if ri := mw.GetRequestId(); ri != nil {
		opts = append(opts, RequestID(ginmw.RequestIDConfig{HeaderName: ri.GetHeaderName()}))
	}

	// --- logging ---
	if lg := mw.GetLogging(); lg != nil {
		opts = append(opts, Logging(ginmw.LoggingConfig{SkipPaths: lg.GetSkipPaths()}))
	}

	// --- cors ---
	if c := mw.GetCors(); c != nil {
		cc := corsConfigFrom(c)
		if verr := cc.Validate(); verr != nil {
			return nil, close, fmt.Errorf("bundle: middleware.cors: %w", verr)
		}
		opts = append(opts, CORS(cc))
	}

	// --- rate_limit ---
	if rl := mw.GetRateLimit(); rl != nil {
		lim, lerr := RateLimitFromMiddleware(mw)
		if lerr != nil {
			return nil, close, fmt.Errorf("bundle: middleware.rate_limit: %w", lerr)
		}
		if lim != nil {
			opts = append(opts, RateLimit(lim))
			closers = append(closers, lim.Close)
		}
	}

	// --- timeout ---
	if t := mw.GetTimeout(); t != nil {
		tc, terr := timeoutConfigFrom(t)
		if terr != nil {
			return nil, close, terr
		}
		opts = append(opts, Timeout(tc))
	}

	// --- tracing ---
	// tracing 段无字段（空 message）：存在即启用 span-only 追踪层。
	if mw.GetTracing() != nil {
		opts = append(opts, Tracing())
	}

	return opts, close, nil
}

// FromGRPCMiddleware 把契约 `server.grpc.middleware` 段翻译为一组只作用于 gRPC
// 面的 bundle Option。与 [FromMiddleware] 的分工：那一个读 HTTP 段、产出「一面
// 给全部」的 Option；这一个读 gRPC 段、产出 gRPC 自己的 Option，两个面因此可以
// 各自设容量。
//
// 返回契约与 FromMiddleware 一致：opts 直接并入 bundle.New；close 总是非 nil
// （无资源可释放时为 no-op），业务在停机路径直接调用；err 仅在**显式声明了段却
// 给了非法值**时报错（启动期 fail-fast）。
//
// 只翻译两段：
//   - rate_limit → [GRPCRateLimit]（gRPC unary + stream 两面共用这一个桶）；
//   - timeout → [GRPCTimeout]（**只 unary**：流的生命周期就是整条流，请求级
//     预算套在流上会静默变成「流的最大存活时长」，见 grpcmw.TimeoutStreamInterceptor）。
//
// recovery/logging/tracing/validate 四段本桥**不翻译**，也不是「翻译了不生效」：
// 它们在 proto 里是空 message（没有可映射字段），而 bundle 的 gRPC 链本就无条件
// 挂 Recovery/RequestID，Observability 由 [NoLogging] 控制。写这四段目前不改变
// 任何行为——这是登记在册的既有边界，别把它们当成配置面。
func FromGRPCMiddleware(mw *bootstrapv1.Server_Grpc_Middleware) (opts []Option, close func() error, err error) {
	closers := []func() error{}
	close = func() error {
		var firstErr error
		for _, c := range closers {
			if e := c(); e != nil && firstErr == nil {
				firstErr = e
			}
		}
		return firstErr
	}
	if mw == nil {
		return nil, close, nil
	}

	if rl := mw.GetRateLimit(); rl != nil {
		lim, lerr := GRPCRateLimitFromMiddleware(mw)
		if lerr != nil {
			return nil, close, fmt.Errorf("bundle: grpc.middleware.rate_limit: %w", lerr)
		}
		if lim != nil {
			opts = append(opts, GRPCRateLimit(lim))
			closers = append(closers, lim.Close)
		}
	}

	if t := mw.GetTimeout(); t != nil {
		ms := t.GetDefaultTimeoutMs()
		if ms <= 0 {
			return nil, close, fmt.Errorf(
				"bundle: grpc.middleware.timeout.default_timeout_ms must be > 0, got %d", ms)
		}
		opts = append(opts, GRPCTimeout(time.Duration(ms)*time.Millisecond))
	}

	return opts, close, nil
}

// corsConfigFrom 把契约 Cors 段映射为 ginmw.CORSConfig。
//
// 空 repeated 字段回退到 DefaultCORS 的默认值（契约注释即此意：为空表示
// 允许所有源 / 使用默认方法 / 默认头）。原 `AllowOrigin` 是单字符串（中间件
// 写单个响应头），故多源用 ", " 连接。
func corsConfigFrom(c *bootstrapv1.Server_Http_Middleware_Cors) *ginmw.CORSConfig {
	def := ginmw.DefaultCORS()
	cfg := &ginmw.CORSConfig{
		AllowOrigin:      joinOr(c.GetAllowedOrigins(), def.AllowOrigin),
		AllowMethods:     joinOr(c.GetAllowedMethods(), def.AllowMethods),
		AllowHeaders:     joinOr(c.GetAllowedHeaders(), def.AllowHeaders),
		ExposedHeaders:   strings.Join(c.GetExposedHeaders(), ", "),
		AllowCredentials: c.GetAllowCredentials(),
		MaxAge:           def.MaxAge,
	}
	if ma := c.GetMaxAge(); ma > 0 {
		cfg.MaxAge = int(ma)
	}
	return cfg
}

// joinOr 返回 join(ss, ", ")；ss 为空时返回 fallback。
func joinOr(ss []string, fallback string) string {
	if len(ss) == 0 {
		return fallback
	}
	return strings.Join(ss, ", ")
}

// timeoutConfigFrom 把契约 Timeout 段映射为 ginmw.TimeoutConfig。
//
// default_timeout_ms 必须 > 0（缺省 0 视为未配置超时）；显式声明 timeout 段
// 却给 0 或负值属配置错误 → fail-fast。
func timeoutConfigFrom(t *bootstrapv1.Server_Http_Middleware_Timeout) (*ginmw.TimeoutConfig, error) {
	ms := t.GetDefaultTimeoutMs()
	if ms <= 0 {
		return nil, fmt.Errorf("bundle: middleware.timeout.default_timeout_ms must be > 0, got %d", ms)
	}
	return &ginmw.TimeoutConfig{
		DefaultTimeoutMs: ms,
		StatusCode:       int(t.GetStatusCode()),
	}, nil
}
