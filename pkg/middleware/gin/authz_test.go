package gin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
)

func TestAuthzMiddleware_Default(t *testing.T) {
	var gotObj, gotAct, gotSub string
	authorizer := authz.Func(func(_ context.Context, subject, object, action string) (bool, error) {
		gotSub, gotObj, gotAct = subject, object, action
		return true, nil
	})
	r := gin.New()
	r.Use(AuthzMiddleware(authorizer))
	r.GET("/v1/secret/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodGet, "/v1/secret/123", nil)
	req = req.WithContext(authn.ContextWithAuthClaims(req.Context(), &authn.AuthClaims{Subject: "u-admin"}))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if gotSub != "u-admin" || gotObj != "/v1/secret/123" || gotAct != "GET" {
		t.Fatalf("default: sub=%q obj=%q act=%q", gotSub, gotObj, gotAct)
	}
}

func TestAuthzMiddleware_ResolverNormalization(t *testing.T) {
	var gotObj, gotAct string
	authorizer := authz.Func(func(_ context.Context, _, object, action string) (bool, error) {
		gotObj, gotAct = object, action
		return true, nil
	})
	r := gin.New()
	r.Use(AuthzMiddleware(authorizer,
		WithObjectResolver(authz.DefaultHTTPObject),
		WithActionResolver(authz.DefaultHTTPAction),
	))
	r.GET("/v1/secret/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.DELETE("/v1/secret/:id", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodDelete, "/v1/secret/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200 got %d", w.Code)
	}
	if gotObj != "secret" || gotAct != "delete" {
		t.Fatalf("normalized: obj=%q act=%q, want secret/delete", gotObj, gotAct)
	}
}

func TestAuthzMiddleware_NilAuthorizerPassthrough(t *testing.T) {
	r := gin.New()
	r.Use(AuthzMiddleware(nil))
	r.GET("/v1/x", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("nil authorizer should pass through, got %d", w.Code)
	}
}

func TestAuthzMiddleware_Denied(t *testing.T) {
	r := gin.New()
	r.Use(AuthzMiddleware(authz.DenyAll(),
		WithObjectResolver(authz.DefaultHTTPObject),
		WithActionResolver(authz.DefaultHTTPAction),
	))
	r.GET("/v1/secret/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
	req := httptest.NewRequest(http.MethodGet, "/v1/secret/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 got %d", w.Code)
	}
}
