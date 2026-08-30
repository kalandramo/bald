package apiserver

import (
	"context"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authz"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// memAuditor 是 e2e 断言用内存 Auditor。
type memAuditor struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (m *memAuditor) Record(_ context.Context, e audit.AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *memAuditor) all() []audit.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.AuditEvent, len(m.events))
	copy(out, m.events)
	return out
}

// invokeWithAudit 串联 Authn + Authz + Audit（M7）拦截器，返回 (err, 审计事件)。
func invokeWithAudit(t *testing.T, a *memAuditor, token, fullMethod string) error {
	t.Helper()
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	info := &grpc.UnaryServerInfo{FullMethod: fullMethod}
	handler := func(c context.Context, _ any) (any, error) { return "ok", nil }
	authzWrapped := grpcmw.AuthzInterceptor(bootstrappkg.Authorizer,
		grpcmw.WithObjectResolver(authz.DefaultGRPCObject),
		grpcmw.WithActionResolver(authz.DefaultGRPCAction))
	authnWrapped := grpcmw.AuthnInterceptor(bootstrappkg.Authenticator)
	auditWrapped := grpcmw.AuditInterceptor(nil, grpcmw.AuditWithAuditor(a),
		grpcmw.AuditWithObjectResolver(authz.DefaultGRPCObject),
		grpcmw.AuditWithActionResolver(authz.DefaultGRPCAction))
	ctx := grpcAuthCtx(token)
	// 链路顺序：Authn(外层) → Audit → Authz → handler(内层)。
	//  - Audit 在 Authn 内侧：能读取认证注入的 subject/tenant；
	//  - Audit 在 Authz 外侧：能捕获授权允许/拒绝的最终 result（旁路不阻断）。
	_, err := authnWrapped(ctx, nil, info, func(c context.Context, r any) (any, error) {
		return auditWrapped(c, r, info, func(c context.Context, r any) (any, error) {
			return authzWrapped(c, r, info, handler)
		})
	})
	return err
}

// TestGRPCAudit_Allow 授权通过的 gRPC 调用应产生一条 allow 审计事件，含归一化三元组。
func TestGRPCAudit_Allow(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	a := &memAuditor{}
	tok := issueToken(t, "admin", "u-admin", "admin")
	if err := invokeWithAudit(t, a, tok, "/go.bald.admin.v1.SecretService/GetSecret"); err != nil {
		t.Fatalf("want ok, got %v", err)
	}
	evs := a.all()
	if len(evs) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Subject != "u-admin" || ev.TenantID != "t-default" {
		t.Errorf("subject/tenant mismatch: %+v", ev)
	}
	if ev.Object != "secret" || ev.Action != "get" {
		t.Errorf("normalized (object,action) mismatch: (%q,%q)", ev.Object, ev.Action)
	}
	if ev.Result != audit.ResultAllow {
		t.Errorf("result should be allow, got %q", ev.Result)
	}
}

// TestGRPCAudit_Deny 授权拒绝的 gRPC 调用应产生一条 deny 审计事件，旁路不阻断。
func TestGRPCAudit_Deny(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	a := &memAuditor{}
	tok := issueToken(t, "alice", "u-alice", "viewer")
	if err := invokeWithAudit(t, a, tok, "/go.bald.admin.v1.SecretService/DeleteSecret"); err == nil {
		t.Fatal("want PermissionDenied, got nil")
	}
	evs := a.all()
	if len(evs) != 1 {
		t.Fatalf("want 1 audit event, got %d", len(evs))
	}
	if evs[0].Result != audit.ResultDeny {
		t.Errorf("result should be deny, got %q", evs[0].Result)
	}
	if evs[0].Action != "delete" {
		t.Errorf("action should be delete, got %q", evs[0].Action)
	}
}
