// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// bridge_gin.go 把统一 Middleware 桥接成 gin handler（自 onexmesh 移植）。
// 它向请求 ctx 注入 Transporter（Kind=HTTP，Operation="METHOD /路由模板"），
// 并让中间件经 c.Next() 包住 gin 链的余下部分。
package middleware

import (
	"context"
	"net/http"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	berrorshttperr "github.com/kalandramo/bald/berrors/httperr"
	"github.com/kalandramo/bald/pkg/middleware/response"
)

// GinHandler 把统一 Middleware 桥接成 gin handler。它注入 Transporter 并让
// 中间件经 c.Next() 包住余下 gin 链；中间件短路时按错误的传输类别渲染
// google.rpc.Status JSON（与 transport/web.ErrorResponse 同形）。
func GinHandler(m Middleware) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 复用外层 GinHandler 已注入的 Transporter，只在最外层创建一次。统一
		// 链按「每中间件一个 GinHandler」注册，显然的实现会给每个中间件造一个
		// Transporter——然后每个看到的都是**不同**实例。这对构造期字段不可见，
		// 对终值字段是致命的：响应状态由最内层包装器写一次、由 tracing/metrics
		// 在出栈时读——一人一个实例时 tracing 读到的是没人写过的状态。
		tr, ok := FromServerContext(c.Request.Context())
		if !ok {
			tr = newHTTPTransporter(c)
			defer ReleaseTransporter(tr)
			c.Request = c.Request.WithContext(NewServerContext(c.Request.Context(), tr))
		}
		ctx := c.Request.Context()

		next := func(ctx context.Context, _ interface{}) (interface{}, error) {
			// 把统一链的 context（携带 Timeout 等写入的 deadline）传播回 gin
			// handler——后者读 c.Request.Context()。
			c.Request = c.Request.WithContext(ctx)
			c.Next()
			// 在终端位置、链回卷之前记录，链上每层观察同一个终值。
			tr.SetStatusCode(c.Writer.Status())
			// 把写出的 HTTP 状态面化成统一错误，让 logging/metrics/tracing
			// 能观察 4xx/5xx 失败，而不只是 gRPC 的 error。
			return nil, httpStatusError(c.Writer.Status())
		}
		if _, err := m(next)(ctx, c); err != nil {
			// 在 next 之前短路的中间件没有写过响应；按错误自带的传输类别渲染
			// 状态码与 JSON 包络（如 Auth 401、RateLimit 429）。next 已写过
			// 状态时，状态派生错误不得触发第二次写。
			if !c.Writer.Written() {
				response.RenderError(c, err)
			}
			// gin 的 c.Next() 是循环：本桥返回后（无论是否调过 c.Next()），gin
			// 会推进索引继续跑链上剩余的 handler。统一链短路 = 后续 gin 链不得
			// 执行，必须显式 Abort——否则拒绝响应（401/429）之后面 handler 照跑。
			c.Abort()
		}
	}
}

// newHTTPTransporter 从 gin context 组装 HTTP Transporter。
//
// # 路由模板，不是 path
//
// FullPath 是路由器匹配到的模板——"/v1/users/:userID"；URL.Path 是调用方
// 敲的——"/v1/users/usr-123"。模板才是可聚合的那个，所以它进 operation 与
// http.route；原始 path 单独携带给 span 的 url.path——基数成本是一条 trace
// 行而不是一条时间序列。
//
// # 为什么 FullPath 这么早就有
//
// gin 在 handleHTTPRequest 里、调用命中路由的 handler 链之前就填好了
// Context.fullPath——本桥是链头，所以运行到这里时模板已解析完毕，不需要
// 延迟或按路由重注册。
func newHTTPTransporter(c *gin.Context) Transporter {
	method, route := c.Request.Method, c.FullPath()
	// 无路由命中：gin 走 404 链时 fullPath 为空。按 path 命名 operation 恰是
	// 要避免的错误——那会把每个被试探的 URL 变成一条新序列。
	operation := method + " " + route
	if route == "" {
		operation = method + " unmatched"
	}

	return NewTransporter(
		KindHTTP,
		operation,
		c.Request.Host,
		NewHTTPHeader(c.Request.Header),
		NewHTTPHeader(c.Writer.Header()),
		WithHTTPRequest(method, route, c.Request.URL.Path, protocolVersion(c.Request.Proto)),
	)
}

// protocolVersion 把 net/http 的协议串（"HTTP/1.1"）转成
// network.protocol.version 要的形态（"1.1"）。
func protocolVersion(proto string) string {
	if version, ok := strings.CutPrefix(proto, "HTTP/"); ok {
		return version
	}
	return proto
}

// httpStatusErrors 按**命中**惰性缓存（ADR-4）：实测热路径只落在少数几个状态码
// （404/401/403/429/500/503），预构造 200 项是过度设计——按需构造一次、并发下经
// sync.Map 复用同一实例（享元语义不变：同状态码返回同一 error）。berrors 的
// Code 是 google.rpc 码域——HTTP 状态经 berrorshttperr.HTTPToCode 反查（CodeToHTTP
// 的逆映射），Reason 取稳定语义名。
var httpStatusErrors sync.Map // map[int]error

// httpStatusError 把 gin 已写出的响应状态面化成协议无关错误，统一链据此观测
// HTTP 失败。1xx/2xx/3xx 返回 nil。
func httpStatusError(status int) error {
	if status < http.StatusBadRequest {
		return nil
	}
	if e, ok := httpStatusErrors.Load(status); ok {
		return e.(error)
	}
	e := berrors.New(berrorshttperr.HTTPToCode(status), httpStatusReason(status)).
		WithMessage("HTTP request failed with status %d", status)
	httpStatusErrors.LoadOrStore(status, e)
	return e
}

// httpStatusReasons 把少数有稳定语义 Reason 的 HTTP 状态固化映射；其余落到
// 类别默认值。
var httpStatusReasons = map[int]string{
	http.StatusBadRequest:      "InvalidArgument",
	http.StatusUnauthorized:    "Unauthenticated",
	http.StatusForbidden:       "PermissionDenied",
	http.StatusNotFound:        "NotFound",
	http.StatusTooManyRequests: "ResourceExhausted.TooManyRequests",
	http.StatusGatewayTimeout:  "ServiceUnavailable.Timeout",
}

// httpStatusReason 把非 2xx 的 HTTP 状态映射到稳定语义 Reason。
func httpStatusReason(status int) string {
	if r, ok := httpStatusReasons[status]; ok {
		return r
	}
	if status >= http.StatusInternalServerError {
		return "InternalError"
	}
	return "InvalidArgument"
}

// FromGin 把裸 gin.HandlerFunc 提升为统一 Handler，用作中间件链的终端节点。
func FromGin(h gin.HandlerFunc) Handler {
	return func(_ context.Context, req interface{}) (interface{}, error) {
		h(req.(*gin.Context))
		return nil, nil
	}
}
