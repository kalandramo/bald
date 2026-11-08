package resiliency

import (
	"bytes"
	"context"
	"io"
	"net/http"
)

// Transport 把 Policy 接成 HTTP RoundTripper，包裹 base（nil = http.DefaultTransport）。
// 经 client.WithTransport 或 contract.WithEntryHTTPOptions 挂进出站条目。
//
// 与 gRPC 面同一组合顺序（熔断在外、重试在内），但 HTTP 的「可重试结果」是
// 响应状态码而非 error：可重试响应（默认 429/500/502/503/504）的体会被读尽
// 缓冲、连接复用，翻译成内部哨兵喂给 retry.Do；重试耗尽或单次尝试命中可重试
// 状态时，把缓冲的最后一个响应原样交还调用方（HTTP 调用方期望读 StatusCode，
// 不是拿一个错误）。传输错误耗尽则无响应可还，返回 error。
//
// 重试要求请求体可重放：req.GetBody 非空时每次尝试取一份新体（http.NewRequest
// 对 bytes/strings.Reader 自动设置）；流式体无 GetBody 则无法安全重试，由调用方
// 保证（与 go-retryablehttp 同一约束）。
func Transport(p Policy, base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	failure := p.HTTPFailure
	if failure == nil {
		failure = DefaultHTTPFailure
	}
	retryable := p.HTTPRetry
	if retryable == nil {
		retryable = DefaultHTTPRetry
	}
	return &roundTripper{policy: p, base: base, failure: failure, retryable: retryable}
}

type roundTripper struct {
	policy    Policy
	base      http.RoundTripper
	failure   func(*http.Response, error) bool
	retryable func(*http.Response, error) bool
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	var (
		finalResp *http.Response
		finalErr  error
		lastBuf   []byte
	)
	call := func(ctx context.Context) error {
		r := req.WithContext(ctx)
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				finalResp, finalErr = nil, err
				return err
			}
			r.Body = body
		}
		resp, err := rt.base.RoundTrip(r)
		if err != nil {
			finalResp, finalErr = nil, err
			return err
		}
		if rt.retryable(resp, nil) {
			buf, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil {
				finalResp, finalErr = nil, readErr
				return readErr
			}
			finalResp, finalErr, lastBuf = resp, nil, buf
			return errRetryableStatus
		}
		finalResp, finalErr = resp, nil
		return nil
	}

	// 熔断标记读捕获的最终 HTTP 结果（响应或传输错误），不看 guard 的 error
	// 形态——重试耗尽的 error 是 join 包装，真正的失败口径在 finalResp/finalErr。
	err := rt.policy.guard(req.Context(), func(error) bool { return rt.failure(finalResp, finalErr) }, call)
	if err != nil {
		if finalResp != nil {
			finalResp.Body = io.NopCloser(bytes.NewReader(lastBuf))
			return finalResp, nil
		}
		return nil, err
	}
	return finalResp, nil
}