package bundle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/metrics"
)

// ---- 测试桩（记录调用序 + 捕获三元组/事件） ----

// callLog 记录各组件的调用顺序，用于契约断言。
type callLog struct {
	mu    sync.Mutex
	order []string
}

func (l *callLog) add(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.order = append(l.order, name)
}

func (l *callLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.order))
	copy(out, l.order)
	return out
}

// stubAuthn 认证桩：记录 "authn"，返回固定 claims。
type stubAuthn struct {
	log *callLog
}

func (s *stubAuthn) Authenticate(context.Context) (*authn.AuthClaims, error) {
	s.log.add("authn")
	return &authn.AuthClaims{Subject: "u-1", TenantID: "t-1"}, nil
}

func (s *stubAuthn) AuthenticateToken(string) (*authn.AuthClaims, error) {
	return &authn.AuthClaims{Subject: "u-1", TenantID: "t-1"}, nil
}

// authzCall 捕获一次授权判定的三元组。
type authzCall struct {
	subject, object, action string
}

// stubAuthz 授权桩：记录 "authz"，捕获三元组，按 allow 字段判定。
type stubAuthz struct {
	log   *callLog
	allow bool
	last  authzCall
}

func (s *stubAuthz) Authorize(_ context.Context, subject, object, action string) (bool, error) {
	s.log.add("authz")
	s.last = authzCall{subject: subject, object: object, action: action}
	return s.allow, nil
}

// stubAuditor 审计桩：记录 "audit"，捕获事件。
type stubAuditor struct {
	log    *callLog
	events []audit.AuditEvent
}

func (s *stubAuditor) Record(_ context.Context, ev audit.AuditEvent) {
	s.log.add("audit")
	s.events = append(s.events, ev)
}

// stubRecorder 指标桩：记录 "metrics"，捕获事件。
type stubRecorder struct {
	log     *callLog
	records []metrics.Event
}

func (s *stubRecorder) Record(_ context.Context, ev metrics.Event, _ metrics.Transport, _ float64) {
	s.log.add("metrics")
	s.records = append(s.records, ev)
}

// newFullBundle 构造四层全开的 Bundle（含归一化）。
// 注意：所有桩必须带 log（缺 log 会让桩内 nil deref panic 被 recordSafely
// 静默 recover，表现为「audit 不触发」——曾在此踩坑）。
func newFullBundle(log *callLog, allow bool) (*Bundle, *stubAuthz, *stubRecorder, *stubAuditor) {
	az := &stubAuthz{log: log, allow: allow}
	rec := &stubRecorder{log: log}
	aud := &stubAuditor{log: log}
	b := New(
		Authn(&stubAuthn{log: log}),
		Authz(az),
		Audit(aud),
		Metrics(rec),
		Normalized(),
	)
	return b, az, rec, aud
}

// ---- gin 链契约 ----

func TestGinChainOrder_Allow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := &callLog{}
	b, az, rec, aud := newFullBundle(log, true)

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/v1/secret/42", func(c *gin.Context) {
		log.add("handler")
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/secret/42", nil)
	req.Header.Set("Authorization", "Bearer tk")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// 契约：authn → authz → handler，c.Next() 返回后 audit → metrics。
	want := []string{"authn", "authz", "handler", "audit", "metrics"}
	if got := log.snapshot(); !equal(got, want) {
		t.Fatalf("gin chain order = %v, want %v", got, want)
	}
	// 契约：authz 拿到 authn 注入的 subject + P9 归一化 object/action。
	if az.last.subject != "u-1" {
		t.Errorf("authz subject = %q, want u-1 (authn must run first)", az.last.subject)
	}
	if az.last.object != "secret" || az.last.action != "get" {
		t.Errorf("authz not normalized: object=%q action=%q, want secret/get", az.last.object, az.last.action)
	}
	// 契约：audit 读到 authn 注入的 subject/tenant，且结果归一化。
	if len(aud.events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(aud.events))
	}
	ev := aud.events[0]
	if ev.Subject != "u-1" || ev.TenantID != "t-1" {
		t.Errorf("audit subject/tenant = %q/%q, want u-1/t-1", ev.Subject, ev.TenantID)
	}
	if ev.Object != "secret" || ev.Action != "get" || ev.Result != audit.ResultAllow {
		t.Errorf("audit event mismatch: %+v", ev)
	}
	// 契约：指标与审计同源 emit。
	if len(rec.records) != 1 || rec.records[0].Result != string(audit.ResultAllow) {
		t.Errorf("metrics records mismatch: %+v", rec.records)
	}
}

