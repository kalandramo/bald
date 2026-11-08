// Package resiliency 把 retry 与 circuitbreaker 接成出站调用的韧性层：
// gRPC 客户端拦截器与 HTTP RoundTripper 两种形态，按命名出站条目显式组合。
// 能力自 onexmesh 的 Dapr 风格 resiliency 吸收；语义改写为 bald 习语——
// 不做声明式 YAML 子系统，而是「调用策略由业务域在调用侧显式组合」
// （client.proto v1 刻意把重试/熔断/限流排除在契约段外，判据 J8）。
//
// 组合顺序固定为「熔断在外、重试在内」：熔断器对一次逻辑调用放行一次
// （Allow），重试在内部消化瞬时失败，最终结果恰好上报一次（Mark*）。
// 这样一次「重试后成功」的逻辑请求记为成功（下游只是瞬时抖动），只有
// 最终失败才喂失败率；且 ErrCircuitOpen 在重试之前返回，永远不会被重试
// （无需调用方把 ErrCircuitOpen 排除出重试分类器）。详见设计文档
// 「为什么熔断在外、重试在内」。
//
// 本包只产出原生 grpc.DialOption / http.RoundTripper，不 import
// transport/client——调用方经 client.WithDialOptions / client.WithTransport
// 组合，或经 contract.WithEntryGRPCDialOptions / WithEntryHTTPOptions 绑定到
// 命名条目。依赖面只有 retry + circuitbreaker + grpc。
//
// 见 docs/devel/zh-CN/Bald 出站韧性设计.md。
package resiliency

import (
	"context"
	"net/http"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/circuitbreaker"
	"github.com/kalandramo/bald/retry"
)

// Policy 是一次出站调用的韧性策略：熔断闸门 + 重试循环 + 失败口径。
// 零值 Policy 是直通（不熔断、不重试），可安全用作「只想要默认失败口径」
// 的起点。三个分类器各自缺省取传输面的默认形状（见 matching.go）。
type Policy struct {
	// Breaker 对每次逻辑调用放行一次（Allow 前置、Mark* 后置恰好一次）。
	// nil = 不熔断。熔断器实例由调用方持有（每个命名条目一个，自适应状态
	// 不跨条目共享——与 selector/circuitbreaker.Registry 同一纪律）。
	Breaker circuitbreaker.CircuitBreaker

	// Retrier 在逻辑调用内部重试瞬时失败。nil = 单次尝试。取 retry.Doer
	// 端口而非具体类型：调用方可注入 retry.New(...) 或测试替身。
	Retrier retry.Doer

	// GRPCFailure 判定下游 gRPC 错误是否计入熔断失败率。nil = DefaultGRPCFailure
	// （服务侧错误码；客户端侧错误码与 ResourceExhausted 不计）。
	GRPCFailure func(error) bool

	// HTTPFailure 判定 HTTP 结果是否计入熔断失败率。nil = DefaultHTTPFailure
	// （5xx 或传输错误；4xx 不计——坏请求不该替全体开窗）。
	HTTPFailure func(*http.Response, error) bool

	// HTTPRetry 判定 HTTP 结果是否值得重试。nil = DefaultHTTPRetry
	// （429/500/502/503/504 或传输错误）。被重试的响应体会被缓冲以便耗尽时
	// 原样交还调用方（见 Transport）。
	HTTPRetry func(*http.Response, error) bool
}

// guard 是熔断 + 重试的共用组合骨架：熔断在外放行一次，重试在内消化瞬时
// 失败，最终结果（含 panic 路径）恰好上报一次。failed 在 defer 里拿到最终
// error 求值——gRPC 直接用失败分类器，HTTP 忽略入参、读自己捕获的响应。
//
// Allow 返回 ErrCircuitOpen 时直接返回、不进重试也不上报（契约：闸门没开
// 不欠 Mark*）。panic 路径记失败后原样抛出——漏掉这一次上报不是少统计一条，
// 是 hystrix 半开探针悬着、熔断从此永久拒绝（与入站中间件同一不变量）。
func (p Policy) guard(ctx context.Context, failed func(error) bool, call func(context.Context) error) (err error) {
	if p.Breaker != nil {
		if allowErr := p.Breaker.Allow(); allowErr != nil {
			return allowErr
		}
		defer func() {
			if r := recover(); r != nil {
				p.Breaker.MarkFailure()
				panic(r)
			}
			if failed(err) {
				p.Breaker.MarkFailure()
			} else {
				p.Breaker.MarkSuccess()
			}
		}()
	}
	if p.Retrier != nil {
		return p.Retrier.Do(ctx, call)
	}
	return call(ctx)
}

// UnaryClientInterceptor 把 Policy 接成 gRPC 一元客户端拦截器。经
// grpc.WithChainUnaryInterceptor 挂进 DialOption；与契约 client 段的默认
// 超时拦截器叠加时，超时在最外层（每个 attempt 拿完整超时窗）。
//
// 流面刻意不接：一条流的「失败」没有一致答案（与入站熔断拦截器同一取舍）。
func UnaryClientInterceptor(p Policy) grpc.UnaryClientInterceptor {
	failure := p.GRPCFailure
	if failure == nil {
		failure = DefaultGRPCFailure
	}
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		return p.guard(ctx, failure, func(ctx context.Context) error {
			return invoker(ctx, method, req, reply, cc, opts...)
		})
	}
}

// GRPCDialOption 把 Policy 包成原生拨号选项，供 client.WithDialOptions 或
// contract.WithEntryGRPCDialOptions 直接消费。
func GRPCDialOption(p Policy) grpc.DialOption {
	return grpc.WithChainUnaryInterceptor(UnaryClientInterceptor(p))
}