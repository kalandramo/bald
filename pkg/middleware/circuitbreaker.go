// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// circuitbreaker.go 是统一形态的入站熔断（方案 S 收官——批次 2 显式登记的
// 「circuitbreaker 随 WithCircuitBreaker 评估」项）：一份实现经三座桥同时服务
// gin 与 gRPC，selector/circuitbreaker 的出站熔断不替代它（那是按节点健康谕言，
// 本件按调用结果汇）。

package middleware

import (
	"context"
	"errors"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/circuitbreaker"
)

// CircuitBreakerOption 配置统一熔断件。
type CircuitBreakerOption func(*circuitBreakerOptions)

type circuitBreakerOptions struct {
	// openCode 是开态（含半开被拒）拒绝的语义码。缺省 Unavailable——「服务
	// 暂时不可用」是客户端库会重试的那个语义，也是面段熔断的既有拒绝码。
	openCode uint32
}

// WithOpenCode 指定开态拒绝的语义码（google.rpc 码域）。HTTP 面经桥的
// CodeToHTTP 渲染成对应状态码（Unavailable → 503）。5xx 之外的语义码会让
// 客户端把「我坏了」当业务错误处理——调用方自行把关。
func WithOpenCode(code uint32) CircuitBreakerOption {
	return func(o *circuitBreakerOptions) { o.openCode = code }
}

// IsFailureCode 报告一个 berrors 语义码是否算「服务侧失败」（统一熔断的失败
// 口径，与面段熔断的失败判定共用同一组）。只列服务侧的错：客户端侧的
// （InvalidArgument / NotFound / PermissionDenied / FailedPrecondition / Aborted /
// OutOfRange / Unauthenticated）是调用方的错，计入失败率会让一个坏客户端替
// 全体用户开窗。ResourceExhausted 也**不在**集合里，尽管字面像「服务满了」：
// 那是本服务限流层自己给出的答案，计入失败等于让熔断消费自己的拒流——配额
// 打满时熔断跟着开窗，两层从互补变成互相放大。
func IsFailureCode(code uint32) bool {
	switch code {
	case berrors.CodeInternal, berrors.CodeUnknown, berrors.CodeUnavailable,
		berrors.CodeDeadlineExceeded, berrors.CodeDataLoss:
		return true
	}
	return false
}

// isFailureError 是熔断失败口径的完整分类（分类顺序对齐 grpcerr.ToStatus——
// 本层在桥内看到的错误可能是任何形态）：
//  1. *berrors.Error → 用它的 Code（统一链上的错误主形态：桥面 5xx 哨兵、
//     recovery 的 Internal、内层件的拒绝都在此列）；
//  2. context 的两个哨兵 → Canceled 是客户端撤单（不算失败），
//     DeadlineExceeded 是服务没在预算内回来（算失败）；
//  3. 其余原生错误 → 与 grpc-go 一样兜成 Unknown，算失败。
func isFailureError(err error) bool {
	if err == nil {
		return false
	}
	if e, ok := berrors.FromError(err); ok {
		return IsFailureCode(e.Code)
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	return true
}

// CircuitBreaker 用注入的熔断器保护统一链上的后续处理。
//
// 与限流（[RateLimit]）同处一条链、各判一件事：限流问「这个主体还有没有配额」，
// 熔断问「这个服务现在值不值得真做」。拒绝的码因此不同：限流回
// ResourceExhausted，熔断回 openCode（缺省 Unavailable）。错误以 *berrors.Error
// 构造，经桥的 grpcerr 收口后 Code/Reason 可跨服务还原；gin 面经桥渲染成
// CodeToHTTP 对应状态码。
//
// 失败口径见 [IsFailureCode]：next 返回的错误按 berrors Code 分类（统一链上
// 的错误都是 *berrors.Error——桥面 5xx 以 httpStatusError 哨兵到达、recovery 以
// Internal 到达）。上报义务与面段熔断同一条契约：「Allow 放行即欠一次 Mark*」，
// panic 路径必须在 defer 里记失败后原样抛出——hystrix 半开态的在途探针标记
// 只有 Mark* 会复位，漏掉一次上报是熔断从此永久拒绝，不是少统计一条。被本层
// 自己拒掉的请求不欠上报（Allow 已返回错误，再调 Mark* 会让拒绝喂养自己的失败率）。
//
// cb 为 nil 时退化为透传（等同未装配，零开销）。
func CircuitBreaker(cb circuitbreaker.CircuitBreaker, opts ...CircuitBreakerOption) Middleware {
	cfg := &circuitBreakerOptions{}
	for _, opt := range opts {
		opt(cfg)
	}
	openCode := cfg.openCode
	if openCode == 0 {
		openCode = berrors.CodeUnavailable
	}
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			if cb == nil {
				return next(ctx, req)
			}
			if err := cb.Allow(); err != nil {
				return nil, berrors.New(openCode, "CIRCUIT_OPEN").
					WithMessage("circuit breaker is open")
			}
			resp, err := func() (resp interface{}, err error) {
				defer func() {
					if p := recover(); p != nil {
						cb.MarkFailure()
						panic(p)
					}
				}()
				return next(ctx, req)
			}()
			if err == nil {
				cb.MarkSuccess()
				return resp, nil
			}
			if isFailureError(err) {
				cb.MarkFailure()
			} else {
				// 客户端侧错误与限流拒绝记成功：它们不证明服务坏了。
				cb.MarkSuccess()
			}
			return resp, err
		}
	}
}
