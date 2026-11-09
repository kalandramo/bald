// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// middleware_batch5d_test.go 收口统一内置件的 0% 选项闭包与协议无关小访问器，
// 并驱动 audit/authz/observability/bridge_gin 的剩余分支（旁路 panic 降级、
// 解析器覆盖、状态面化回退）。

package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"github.com/kalandramo/bald/pkg/contextx"
	pmetrics "github.com/kalandramo/bald/pkg/metrics"
)

// --- transporter 小访问器 ---

func TestTransporterSmallAccessors5d(t *testing.T) {
	if KindHTTP.String() != "HTTP" || KindGRPC.String() != "gRPC" {
		t.Errorf("Kind.String = %q/%q", KindHTTP.String(), KindGRPC.String())
	}

	mh := NewMetadataHeader(metadata.MD{"a": {"1"}, "b": {"2"}})
	if got := mh.Keys(); len(got) != 2 {
		t.Errorf("metadataHeader.Keys = %v, want 2 entries", got)
	}

	tr := NewTransporter(KindHTTP, "GET /", "127.0.0.1:8080",
		NewHTTPHeader(http.Header{}), NewHTTPHeader(http.Header{}))
	defer ReleaseTransporter(tr)
	if tr.Endpoint() != "127.0.0.1:8080" {
		t.Errorf("Endpoint = %q", tr.Endpoint())
	}
}

// --- audit 选项闭包 ---

func TestAuditOptionClosures5d(t *testing.T) {
	var o auditOptions
	AuditWithObjectResolver(func(string) string { return "obj" })(&o)
	if o.objectResolver == nil || o.objectResolver("x") != "obj" {
		t.Error("AuditWithObjectResolver closure broken")
	}
	AuditWithActionResolver(func(string) string { return "act" })(&o)
	if o.actionResolver == nil || o.actionResolver("x") != "act" {
		t.Error("AuditWithActionResolver closure broken")
	}
	AuditWithSubjectResolver(func(context.Context, string) string { return "sub" })(&o)
	if o.subjectResolver == nil {
		t.Error("AuditWithSubjectResolver closure broken")
	}
	rec := pmetrics.NopRecorder()
	AuditWithMetrics(rec)(&o)
	if o.metricsRecorder != rec {
		t.Error("AuditWithMetrics closure broken")
	}
}

func TestLastSpace5d(t *testing.T) {
	if lastSpace("GET /v1/x") != 3 {
		t.Errorf(`lastSpace("GET /v1/x") = %d, want 3`, lastSpace("GET /v1/x"))
	}
	if lastSpace("nosep") != -1 || lastSpace("") != -1 {
		t.Error("lastSpace without space should be -1")
	}
}

func TestAuditObjectAction5d(t *testing.T) {
	// HTTP 带空格 → 动词/模板拆分。
	obj, act := auditObjectAction(&auditOptions{}, "POST /v1/thing", pmetrics.TransportHTTP)
	if obj != "/v1/thing" || act != "POST" {
		t.Errorf("http split = %q/%q", obj, act)
	}
	// HTTP 无空格 → action 保持默认 GET，object=operation。
	obj, act = auditObjectAction(&auditOptions{}, "single", pmetrics.TransportHTTP)
	if obj != "single" || act != "GET" {
		t.Errorf("http no-space = %q/%q", obj, act)
	}
	// gRPC → action 恒 CALL。
	obj, act = auditObjectAction(&auditOptions{}, "/pkg.Svc/M", pmetrics.TransportGRPC)
	if obj != "/pkg.Svc/M" || act != "CALL" {
		t.Errorf("grpc = %q/%q", obj, act)
	}
	// 解析器覆盖。
	cfg := &auditOptions{
		objectResolver: func(string) string { return "O" },
		actionResolver: func(string) string { return "A" },
	}
	obj, act = auditObjectAction(cfg, "GET /x", pmetrics.TransportHTTP)
	if obj != "O" || act != "A" {
		t.Errorf("resolver override = %q/%q", obj, act)
	}
}

