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
	"github.com/kalandramo/bald/pkg/metrics"
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

// metricsMem 是测试用内存 metrics.Recorder。
type metricsMem struct {
	mu      sync.Mutex
	records []metricsCall
}

type metricsCall struct {
	ev        metrics.Event
	transport metrics.Transport
	dur       float64
}

func (m *metricsMem) Record(_ context.Context, ev metrics.Event, tr metrics.Transport, dur float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, metricsCall{ev: ev, transport: tr, dur: dur})
}

func (m *metricsMem) all() []metricsCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]metricsCall, len(m.records))
	copy(out, m.records)
	return out
}

func withClaims(ctx context.Context, subject, tenant string) context.Context {
	return authn.ContextWithAuthClaims(ctx, &authn.AuthClaims{Subject: subject, TenantID: tenant})
}

func TestAuditInterceptor_Allow(t *testing.T) {
	a := &auditMem{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/GetSecret"}
	inter := AuditInterceptor(AuditWithAuditor(a),
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
	inter := AuditInterceptor(AuditWithAuditor(a),
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
	inter := AuditInterceptor(AuditWithAuditor(a),
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
	// D6 修正：panicAuditor 经 AuditWithAuditor 真实注入（此前传给被丢弃的首参，
	// 实际执行全局 nop，本测试对 recover 逻辑零覆盖而"假通过"）。
	inter := AuditInterceptor(
		AuditWithAuditor(bad),
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

// TestAuditInterceptor_MetricsEmitted 验证 M8：审计同源 emit 指标（计数+维度）。
func TestAuditInterceptor_MetricsEmitted(t *testing.T) {
	a := &auditMem{}
	m := &metricsMem{}
	info := &grpc.UnaryServerInfo{FullMethod: "/admin.v1.SecretService/DeleteSecret"}
	inter := AuditInterceptor(AuditWithAuditor(a),
		AuditWithMetrics(m),
		AuditWithObjectResolver(authz.DefaultGRPCObject),
		AuditWithActionResolver(authz.DefaultGRPCAction),
	)
	// 核心单测不涉及 Authz，handler 成功返回 → 审计 allow + 指标 result=allow。
	if _, err := inter(context.Background(), nil, info, func(context.Context, any) (any, error) {
		return "ok", nil
	}); err != nil {
		t.Fatalf("handler should pass through, got %v", err)
	}
	calls := m.all()
	if len(calls) != 1 {
		t.Fatalf("want 1 metric record, got %d", len(calls))
	}
	c := calls[0]
	if c.transport != metrics.TransportGRPC {
		t.Errorf("transport should be grpc, got %q", c.transport)
	}
	if c.ev.Object != "secret" || c.ev.Action != "delete" || c.ev.Result != "allow" {
		t.Errorf("metric dims mismatch: %+v", c.ev)
	}
	if c.dur <= 0 {
		t.Errorf("duration should be positive, got %v", c.dur)
	}
}
