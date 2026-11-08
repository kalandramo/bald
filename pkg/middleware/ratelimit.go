// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// ratelimit.go 是统一形态的限流中间件（自两链合并；方案 S 批次 2）。

package middleware

import (
	"context"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/ratelimit"
)

// ReasonRateLimited 是本层所有拒绝的稳定业务标识（berrors.Is 按 Reason 匹配，
// 忽略 Code/Message）。快速失败与排队失败共用它：调用方要区分两者读 Message。
const ReasonRateLimited = "RATE_LIMITED"

// RateLimit 用一个 ratelimit.Limiter 对调用限流：一次实现双协议——拒绝错误
// 经统一链收口，gRPC 面 ResourceExhausted、gin 面 429（CodeToHTTP(8)）。
//
// wait=false（快速失败，默认口径）：令牌不足立即拒绝，不进 handler，保护下游。
// wait=true（平滑）：阻塞到令牌可用或 ctx 取消；取消同样以限流错误结束。
//
// 链序：挂在认证/授权**之后**——被拒的多是有配额的主体，匿名流量无法先耗尽
// 配额。代价是不保护认证层自身；需要在认证前限流的业务自行挪到链首。
//
// 未覆盖：ratelimit.InflightLimiter 的 Done(rtt) 配对上报（BBR 自适应需要），
// 两侧旧链同样没做，口径一致；要用 BBR 的 RTT 反馈在链外自行配对。
func RateLimit(l ratelimit.Limiter, wait bool) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			if l == nil {
				return next(ctx, req)
			}
			if wait {
				if err := l.Wait(ctx); err != nil {
					return nil, berrors.ResourceExhausted(ReasonRateLimited).
						WithMessage("rate limit wait failed: %v", err).WithCause(err)
				}
				return next(ctx, req)
			}
			ok, err := l.Allow()
			if err != nil {
				return nil, berrors.ResourceExhausted(ReasonRateLimited).
					WithMessage("rate limiter unavailable: %v", err).WithCause(err)
			}
			if !ok {
				return nil, berrors.ResourceExhausted(ReasonRateLimited).
					WithMessage("rate limit exceeded")
			}
			return next(ctx, req)
		}
	}
}
