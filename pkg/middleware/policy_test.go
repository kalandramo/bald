// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/ratelimit"
)

// 本文件是策略面统一内置件（Authn/Authz/Audit/RateLimit/Validator/Defaulter）
// 的验收面：每件断言统一链语义 + gin 桥的协议落点。

// --- 测试脚手架 ---

// fakeAuthenticator 按 token 表认证。
type fakeAuthenticator struct{ valid map[string]bool }

func (f *fakeAuthenticator) Authenticate(ctx context.Context) (*authn.AuthClaims, error) {
	token := authn.TokenFromContext(ctx)
	return f.AuthenticateToken(token)
}

func (f *fakeAuthenticator) AuthenticateToken(token string) (*authn.AuthClaims, error) {
	if f.valid[token] {
		return &authn.AuthClaims{Subject: "usr-" + token, TenantID: "t1"}, nil
	}
	return nil, errors.New("bad token")
}

// recordingAuditor 捕获审计事件供断言。
type recordingAuditor struct{ events []audit.AuditEvent }

func (a *recordingAuditor) Record(_ context.Context, ev audit.AuditEvent) {
	a.events = append(a.events, ev)
}

// denyLimiter 恒拒绝的限流器。
type denyLimiter struct{}

func (d *denyLimiter) Allow() (bool, error)           { return false, nil }
func (d *denyLimiter) Wait(ctx context.Context) error { return nil }
func (d *denyLimiter) Close() error                   { return nil }

var _ ratelimit.Limiter = (*denyLimiter)(nil)

// transporterWithBearer 构造携带 Authorization 头的 gRPC 面 Transporter ctx。
func transporterWithBearer(ctx context.Context, token string) context.Context {
	tr := NewTransporter(KindGRPC, "/pkg.Svc/M", "", NewMetadataHeader(metadata.MD{}), NewMetadataHeader(metadata.MD{}))
	if token != "" {
		tr.RequestHeader().Set("authorization", "Bearer "+token)
	}
	return NewServerContext(ctx, tr)
}

// --- Authn ---

// TestAuthnUnified 双面断言：有效 token → claims 注入 ctx（subject/tenant）；
// 缺 token → Unauthenticated(MISSING_TOKEN) + deny 审计事件。
func TestAuthnUnified(t *testing.T) {
	auth := &fakeAuthenticator{valid: map[string]bool{"good": true}}
	aud := &recordingAuditor{}
	m := Authn(auth, AuthnWithAuditor(aud))

	var subject, tenant string
	ctx := transporterWithBearer(context.Background(), "good")
	_, err := m(func(ctx context.Context, _ interface{}) (interface{}, error) {
		if claims := authn.AuthClaimsFromContext(ctx); claims != nil {
			subject, tenant = claims.Subject, claims.TenantID
		}
		return nil, nil
	})(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if subject != "usr-good" || tenant != "t1" {
		t.Errorf("claims = %s/%s", subject, tenant)
	}

	// 缺 token。
	before := len(aud.events)
	_, err = m(func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil })(
		transporterWithBearer(context.Background(), ""), nil)
	if !isUnauthenticated(err) {
		t.Fatalf("missing token err = %v, want Unauthenticated", err)
	}
	if len(aud.events) != before+1 || aud.events[len(aud.events)-1].Result != audit.ResultDeny {
		t.Error("deny audit event missing")
	}
}

func isUnauthenticated(err error) bool {
	w, ok := berrors.FromError(err)
	return ok && w.Code == berrors.CodeUnauthenticated
}

// --- Authz ---

// TestAuthzUnifiedTriples 默认三元组：subject=claims.Subject、object=operation、
// action=gRPC "CALL"；拒绝为 PermissionDenied(ACCESS_DENIED)。
func TestAuthzUnifiedTriples(t *testing.T) {
	var seenS, seenO, seenA string
	deny := authz.AuthorizerFunc(func(_ context.Context, subject, object, action string) (bool, error) {
		seenS, seenO, seenA = subject, object, action
		return subject == "usr-good", nil
	})
	m := Authz(deny)
	claims := authn.AuthClaims{Subject: "usr-good"}

	ctx := authn.ContextWithAuthClaims(transporterWithBearer(context.Background(), "good"), &claims)
	if _, err := m(func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil })(ctx, nil); err != nil {
		t.Fatalf("allowed call should pass: %v", err)
	}
	if seenS != "usr-good" || seenO != "/pkg.Svc/M" || seenA != "CALL" {
		t.Fatalf("triples = %s/%s/%s", seenS, seenO, seenA)
	}

	// 另一 subject 被拒。
	ctx2 := authn.ContextWithAuthClaims(transporterWithBearer(context.Background(), "good"), &authn.AuthClaims{Subject: "usr-evil"})
	_, err := m(func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil })(ctx2, nil)
	werr, ok := berrors.FromError(err)
	if !ok || werr.Code != berrors.CodePermissionDenied || werr.Reason != "ACCESS_DENIED" {
		t.Fatalf("denied err = %v, want PermissionDenied/ACCESS_DENIED", err)
	}
}