func TestGinChainOrder_Deny(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := &callLog{}
	b, _, _, aud := newFullBundle(log, false) // 授权拒绝

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/v1/secret/42", func(c *gin.Context) {
		log.add("handler") // 不应执行
		c.JSON(http.StatusOK, nil)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/secret/42", nil)
	req.Header.Set("Authorization", "Bearer tk")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// 契约：deny 时不进 handler；Audit 在 Authz 外侧，捕获 deny 最终结果。
	want := []string{"authn", "authz", "audit", "metrics"}
	if got := log.snapshot(); !equal(got, want) {
		t.Fatalf("gin chain order = %v, want %v", got, want)
	}
	if len(aud.events) != 1 || aud.events[0].Result != audit.ResultDeny {
		t.Fatalf("audit should capture deny, got %+v", aud.events)
	}
}

func TestGinRawDefaults_WithoutNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := &callLog{}
	az := &stubAuthz{log: log, allow: true}
	b := New(Authn(&stubAuthn{log: log}), Authz(az), Raw()) // 显式 opt-out 原始语义（D7 起默认归一化）

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/v1/secret/42", func(c *gin.Context) { c.JSON(http.StatusOK, nil) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/secret/42", nil)
	req.Header.Set("Authorization", "Bearer tk")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	// 契约（向后兼容）：默认 object=原始路径、action=HTTP 方法（P7 语义）。
	if az.last.object != "/v1/secret/42" || az.last.action != http.MethodGet {
		t.Fatalf("raw defaults: object=%q action=%q", az.last.object, az.last.action)
	}
}

// ---- gRPC 链契约 ----

// chainInvoke 按 grpc.ChainUnaryInterceptor 的语义组合拦截器（slice 序即外→内）
// 并调用，避免为单测起真实 server。
func chainInvoke(ics []grpc.UnaryServerInterceptor, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler, ctx context.Context, req any) (any, error) {
	h := handler
	for i := len(ics) - 1; i >= 0; i-- {
		ic := ics[i]
		next := h
		h = func(ctx context.Context, req any) (any, error) {
			return ic(ctx, req, info, next)
		}
	}
	return h(ctx, req)
}

func authedCtx() context.Context {
	return metadata.NewIncomingContext(context.Background(),
		metadata.Pairs("authorization", "Bearer tk"))
}

func TestGRPCChainOrder_Allow(t *testing.T) {
	log := &callLog{}
	b, az, rec, aud := newFullBundle(log, true)

	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	var handlerCalled bool
	resp, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) {
			log.add("handler")
			handlerCalled = true
			return "ok", nil
		}, authedCtx(), nil)
	if err != nil || resp != "ok" || !handlerCalled {
		t.Fatalf("resp=%v err=%v", resp, err)
	}
	// 契约：authn → authz → handler，handler 返回后 audit → metrics。
	want := []string{"authn", "authz", "handler", "audit", "metrics"}
	if got := log.snapshot(); !equal(got, want) {
		t.Fatalf("grpc chain order = %v, want %v", got, want)
	}
	if az.last.subject != "u-1" || az.last.object != "secret" || az.last.action != "get" {
		t.Errorf("authz not normalized: %+v", az.last)
	}
	if len(aud.events) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(aud.events))
	}
	ev := aud.events[0]
	if ev.Subject != "u-1" || ev.TenantID != "t-1" || ev.Object != "secret" || ev.Action != "get" || ev.Result != audit.ResultAllow {
		t.Errorf("audit event mismatch: %+v", ev)
	}
	if len(rec.records) != 1 || rec.records[0].Result != string(audit.ResultAllow) {
		t.Errorf("metrics records mismatch: %+v", rec.records)
	}
}

