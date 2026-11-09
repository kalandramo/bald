package grpc

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/metrics"
)

// nilLogger 满足 log.Logger（观测选项测试需要非 nil 注入）。
type nilLogger struct{}

func (nilLogger) Debug(context.Context, string, ...any) {}
func (nilLogger) Info(context.Context, string, ...any)  {}
func (nilLogger) Warn(context.Context, string, ...any)  {}
func (nilLogger) Error(context.Context, string, ...any) {}
func (nilLogger) Enabled(log.Level) bool                { return false }
func (nilLogger) With(...any) log.Logger                { return nilLogger{} }

// panicMetrics 验证指标旁路的 panic 降级。
type panicMetrics struct{}

func (panicMetrics) Record(context.Context, metrics.Event, metrics.Transport, float64) { panic("boom") }
func (panicMetrics) RecordActive(context.Context, metrics.Event, metrics.Transport, int64) {
	panic("boom")
}

func TestAuthnInterceptor_MissingTokenPaths(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/Get"}
	cases := []struct {
		name string
		ctx  context.Context
	}{
		{"no metadata", context.Background()},
		{"no authorization", metadata.NewIncomingContext(context.Background(), metadata.Pairs("x", "y"))},
		{"non-bearer scheme", metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("authorization", "Basic abc"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &auditMem{}
			ic := AuthnInterceptor(&stubAuthn{}, AuthnWithAuditor(a))
			_, err := ic(c.ctx, nil, info, func(context.Context, any) (any, error) {
				t.Fatal("handler must not run without token")
				return nil, nil
			})
			be, ok := berrors.FromError(err)
			if !ok || be.Reason != "MISSING_TOKEN" {
				t.Fatalf("want MISSING_TOKEN, got %v", err)
			}
			evs := a.all()
			if len(evs) != 1 {
				t.Fatalf("want 1 audit event, got %d", len(evs))
			}
			ev := evs[0]
			if ev.Object != "authn" || ev.Action != "authenticate" || ev.Result != audit.ResultDeny {
				t.Fatalf("unexpected audit event: %+v", ev)
			}
			if ev.Error != "MISSING_TOKEN" {
				t.Errorf("error should carry reason, got %q", ev.Error)
			}
			if ev.Meta["full_method"] != info.FullMethod {
				t.Errorf("meta full_method mismatch: %+v", ev.Meta)
			}
		})
	}
}

func TestAuthnInterceptor_AuthenticateError(t *testing.T) {
	a := &auditMem{}
	ic := AuthnInterceptor(&stubAuthn{err: errors.New("bad token")}, AuthnWithAuditor(a))
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer t"))
	_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(context.Context, any) (any, error) { return nil, nil })
	be, ok := berrors.FromError(err)
	if !ok || be.Reason != "UNAUTHENTICATED" {
		t.Fatalf("want UNAUTHENTICATED, got %v", err)
	}
	if evs := a.all(); len(evs) != 1 || evs[0].Result != audit.ResultDeny {
		t.Fatalf("want 1 deny event, got %+v", evs)
	}
}

func TestAuthnInterceptor_NilAuthenticatorPassthrough(t *testing.T) {
	ic := AuthnInterceptor(nil)
	resp, err := ic(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil || resp != "ok" {
		t.Fatalf("nil authenticator should pass through, got resp=%v err=%v", resp, err)
	}
}

// TestAuditAuthnFailure_NilInfo：认证失败时 info 可能为 nil（直接调用拦截器），
// full_method 必须留空而不是 panic。
func TestAuditAuthnFailure_NilInfo(t *testing.T) {
	a := &auditMem{}
	auditAuthnFailure(context.Background(), nil, a, "REASON")
	evs := a.all()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	if evs[0].Meta["full_method"] != "" {
		t.Errorf("nil info must yield empty full_method, got %v", evs[0].Meta["full_method"])
	}
}

func TestAuthzInterceptor_SubjectResolverAndEngineError(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/Get"}
	var gotSubject string
	ic := AuthzInterceptor(authz.Func(func(_ context.Context, subject, _, _ string) (bool, error) {
		gotSubject = subject
		return false, errors.New("engine down")
	}), WithSubjectResolver(func(context.Context, *grpc.UnaryServerInfo) string { return "svc-x" }))
	_, err := ic(context.Background(), nil, info, func(context.Context, any) (any, error) {
		t.Fatal("handler must not run when engine errors")
		return nil, nil
	})
	be, ok := berrors.FromError(err)
	if !ok || be.Reason != "AUTHZ_ENGINE_ERROR" {
		t.Fatalf("want AUTHZ_ENGINE_ERROR, got %v", err)
	}
	if gotSubject != "svc-x" {
		t.Fatalf("subject resolver not applied, got %q", gotSubject)
	}
}

func TestAuditInterceptor_SubjectResolverAndStatusDeny(t *testing.T) {
	// 无 auditor 注入时退化为 Nop（不得 panic）；subject 走自定义 resolver；
	// handler 返回原生 gRPC status（非 berrors）也必须识别为 deny。
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/Delete"}
	ev := make(chan audit.AuditEvent, 1)
	inter := AuditInterceptor(
		AuditWithAuditor(&auditChan{ch: ev}),
		AuditWithSubjectResolver(func(context.Context, *grpc.UnaryServerInfo) string { return "svc-a" }),
	)
	_, err := inter(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, status.Error(codes.PermissionDenied, "denied")
	})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("handler error should pass through, got %v", err)
	}
	got := <-ev
	if got.Subject != "svc-a" || got.TenantID != "" {
		t.Errorf("resolver subject/tenant mismatch: %+v", got)
	}
	if got.Result != audit.ResultDeny {
		t.Errorf("status-code PermissionDenied must map to deny, got %q", got.Result)
	}

	// 默认 auditor（无选项）路径不得 panic。
	if _, err := AuditInterceptor()(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
		t.Fatalf("default auditor path failed: %v", err)
	}
}