// TestAuthzUnifiedHTTPAction HTTP 面默认 action 取动词（经 Transporter.Method）。
func TestAuthzUnifiedHTTPAction(t *testing.T) {
	var seenA string
	deny := authz.AuthorizerFunc(func(_ context.Context, _, _, action string) (bool, error) {
		seenA = action
		return true, nil
	})
	m := Authz(deny)

	tr := NewTransporter(KindHTTP, "GET /v1/thing", "", NewHTTPHeader(http.Header{}), NewHTTPHeader(http.Header{}),
		WithHTTPRequest("GET", "/v1/thing", "/v1/thing", "1.1"))
	_, err := m(func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil })(
		NewServerContext(context.Background(), tr), nil)
	if err != nil {
		t.Fatal(err)
	}
	if seenA != "GET" {
		t.Errorf("action = %q, want GET", seenA)
	}
}

// --- RateLimit ---

// TestRateLimitUnified 拒绝语义：ResourceExhausted(RATE_LIMITED)，统一链直调
// 断言 + gin 桥渲染断言（CodeToHTTP(8)=429）。
func TestRateLimitUnified(t *testing.T) {
	m := RateLimit(&denyLimiter{}, false)

	_, err := m(func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil })(context.Background(), nil)
	werr, ok := berrors.FromError(err)
	if !ok || werr.Code != berrors.CodeResourceExhausted || werr.Reason != ReasonRateLimited {
		t.Fatalf("err = %v, want ResourceExhausted/RATE_LIMITED", err)
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v1/x", GinHandler(m), func(c *gin.Context) { c.Status(http.StatusOK) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("gin code = %d, want 429", w.Code)
	}
}

// --- Validator / Defaulter ---

// defaultableReq 实现 RequestDefaulter：name 空则填充 "filled"。
type defaultableReq struct{ name string }

func (r *defaultableReq) Default() {
	if r.name == "" {
		r.name = "filled"
	}
}

// TestValidatorAndDefaulter 链序：Defaulter 内层先填充，Validator 外层后校验
// ——填充后校验通过，证明两件在同一统一链上按声明序工作。
func TestValidatorAndDefaulter(t *testing.T) {
	r := &defaultableReq{}

	// Defaulter 必须最外层（先执行填充），Validator 在其内层。
	chain := Chain(
		Defaulter(),
		Validator(func(_ context.Context, rq any) error {
			rr := rq.(*defaultableReq)
			if rr.name == "" {
				return errors.New("name required")
			}
			return nil
		}),
	)
	if _, err := chain(func(context.Context, interface{}) (interface{}, error) {
		return nil, nil
	})(context.Background(), r); err != nil {
		t.Fatalf("defaulter should fill name before validator: %v", err)
	}
	if r.name != "filled" {
		t.Errorf("name = %q, want filled", r.name)
	}
}

// TestValidatorBlocks 校验失败阻断：validator 返回错误时 handler 不执行。
func TestValidatorBlocks(t *testing.T) {
	called := false
	_, err := Validator(func(_ context.Context, _ any) error { return errors.New("bad") })(
		func(context.Context, interface{}) (interface{}, error) {
			called = true
			return nil, nil
		})(context.Background(), nil)
	if err == nil || called {
		t.Fatalf("validation failure must block handler: err=%v called=%v", err, called)
	}
}

// --- Audit ---

// TestAuditUnifiedResultDerivation 错误直推 Result（统一链持错误本身，无需
// gin 状态推导或 grpc status 二次反解）：PermissionDenied→deny、普通
// error→error、成功→allow；object 默认 = operation。
func TestAuditUnifiedResultDerivation(t *testing.T) {
	aud := &recordingAuditor{}
	m := Audit(AuditWithAuditor(aud))

	run := func(fn Handler) {
		ctx := authn.ContextWithAuthClaims(transporterWithBearer(context.Background(), "good"), &authn.AuthClaims{Subject: "usr-good", TenantID: "t1"})
		_, _ = m(fn)(ctx, nil)
	}

	run(func(ctx context.Context, _ interface{}) (interface{}, error) { return nil, nil })
	run(func(ctx context.Context, _ interface{}) (interface{}, error) {
		return nil, berrors.PermissionDenied("ACCESS_DENIED")
	})
	run(func(ctx context.Context, _ interface{}) (interface{}, error) {
		return nil, errors.New("boom")
	})

	allow, deny, errCount := 0, 0, 0
	for _, ev := range aud.events {
		if ev.Object != "/pkg.Svc/M" {
			t.Errorf("object = %q, want operation", ev.Object)
		}
		if ev.Subject != "usr-good" {
			t.Errorf("subject = %q", ev.Subject)
		}
		switch ev.Result {
		case audit.ResultAllow:
			allow++
		case audit.ResultDeny:
			deny++
		case audit.ResultError:
			errCount++
		}
	}
	if allow != 1 || deny != 1 || errCount != 1 {
		t.Fatalf("allow=%d deny=%d error=%d, want 1/1/1", allow, deny, errCount)
	}
}