func TestGRPCChain_Deny(t *testing.T) {
	log := &callLog{}
	b, _, _, aud := newFullBundle(log, false) // 授权拒绝

	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	resp, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) {
			log.add("handler") // 不应执行
			return "ok", nil
		}, authedCtx(), nil)

	// 契约：authz 的 PermissionDenied 经最外层 Error 收口为 gRPC status。
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied (err=%v)", status.Code(err), err)
	}
	if resp != nil {
		t.Fatalf("resp should be nil, got %v", resp)
	}
	want := []string{"authn", "authz", "audit", "metrics"}
	if got := log.snapshot(); !equal(got, want) {
		t.Fatalf("grpc chain order = %v, want %v", got, want)
	}
	if len(aud.events) != 1 || aud.events[0].Result != audit.ResultDeny {
		t.Fatalf("audit should capture deny, got %+v", aud.events)
	}
}

func TestGRPCChain_ErrorMapping(t *testing.T) {
	// 契约：Error 必须最外层——handler 的 *berrors.Error 须被收口为携带
	// gRPC code + reason 的 status（否则被 status.Convert 兜底成空 Unknown）。
	log := &callLog{}
	b, _, _, aud := newFullBundle(log, true)

	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	berr := berrors.NotFound("SECRET_NOT_FOUND").WithMessage("not found")
	_, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) { return nil, berr },
		authedCtx(), nil)

	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v, want NotFound (err=%v)", status.Code(err), err)
	}
	st := status.Convert(err)
	if !strings.Contains(st.Message(), "not found") {
		t.Errorf("message should carry original text, got %q", st.Message())
	}
	// handler 出错时审计应记录 error 结果。
	if len(aud.events) != 1 || aud.events[0].Result != audit.ResultError {
		t.Fatalf("audit should capture error, got %+v", aud.events)
	}
}

// TestEmpty_NoOverhead 验证零依赖构造：链只剩基础层，请求直通。
func TestEmpty_NoOverhead(t *testing.T) {
	b := New()

	ginChain := b.Gin()
	// Recovery + RequestID + Logging，无认证/授权/审计层。
	if len(ginChain) != 3 {
		t.Fatalf("gin chain len = %d, want 3", len(ginChain))
	}
	grpcChain := b.GRPCInterceptors()
	// Error + Recovery + RequestID + Observability。
	if len(grpcChain) != 4 {
		t.Fatalf("grpc chain len = %d, want 4", len(grpcChain))
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(ginChain...)
	r.GET("/ping", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"pong": true}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	if _, err := chainInvoke(grpcChain, info,
		func(context.Context, any) (any, error) { return "ok", nil },
		context.Background(), nil); err != nil {
		t.Fatalf("empty chain should pass through, got %v", err)
	}

	// GRPCChain 返回的 ServerOption 应可被 grpc.NewServer 接受。
	srv := grpc.NewServer(b.GRPCChain()...)
	srv.Stop()
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- D3 契约：认证失败（authn abort）必产生审计事件 ----

// failingAuthn 认证失败桩：始终拒绝。
type failingAuthn struct{}

func (failingAuthn) Authenticate(context.Context) (*authn.AuthClaims, error) {
	return nil, context.Canceled
}

func (failingAuthn) AuthenticateToken(string) (*authn.AuthClaims, error) {
	return nil, context.Canceled
}

// TestDefaultNormalized 契约（D7）：New() 不显式传 Normalized() 也默认归一化——
// P9 的双命名空间根治不应依赖调用方记得 opt-in；Raw() 显式关闭。
func TestDefaultNormalized(t *testing.T) {
	log := &callLog{}
	az := &stubAuthz{log: log, allow: true}
	b := New(Authn(&stubAuthn{log: log}), Authz(az)) // 未传 Normalized()

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/v1/secret/42", func(c *gin.Context) { c.JSON(http.StatusOK, nil) })
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/secret/42", nil)
	req.Header.Set("Authorization", "Bearer tk")
	r.ServeHTTP(w, req)

	// 默认即归一化：object=secret、action=get（非原始路径/方法）。
	if az.last.object != "secret" || az.last.action != "get" {
		t.Fatalf("normalized by default: object=%q action=%q", az.last.object, az.last.action)
	}

	// Raw() 关闭归一化。
	az2 := &stubAuthz{log: log, allow: true}
	b2 := New(Authn(&stubAuthn{log: log}), Authz(az2), Raw())
	r2 := gin.New()
	r2.Use(b2.Gin()...)
	r2.GET("/v1/secret/42", func(c *gin.Context) { c.JSON(http.StatusOK, nil) })
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/v1/secret/42", nil)
	req2.Header.Set("Authorization", "Bearer tk")
	r2.ServeHTTP(w2, req2)

	if az2.last.object != "/v1/secret/42" || az2.last.action != http.MethodGet {
		t.Fatalf("Raw() should restore raw semantics: object=%q action=%q", az2.last.object, az2.last.action)
	}
}

// TestGRPCRecovery_PanicContained 契约（D5）：handler panic 被 Recovery 拦截器
// 捕获并转为 *berrors.Internal，由最外层 Error 收口为 Internal status，不打穿进程。
func TestGRPCRecovery_PanicContained(t *testing.T) {
	b := New()
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Method"}
	resp, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) {
			panic("boom")
		}, context.Background(), nil)

	if resp != nil {
		t.Fatalf("resp should be nil on panic, got %v", resp)
	}
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal (err=%v)", status.Code(err), err)
	}
	if !strings.Contains(status.Convert(err).Message(), "boom") {
		t.Fatalf("status message should carry panic value, got %q", status.Convert(err).Message())
	}
}

