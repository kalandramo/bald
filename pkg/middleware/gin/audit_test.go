package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/audit"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
)

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

func TestAuditMiddleware_Allow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &auditMem{}
	mw := AuditMiddleware(nil, AuditWithAuditor(a),
		AuditWithObjectResolver(authz.DefaultHTTPObject),
		AuditWithActionResolver(authz.DefaultHTTPAction),
	)
	r := gin.New()
	r.Use(mw)
	r.GET("/v1/secret/:id", func(c *gin.Context) {
		c.Request = c.Request.WithContext(
			authn.ContextWithAuthClaims(c.Request.Context(),
				&authn.AuthClaims{Subject: "u-admin", TenantID: "t-a"}))
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/secret/s1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status should pass through, got %d", w.Code)
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
}

func TestAuditMiddleware_Deny(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a := &auditMem{}
	mw := AuditMiddleware(nil, AuditWithAuditor(a),
		AuditWithObjectResolver(authz.DefaultHTTPObject),
		AuditWithActionResolver(authz.DefaultHTTPAction),
	)
	r := gin.New()
	r.Use(mw)
	r.DELETE("/v1/secret/:id", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusForbidden)
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v1/secret/s1", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status should pass through, got %d", w.Code)
	}
	evs := a.all()
	if len(evs) != 1 || evs[0].Result != audit.ResultDeny {
		t.Fatalf("want 1 deny event, got %+v", evs)
	}
	if evs[0].Action != "delete" {
		t.Errorf("action should be delete, got %q", evs[0].Action)
	}
}

func TestAuditMiddleware_PanickingAuditorSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	mw := AuditMiddleware(nil, AuditWithAuditor(panicAuditor{}))
	r := gin.New()
	r.Use(mw)
	r.GET("/v1/ping", func(c *gin.Context) { c.Status(http.StatusOK) })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/ping", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("panicking auditor must not block handler, got %d", w.Code)
	}
}

type panicAuditor struct{}

func (panicAuditor) Record(context.Context, audit.AuditEvent) { panic("boom") }
