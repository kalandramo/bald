package grpc

import (
	"context"
	"errors"
	"sync"
	"testing"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/contextx"
)

// auditMem 是测试用内存 Auditor。
type auditMem struct {
	mu     sync.Mutex
	events []audit.AuditEvent
}

func (m *auditMem) Record(_ context.Context, e audit.AuditEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
}

func (m *auditMem) all() []audit.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]audit.AuditEvent, len(m.events))
	copy(out, m.events)
	return out
}

// panicAuditor 用于验证旁路降级。
type panicAuditor struct{}

func (panicAuditor) Record(context.Context, audit.AuditEvent) { panic("boom") }

func withClaims(ctx context.Context, subject, tenant string) context.Context {
	return authn.ContextWithAuthClaims(ctx, &authn.AuthClaims{Subject: subject, TenantID: tenant})
}

func TestAuditInterceptor_Allow(t *testing.T) {
	a := &auditMem{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	inter := AuditInterceptor(nil, AuditWithAuditor(a),
		AuditWithObjectResolver(authz.DefaultGRPCObject),
		AuditWithActionResolver(authz.DefaultGRPCAction),
	)
	ctx := withClaims(context.Background(), "u-admin", "t-a")
	resp, err := inter(ctx, nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	if err != nil || resp != "ok" {
		t.Fatalf("handler should pass through, got resp=%v err=%v", resp, err)
	}
	evs := a.all()
	if len(evs) != 1 {
		t.Fatalf("want 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev.Subject != "u-admin" || ev.TenantID != "t-a" {
		t.Errorf("subject/tenant mismatch: %+v", ev)
	}
	if ev.Object != "secret" || ev.Action != "get" {
		t.Errorf("normalized object/action mismatch: object=%q action=%q", ev.Object, ev.Action)
	}
	if ev.Result != audit.ResultAllow {
		t.Errorf("result should be allow, got %q", ev.Result)
	}
	if ev.Meta["request_id"] != contextx.RequestIDFromContext(ctx) {
		t.Errorf("meta request_id missing")
	}
}

func TestAuditInterceptor_Deny(t *testing.T) {
	a := &auditMem{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/DeleteSecret"}
	inter := AuditInterceptor(nil, AuditWithAuditor(a),
		AuditWithObjectResolver(authz.DefaultGRPCObject),
		AuditWithActionResolver(authz.DefaultGRPCAction),
	)
	denied := berrors.PermissionDenied("ACCESS_DENIED")
	_, err := inter(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, denied
	})
	if !errors.Is(err, denied) {
		t.Fatalf("error should pass through, got %v", err)
	}
	evs := a.all()
	if len(evs) != 1 || evs[0].Result != audit.ResultDeny {
		t.Fatalf("want 1 deny event, got %+v", evs)
	}
	if evs[0].Action != "delete" {
		t.Errorf("action should be delete, got %q", evs[0].Action)
	}
}

func TestAuditInterceptor_Error(t *testing.T) {
	a := &auditMem{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/ListSecrets"}
	inter := AuditInterceptor(nil, AuditWithAuditor(a),
		AuditWithObjectResolver(authz.DefaultGRPCObject),
		AuditWithActionResolver(authz.DefaultGRPCAction),
	)
	boom := errors.New("boom")
	_, err := inter(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return nil, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("error should pass through, got %v", err)
	}
	evs := a.all()
	if len(evs) != 1 || evs[0].Result != audit.ResultError {
		t.Fatalf("want 1 error event, got %+v", evs)
	}
}

func TestAuditInterceptor_PanickingAuditorSafe(t *testing.T) {
	// Auditor panic 不应影响业务响应。
	bad := &panicAuditor{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	inter := AuditInterceptor(bad,
		AuditWithObjectResolver(authz.DefaultGRPCObject),
		AuditWithActionResolver(authz.DefaultGRPCAction),
	)
	resp, err := inter(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	})
	if err != nil || resp != "ok" {
		t.Fatalf("panicking auditor must not block handler, got resp=%v err=%v", resp, err)
	}
}
