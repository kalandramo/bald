package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/log"
)

// nilLogger 满足 log.Logger（观测选项测试需要非 nil 注入）。
type nilLogger struct{}

func (nilLogger) Debug(context.Context, string, ...any) {}
func (nilLogger) Info(context.Context, string, ...any)  {}
func (nilLogger) Warn(context.Context, string, ...any)  {}
func (nilLogger) Error(context.Context, string, ...any) {}
func (nilLogger) Enabled(log.Level) bool                { return false }
func (nilLogger) With(...any) log.Logger                { return nilLogger{} }





func TestObservability_OptionsAndSkipMatcher(t *testing.T) {
	// WithLogger(nil) 不得覆盖默认；显式 logger 走注入分支。
	un := UnaryObservability(WithLogger(nil), WithLogger(nilLogger{}))
	if _, err := un(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("unary with options failed: %v", err)
	}
	st := StreamObservability(WithLogger(nil), WithLogger(nilLogger{}))
	if err := st(nil, &fakeServerStream{ctx: context.Background()},
		&grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(any, grpc.ServerStream) error { return nil }); err != nil {
		t.Fatalf("stream with options failed: %v", err)
	}

	cases := []struct {
		method, pattern string
		want            bool
	}{
		{"/svc/Get", "/svc/Get", true},
		{"/svc/Get", "/svc/*", true},
		{"/svc/Get", "/other/Get", false},
		{"/svc/Get", "/", false},
	}
	for _, c := range cases {
		if got := matchMethod(c.method, c.pattern); got != c.want {
			t.Errorf("matchMethod(%q, %q) = %v, want %v", c.method, c.pattern, got, c.want)
		}
	}
	if !shouldSkipMethod("/metrics", []string{"/health", "/metrics"}) {
		t.Error("shouldSkipMethod must scan all patterns")
	}
	if shouldSkipMethod("/svc/Get", nil) {
		t.Error("empty skip list must not skip")
	}
}


// ---------------------------------------------------------------------------
// 结构性零覆盖（登记备查）：
//   - audit.go:140 —— emitMetricsSafely 的 rec==nil 早退：唯一调用方
//     AuditInterceptor 在 cfg.metricsRecorder 为 nil 时已替换为 NopRecorder，
//     故 nil 不可达（双层校验的防御分支）。
//   - audit.go:184 —— recordSafely 的 auditor==nil 早退：AuditInterceptor 与
//     AuthnInterceptor 均在构造期把 nil 换成 NopAuditor，故 nil 不可达。
// 两处均保留为防御，不加直接单测（直接调用等于绕过公开入口伪造不变式）。
// ---------------------------------------------------------------------------
