// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// timeout.go 是统一形态的服务端默认超时（自 gRPC 侧现行语义统一而来）。

package middleware

import (
	"context"
	"time"
)

// Timeout 给**没有携带 deadline** 的调用挂上服务端默认超时：一份实现经两座
// 桥同时服务 gRPC 与 HTTP。
//
// 语义，以及它**不**保证什么（两侧旧链的差异必须讲清，别当成同一个东西）：
//   - 已有 deadline 的调用原样放过。框架不缩短调用方给的预算。「取 remaining
//     与 d 较小者」会把客户端刻意设的长预算腰斩，不是这一层该有的权力。
//   - 它约束的是 handler 可见的 ctx：handler 得监听 ctx.Done()/ctx.Err() 才
//     会真的停下。Go 不能杀 goroutine，本层保证的是「handler 拿到一个会到期
//     的上下文」，**不是**「响应按时返回」。
//   - 与 gin 旧链的差异：gin 的 Timeout（契约 server.http.middleware.timeout）
//     是「响应按时返回」语义——handler 丢进 goroutine、缓冲 writer、超时后
//     写 503。统一链取 gRPC 侧的 ctx-deadline 语义（gin 旧链保留至批次 3；
//     契约段统一时两种语义用不同字段表达，绝不静默合并）。
//   - 流式调用：本件不进默认流链——「一个 d」在流上是「流的最大存活时长」，
//     一条订阅/双向会话到点被砍且不报错。要这个语义显式挂（批次 3 的统一段
//     同样只用字段表达，不默认）。
//
// d <= 0 时不挂超时（等同不装配）：把 0 当「立即到期」会让所有请求失败，
// 而 0 更常见的原因是配置段留空。
func Timeout(d time.Duration) Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			if d <= 0 {
				return next(ctx, nil)
			}
			if _, ok := ctx.Deadline(); ok {
				return next(ctx, nil)
			}
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next(ctx, nil)
		}
	}
}