func TestStatusHint5d(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{nil, 200},
		{berrors.New(berrors.CodePermissionDenied, "R"), 403},
		{berrors.New(berrors.CodeResourceExhausted, "R"), 429},
		{berrors.New(berrors.CodeUnauthenticated, "R"), 401},
		{berrors.New(berrors.CodeInvalidArgument, "R"), 400},
		{berrors.New(berrors.CodeNotFound, "R"), 500}, // 命中 FromError 但不在特判码内 → 兜底 500
		{errors.New("plain"), 500},                    // 非 berrors → 兜底 500
	}
	for _, c := range cases {
		if got := statusHint(c.err); got != c.want {
			t.Errorf("statusHint(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}

func TestAuditSubjectTenant5d(t *testing.T) {
	// subjectResolver 命中：忽略 claims，返回 resolver 值。
	cfg := &auditOptions{subjectResolver: func(context.Context, string) string { return "S" }}
	sub, ten := auditSubjectTenant(context.Background(), cfg, "op")
	if sub != "S" || ten != "" {
		t.Errorf("resolver subject/tenant = %q/%q", sub, ten)
	}

	// 有 claims：subject/tenant 取自 claims。
	ctx := authn.ContextWithAuthClaims(context.Background(), &authn.AuthClaims{Subject: "usr", TenantID: "t9"})
	sub, ten = auditSubjectTenant(ctx, &auditOptions{}, "op")
	if sub != "usr" || ten != "t9" {
		t.Errorf("claims subject/tenant = %q/%q", sub, ten)
	}

	// 无 claims 无 resolver：双空。
	sub, ten = auditSubjectTenant(context.Background(), &auditOptions{}, "op")
	if sub != "" || ten != "" {
		t.Errorf("empty subject/tenant = %q/%q", sub, ten)
	}
}

// panickingRecorder 在 Record/RecordActive 时 panic，驱动旁路降级分支。
type panickingRecorder struct{}

func (panickingRecorder) Record(context.Context, pmetrics.Event, pmetrics.Transport, float64) {
	panic("rec boom")
}
func (panickingRecorder) RecordActive(context.Context, pmetrics.Event, pmetrics.Transport, int64) {
}

// capturingRecorder 记录是否被调用（驱动 emitMetricsSafely 正常路径）。
type capturingRecorder struct{ n int }

func (c *capturingRecorder) Record(context.Context, pmetrics.Event, pmetrics.Transport, float64) {
	c.n++
}
func (c *capturingRecorder) RecordActive(context.Context, pmetrics.Event, pmetrics.Transport, int64) {
}

func TestEmitMetricsSafely5d(t *testing.T) {
	ctx := context.Background()
	// nil recorder 早退，不 panic。
	emitMetricsSafely(nil, ctx, pmetrics.Event{}, pmetrics.TransportHTTP, 0.1)
	// 正常路径：Record 被调用。
	cap := &capturingRecorder{}
	emitMetricsSafely(cap, ctx, pmetrics.Event{}, pmetrics.TransportHTTP, 0.1)
	if cap.n != 1 {
		t.Errorf("recorder calls = %d, want 1", cap.n)
	}
	// panic 降级：recover 吞掉，不向上传播。
	emitMetricsSafely(panickingRecorder{}, ctx, pmetrics.Event{}, pmetrics.TransportHTTP, 0.1)
}

// panickingAuditor 在 Record 时 panic，驱动 recordSafely 降级分支。
type panickingAuditor struct{}

func (panickingAuditor) Record(context.Context, audit.AuditEvent) { panic("aud boom") }

func TestRecordSafelyPanics5d(t *testing.T) {
	recordSafely(nil, context.Background(), audit.AuditEvent{})
	recordSafely(panickingAuditor{}, context.Background(), audit.AuditEvent{})
}

// --- authz 选项闭包 + 剩余分支 ---

func TestAuthzOptionClosures5d(t *testing.T) {
	var o authzOptions
	WithObjectResolver(func(string) string { return "O" })(&o)
	if o.objectResolver == nil {
		t.Error("WithObjectResolver closure broken")
	}
	WithActionResolver(func(string) string { return "A" })(&o)
	if o.actionResolver == nil {
		t.Error("WithActionResolver closure broken")
	}
	WithSubjectResolver(func(context.Context, string) string { return "S" })(&o)
	if o.subjectResolver == nil {
		t.Error("WithSubjectResolver closure broken")
	}
}

func TestAuthzEngineError5d(t *testing.T) {
	eng := authz.AuthorizerFunc(func(context.Context, string, string, string) (bool, error) {
		return false, errors.New("engine down")
	})
	m := Authz(eng)
	ctx := authn.ContextWithAuthClaims(transporterWithBearer(context.Background(), "good"),
		&authn.AuthClaims{Subject: "usr"})
	_, err := m(func(context.Context, interface{}) (interface{}, error) { return nil, nil })(ctx, nil)
	w, ok := berrors.FromError(err)
	if !ok || w.Code != berrors.CodeInternal || w.Reason != "AUTHZ_ENGINE_ERROR" {
		t.Fatalf("engine error = %v, want Internal/AUTHZ_ENGINE_ERROR", err)
	}
}

func TestAuthzResolvers5d(t *testing.T) {
	var seenS, seenO, seenA string
	allow := authz.AuthorizerFunc(func(_ context.Context, s, o, a string) (bool, error) {
		seenS, seenO, seenA = s, o, a
		return true, nil
	})
	m := Authz(allow,
		WithSubjectResolver(func(context.Context, string) string { return "SOLVED" }),
		WithObjectResolver(func(string) string { return "OBJ" }),
		WithActionResolver(func(string) string { return "ACT" }),
	)
	ctx := authn.ContextWithAuthClaims(transporterWithBearer(context.Background(), "good"),
		&authn.AuthClaims{Subject: "ignored"})
	if _, err := m(func(context.Context, interface{}) (interface{}, error) { return nil, nil })(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if seenS != "SOLVED" || seenO != "OBJ" || seenA != "ACT" {
		t.Errorf("resolver triples = %q/%q/%q", seenS, seenO, seenA)
	}
}

// --- observability 选项闭包 + resolveLogger + bodyCaptureWriter.Write ---

func TestResolveLogger5d(t *testing.T) {
	// 未注入 → 惰性取全局（非 nil）。
	if (&observabilityOptions{}).resolveLogger() == nil {
		t.Error("resolveLogger with no injection should return global logger")
	}
	// 注入优先。
	lg := log.GetLogger()
	got := (&observabilityOptions{logger: lg}).resolveLogger()
	if got != lg {
		t.Error("resolveLogger should return injected logger")
	}
}

func TestObservabilityOptionClosures5d(t *testing.T) {
	var o observabilityOptions
	ObservabilityWithTraceInjection(InjectW3CTraceContext, "x-trace")(&o)
	if o.traceMode != InjectW3CTraceContext || o.customTraceHeader != "x-trace" {
		t.Error("ObservabilityWithTraceInjection closure broken")
	}
	lg := log.GetLogger()
	ObservabilityWithLogger(lg)(&o)
	if o.logger != lg {
		t.Error("ObservabilityWithLogger closure broken")
	}
	ObservabilityWithLogger(nil)(&o) // nil 不覆盖
	if o.logger != lg {
		t.Error("ObservabilityWithLogger(nil) should not overwrite")
	}
	var o2 observabilityOptions
	ObservabilityWithDisableBodyLog(true)(&o2)
	if !o2.disableBodyLog {
		t.Error("ObservabilityWithDisableBodyLog closure broken")
	}
}

func TestBodyCaptureWriterWrite5d(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	body := &bytes.Buffer{}
	cw := &bodyCaptureWriter{ResponseWriter: c.Writer, body: body}
	n, err := cw.Write([]byte("hello"))
	if err != nil || n != len("hello") {
		t.Fatalf("Write = (%d, %v)", n, err)
	}
	if body.String() != "hello" {
		t.Errorf("captured body = %q", body.String())
	}
	if w.Body.String() != "hello" {
		t.Errorf("underlying body = %q", w.Body.String())
	}
}

// --- bridge_gin: FromGin + httpStatusError 回退分支 ---

func TestFromGin5d(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ran := false
	h := FromGin(func(*gin.Context) { ran = true })
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	resp, err := h(context.Background(), c)
	if err != nil || resp != nil {
		t.Fatalf("FromGin handler = (%v, %v)", resp, err)
	}
	if !ran {
		t.Error("FromGin did not invoke the wrapped gin handler")
	}
}

// --- bearerFromTransporter 剩余错误分支 ---

func TestBearerFromTransporterBranches5d(t *testing.T) {
	// 无 Transporter。
	if _, err := bearerFromTransporter(context.Background()); err == nil {
		t.Error("no transporter should error")
	}

	// 有 Transporter 但 authorization 值为空列表。
	tr := NewTransporter(KindGRPC, "/pkg.Svc/M", "", NewMetadataHeader(metadata.MD{}), NewMetadataHeader(metadata.MD{}))
	defer ReleaseTransporter(tr)
	if _, err := bearerFromTransporter(NewServerContext(context.Background(), tr)); err == nil {
		t.Error("empty authorization values should error")
	}

	// 有值但缺 "Bearer " 前缀。
	tr2 := NewTransporter(KindGRPC, "/pkg.Svc/M", "", NewMetadataHeader(metadata.MD{}), NewMetadataHeader(metadata.MD{}))
	defer ReleaseTransporter(tr2)
	tr2.RequestHeader().Set("authorization", "Token abc")
	if _, err := bearerFromTransporter(NewServerContext(context.Background(), tr2)); err == nil {
		t.Error("missing Bearer prefix should error")
	}

	// 正常路径。
	tr3 := NewTransporter(KindGRPC, "/pkg.Svc/M", "", NewMetadataHeader(metadata.MD{}), NewMetadataHeader(metadata.MD{}))
	defer ReleaseTransporter(tr3)
	tr3.RequestHeader().Set("authorization", "Bearer abc")
	tok, err := bearerFromTransporter(NewServerContext(context.Background(), tr3))
	if err != nil || tok != "abc" {
		t.Errorf("bearer = %q, err = %v", tok, err)
	}
}

// --- RateLimit 剩余分支 ---

// funcLimiter 按参数化行为满足 ratelimit.Limiter。
type funcLimiter struct {
	allowOK  bool
	allowErr error
	waitErr  error
}

func (f *funcLimiter) Allow() (bool, error)       { return f.allowOK, f.allowErr }
func (f *funcLimiter) Wait(context.Context) error { return f.waitErr }
func (f *funcLimiter) Close() error               { return nil }

func TestRateLimitBranches5d(t *testing.T) {
	pass := func(context.Context, interface{}) (interface{}, error) { return "ok", nil }
	ctx := context.Background()

	// l == nil 直通。
	resp, err := RateLimit(nil, false)(pass)(ctx, nil)
	if err != nil || resp != "ok" {
		t.Errorf("nil limiter should pass: %v/%v", resp, err)
	}

	// wait=false, Allow 报错。
	_, err = RateLimit(&funcLimiter{allowErr: errors.New("limiter down")}, false)(pass)(ctx, nil)
	if w, ok := berrors.FromError(err); !ok || w.Code != berrors.CodeResourceExhausted {
		t.Errorf("allow error = %v", err)
	}

	// wait=false, Allow 放行。
	if _, err := RateLimit(&funcLimiter{allowOK: true}, false)(pass)(ctx, nil); err != nil {
		t.Errorf("allow ok should pass: %v", err)
	}

	// wait=true, Wait 成功。
	if _, err := RateLimit(&funcLimiter{}, true)(pass)(ctx, nil); err != nil {
		t.Errorf("wait ok should pass: %v", err)
	}

	// wait=true, Wait 报错。
	_, err = RateLimit(&funcLimiter{waitErr: errors.New("canceled")}, true)(pass)(ctx, nil)
	if w, ok := berrors.FromError(err); !ok || w.Code != berrors.CodeResourceExhausted {
		t.Errorf("wait error = %v", err)
	}
}

func TestHttpStatusErrorFallback5d(t *testing.T) {
	if httpStatusError(http.StatusOK) != nil {
		t.Error("2xx should be nil")
	}
	if httpStatusError(http.StatusNotFound) == nil {
		t.Error("404 (in map) should be non-nil")
	}
	// 状态码 >= 400 但不在预填 [400,600) 表内 → 走末路回退构造。
	if e := httpStatusError(600); e == nil {
		t.Error("600 (unmapped) should build a fallback error")
	} else if w, ok := berrors.FromError(e); !ok || w.Reason != httpStatusReason(600) {
		t.Errorf("fallback error mismatch: %v", e)
	}
}

// --- observability debug 级 body 捕获外围（observeHTTP / observeGRPC） ---

// dbgLogger 恒 Debug 启用，令 shouldLogBody 为真，驱动 body 捕获分支。
type dbgLogger struct{}

func (dbgLogger) Debug(context.Context, string, ...any) {}
func (dbgLogger) Info(context.Context, string, ...any)  {}
func (dbgLogger) Warn(context.Context, string, ...any)  {}
func (dbgLogger) Error(context.Context, string, ...any) {}
func (dbgLogger) Enabled(log.Level) bool                { return true }
func (l dbgLogger) With(...any) log.Logger              { return l }

// validSpanTracer 返回携带有效 SpanContext 的 ctx，令 InjectTrace 越过
// 「spanCtx 无效早退」真正调用 set（no-op tracer 下恒全零会跳过注入闭包）。
type validSpanTracer struct{ trace.Tracer }

func (validSpanTracer) Start(ctx context.Context, _ string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	tid, _ := trace.TraceIDFromHex("0af7651916cd43dd8448eb211c80319c")
	sid, _ := trace.SpanIDFromHex("5759e988d8fa739c")
	sc := trace.NewSpanContext(trace.SpanContextConfig{TraceID: tid, SpanID: sid, TraceFlags: trace.FlagsSampled})
	ctx = trace.ContextWithRemoteSpanContext(ctx, sc)
	return ctx, trace.SpanFromContext(ctx)
}

func TestObservabilityDebugBodyHTTP5d(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/v1/b", GinHandler(Observability(ObservabilityWithLogger(dbgLogger{}))),
		func(c *gin.Context) { c.String(http.StatusOK, "resp-body") })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/b", strings.NewReader("req-payload"))
	req.Header.Set("Content-Type", "text/plain")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "resp-body" {
		t.Fatalf("response = (%d, %q)", w.Code, w.Body.String())
	}
}

func TestObservabilityDebugBodyGRPCUnary5d(t *testing.T) {
	mdCtx := metadata.NewIncomingContext(context.Background(), metadata.MD{"x-a": {"1"}})
	_, err := UnaryServerInterceptor(Observability(
		ObservabilityWithTracer(validSpanTracer{}),
		ObservabilityWithLogger(dbgLogger{}),
		ObservabilityWithTraceInjection(InjectBoth, "x-trace-id"),
	))(
		mdCtx, map[string]any{"in": 1}, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(context.Context, any) (any, error) { return map[string]any{"out": 2}, nil })
	if err != nil {
		t.Fatalf("unary debug body: %v", err)
	}
}

func TestObservabilityDebugStreamGRPC5d(t *testing.T) {
	mdCtx := metadata.NewIncomingContext(context.Background(), metadata.MD{"x": {"1"}})
	err := StreamServerInterceptor(Observability(
		ObservabilityWithLogger(dbgLogger{}),
		ObservabilityWithStream(),
	))(nil, &fakeServerStream{ctx: mdCtx}, &grpc.StreamServerInfo{FullMethod: "/svc/Watch"},
		func(_ any, _ grpc.ServerStream) error { return nil })
	if err != nil {
		t.Fatalf("stream debug: %v", err)
	}
}

// --- RequestID 剩余分支 ---

func TestRequestIDBranches5d(t *testing.T) {
	// 无 Transporter：仅注入 context，不读写头。
	var seen string
	_, _ = RequestID()(func(ctx context.Context, _ interface{}) (interface{}, error) {
		seen = contextx.RequestIDFromContext(ctx)
		return nil, nil
	})(context.Background(), nil)
	if seen == "" {
		t.Error("RequestID without transporter should still inject generated id")
	}

	// 自定义头名 + 入站头已有值 → 透传。
	tr := NewTransporter(KindGRPC, "/svc/M", "", NewMetadataHeader(metadata.MD{}), NewMetadataHeader(metadata.MD{}))
	defer ReleaseTransporter(tr)
	tr.RequestHeader().Set("x-custom", "custom-id")
	_, _ = RequestID(RequestIDConfig{HeaderName: "x-custom"})(func(context.Context, interface{}) (interface{}, error) {
		return nil, nil
	})(NewServerContext(context.Background(), tr), nil)
	if tr.ReplyHeader().Get("x-custom") != "custom-id" {
		t.Errorf("reply header = %q, want custom-id", tr.ReplyHeader().Get("x-custom"))
	}
}

// --- Authn 剩余分支 ---

// staticAuthenticator 返回固定 claims（用于构造空 TenantID 场景）。
type staticAuthenticator struct{ claims *authn.AuthClaims }

func (s staticAuthenticator) Authenticate(context.Context) (*authn.AuthClaims, error) {
	return s.claims, nil
}
func (s staticAuthenticator) AuthenticateToken(string) (*authn.AuthClaims, error) {
	return s.claims, nil
}

func TestAuthnNilAndEmptyTenant5d(t *testing.T) {
	// authenticator == nil 直通。
	called := false
	_, err := Authn(nil)(func(context.Context, interface{}) (interface{}, error) {
		called = true
		return nil, nil
	})(context.Background(), nil)
	if err != nil || !called {
		t.Errorf("nil authenticator should pass: called=%v err=%v", called, err)
	}

	// 有效 token 但空 TenantID → 跳过 WithTenantID 分支。
	ctx := transporterWithBearer(context.Background(), "good")
	_, err = Authn(staticAuthenticator{claims: &authn.AuthClaims{Subject: "s"}})(
		func(context.Context, interface{}) (interface{}, error) { return nil, nil })(ctx, nil)
	if err != nil {
		t.Fatalf("empty tenant authn: %v", err)
	}

	// token 存在但校验失败 → Authenticate 错误分支（UNAUTHENTICATED + deny 审计）。
	aud := &recordingAuditor{}
	_, err = Authn(&fakeAuthenticator{valid: map[string]bool{"good": true}}, AuthnWithAuditor(aud))(
		func(context.Context, interface{}) (interface{}, error) { return nil, nil })(
		transporterWithBearer(context.Background(), "bad"), nil)
	w, ok := berrors.FromError(err)
	if !ok || w.Code != berrors.CodeUnauthenticated || w.Reason != "UNAUTHENTICATED" {
		t.Fatalf("bad token err = %v, want Unauthenticated/UNAUTHENTICATED", err)
	}
	if len(aud.events) == 0 || aud.events[len(aud.events)-1].Result != audit.ResultDeny {
		t.Error("authenticate failure should emit deny audit event")
	}
}