// auditChan 把事件送入 channel，便于断言后立即返回。
type auditChan struct{ ch chan audit.AuditEvent }

func (a *auditChan) Record(_ context.Context, ev audit.AuditEvent) { a.ch <- ev }

func TestAuditInterceptor_PanickingMetricsSafe(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/svc/Get"}
	resp, err := AuditInterceptor(AuditWithMetrics(panicMetrics{}))(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil || resp != "ok" {
		t.Fatalf("panicking metrics recorder must not block handler, got resp=%v err=%v", resp, err)
	}
}

func TestDefaulterInterceptor(t *testing.T) {
	v := 0
	req := &defaultReq{n: &v}
	if _, err := DefaulterInterceptor()(context.Background(), req, &grpc.UnaryServerInfo{},
		func(_ context.Context, r any) (any, error) {
			if r.(*defaultReq).n == nil || *r.(*defaultReq).n != 42 {
				t.Fatal("Default() not applied before handler")
			}
			return r, nil
		}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 非 RequestDefaulter 请求原样透传。
	if _, err := DefaulterInterceptor()(context.Background(), 1, &grpc.UnaryServerInfo{},
		func(_ context.Context, r any) (any, error) { return r, nil }); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
}

type defaultReq struct{ n *int }

func (r *defaultReq) Default() { *r.n = 42 }

func TestRecoveryInterceptor(t *testing.T) {
	ri := RecoveryInterceptor()
	resp, err := ri(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(context.Context, any) (any, error) { panic("boom") })
	be, ok := berrors.FromError(err)
	if !ok || be.Reason != "PANIC" {
		t.Fatalf("want PANIC error, got %v", err)
	}
	if resp != nil {
		t.Errorf("panic must yield nil response, got %v", resp)
	}
	// info 为 nil 时同样兜底。
	if _, err := ri(context.Background(), nil, nil,
		func(context.Context, any) (any, error) { panic("boom") }); err == nil {
		t.Fatal("panic with nil info must still be recovered")
	}
	// 正常返回原样透传。
	resp, err = ri(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil || resp != "ok" {
		t.Fatalf("pass-through mismatch: resp=%v err=%v", resp, err)
	}
}

func TestRequestIDInterceptor_ReusesAndGenerates(t *testing.T) {
	ri := RequestIDInterceptor()

	t.Run("reuses incoming id", func(t *testing.T) {
		ctx := metadata.NewIncomingContext(context.Background(),
			metadata.Pairs("x-request-id", "rid-123"))
		_, err := ri(ctx, nil, &grpc.UnaryServerInfo{}, func(hctx context.Context, _ any) (any, error) {
			if got := contextx.RequestIDFromContext(hctx); got != "rid-123" {
				t.Errorf("request id = %q, want rid-123", got)
			}
			return nil, nil
		})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})

	t.Run("generates when absent", func(t *testing.T) {
		_, err := ri(context.Background(), nil, &grpc.UnaryServerInfo{},
			func(hctx context.Context, _ any) (any, error) {
				if got := contextx.RequestIDFromContext(hctx); got == "" {
					t.Error("generated request id must not be empty")
				}
				md, ok := metadata.FromIncomingContext(hctx)
				if !ok || len(md.Get("x-request-id")) == 0 {
					t.Error("generated id must be written back to incoming metadata")
				}
				return nil, nil
			})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	})
}

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

func TestAuthnWithAuditor_NilFallsBackToNop(t *testing.T) {
	// 显式传 nil auditor 也必须退化为 Nop（不 panic）。
	ic := AuthnInterceptor(&stubAuthn{claims: &authn.AuthClaims{Subject: "u"}}, AuthnWithAuditor(nil))
	ctx := metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer t"))
	if _, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(context.Context, any) (any, error) { return nil, nil }); err != nil {
		t.Fatalf("unexpected err: %v", err)
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