// TestGinAuthnFailureAudited 契约：受保护路由认证失败（401 abort）时，审计事件
// 必须由 authn 层显式发出——审计中间件注册在 Authn 内侧，abort 后不会执行。
func TestGinAuthnFailureAudited(t *testing.T) {
	gin.SetMode(gin.TestMode)
	log := &callLog{}
	aud := &stubAuditor{log: log}
	b := New(
		Authn(failingAuthn{}),
		Audit(aud),
		Normalized(),
	)

	r := gin.New()
	r.Use(b.Gin()...)
	r.GET("/protected", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/protected", nil)) // 无 Authorization 头

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
	// 契约核心：authn abort 路径留痕。
	if len(aud.events) != 1 {
		t.Fatalf("authn failure must emit exactly 1 audit event, got %+v", aud.events)
	}
	ev := aud.events[0]
	if ev.Object != "authn" || ev.Action != "authenticate" || ev.Result != audit.ResultDeny {
		t.Fatalf("audit event = %+v, want {Object:authn, Action:authenticate, Result:deny}", ev)
	}
	if ev.Meta["reason"] == nil || ev.Meta["path"] != "/protected" {
		t.Fatalf("audit event meta should carry reason and path, got %+v", ev.Meta)
	}
}

// TestGRPCAuthnFailureAudited 契约：gRPC 认证失败（Unauthenticated）时同样由
// authn 层显式发审计事件（内侧 Audit 拦截器不会执行）。
func TestGRPCAuthnFailureAudited(t *testing.T) {
	log := &callLog{}
	aud := &stubAuditor{log: log}
	b := New(
		Authn(failingAuthn{}),
		Audit(aud),
		Normalized(),
	)

	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	_, err := chainInvoke(b.GRPCInterceptors(), info,
		func(context.Context, any) (any, error) {
			t.Fatal("handler must not run on authn failure")
			return "ok", nil
		}, context.Background(), nil) // 无 authorization metadata

	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated (err=%v)", status.Code(err), err)
	}
	if len(aud.events) != 1 {
		t.Fatalf("authn failure must emit exactly 1 audit event, got %+v", aud.events)
	}
	ev := aud.events[0]
	if ev.Object != "authn" || ev.Action != "authenticate" || ev.Result != audit.ResultDeny {
		t.Fatalf("audit event = %+v, want {Object:authn, Action:authenticate, Result:deny}", ev)
	}
	if ev.Meta["full_method"] != info.FullMethod {
		t.Fatalf("audit event meta should carry full_method, got %+v", ev.Meta)
	}
}
