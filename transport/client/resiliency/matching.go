package resiliency

import (
	"errors"
	"net/http"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// grpcFailureCodes 是出站 gRPC 面计入熔断失败率的 status code 集合，与入站
// 中间件（pkg/middleware/grpc.failureCodes）同一口径：只列下游服务侧的错。
//
// 客户端侧的错（InvalidArgument/NotFound/PermissionDenied/...）是调用方的
// 问题，计入会让一个坏请求替全体开窗。ResourceExhausted 刻意不在集合里：
// 那是下游限流层给出的「你太多」，不是「下游坏了」——把它计入失败率会让
// 熔断在下游节流时跟着开窗，两层从互补变成互相放大（与入站排除自己的拒流
// 同一理由）。它仍可重试（见 retry 分类器），但默认重试集不含它，由调用方
// 按需开。
var grpcFailureCodes = map[codes.Code]bool{
	codes.Internal:         true,
	codes.Unknown:          true,
	codes.Unavailable:      true,
	codes.DeadlineExceeded: true,
	codes.DataLoss:         true,
}

// DefaultGRPCFailure 判定下游 gRPC 错误是否计入熔断失败率。
//
// 遍历错误树而非只看顶层：重试耗尽时 retry.Do 返回 errors.Join(ErrMaxAttempts,
// lastErr)，顶层不是 status，真正的下游错误码藏在叶子里。只对「确实是 gRPC
// status」的叶子判码（status.FromError 的 ok=true）——非 status 错误退化成
// Unknown 是 ok=false，不能把任意错误都算成 Unknown 失败。
func DefaultGRPCFailure(err error) bool {
	found := false
	walkError(err, func(e error) {
		if s, ok := status.FromError(e); ok && grpcFailureCodes[s.Code()] {
			found = true
		}
	})
	return found
}

// walkError 深度优先遍历 err 及其 unwrap 树（含 errors.Join 的多孩子），
// 对每个节点调 fn。err 为 nil 即返回。
func walkError(err error, fn func(error)) {
	if err == nil {
		return
	}
	fn(err)
	switch u := err.(type) {
	case interface{ Unwrap() []error }:
		for _, child := range u.Unwrap() {
			walkError(child, fn)
		}
	case interface{ Unwrap() error }:
		walkError(u.Unwrap(), fn)
	}
}

// DefaultHTTPFailure 判定 HTTP 结果是否计入熔断失败率：传输错误（连接失败/
// 超时）或 5xx 算下游失败；4xx 不算（坏请求不该替全体开窗），429 也不算
// （下游节流 ≠ 下游坏了，与 gRPC 排除 ResourceExhausted 同理）。
func DefaultHTTPFailure(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	return resp != nil && resp.StatusCode >= http.StatusInternalServerError
}

// DefaultHTTPRetry 判定 HTTP 结果是否值得重试：传输错误，或 429/500/502/
// 503/504（节流与瞬时服务端错）。其余 4xx 与 2xx/3xx 不重试。被重试的响应
// 由 Transport 缓冲后原样交还（耗尽时返回最后一个响应而非错误）。
func DefaultHTTPRetry(resp *http.Response, err error) bool {
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}
	switch resp.StatusCode {
	case http.StatusTooManyRequests, http.StatusInternalServerError,
		http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

// errRetryableStatus 是「响应值得重试」的内部哨兵：把可重试的 HTTP 响应翻译
// 成 retry.Do 能消化的 error。它只在 Transport 内部流转，耗尽时由缓冲的响应
// 体还原成真实响应交还调用方，不会泄漏到 RoundTripper 的返回值。
var errRetryableStatus = errors.New("resiliency: retryable http status")