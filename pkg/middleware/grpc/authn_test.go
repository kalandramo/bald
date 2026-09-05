package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald-crud/viewer"
	"github.com/kalandramo/bald/pkg/authn"
)

type stubAuthn struct {
	claims *authn.AuthClaims
	err    error
}

func (s *stubAuthn) Authenticate(_ context.Context) (*authn.AuthClaims, error) {
	return s.claims, s.err
}

func (s *stubAuthn) AuthenticateToken(_ string) (*authn.AuthClaims, error) {
	return s.claims, s.err
}

func TestAuthnInterceptor_InjectsViewer(t *testing.T) {
	// 认证成功后内置注入 viewer.Context：scopes/roles 流转，业务零配置。
	ic := AuthnInterceptor(&stubAuthn{claims: &authn.AuthClaims{
		Subject:  "7",
		TenantID: "1001",
		Scopes:   []string{"read:user"},
		Roles:    []string{"admin"},
	}})
	md := metadata.Pairs("authorization", "Bearer test-token")
	ctx := metadata.NewIncomingContext(context.Background(), md)

	var got viewer.Context
	_, err := ic(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/svc/Get"},
		func(hctx context.Context, _ any) (any, error) {
			got, _ = viewer.FromContext(hctx)
			return nil, nil
		})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got == nil {
		t.Fatal("viewer.Context not injected after authentication")
	}
	if got.TenantID() != 1001 || got.UserID() != 7 {
		t.Fatalf("viewer identity = uid(%d)/tid(%d), want 7/1001", got.UserID(), got.TenantID())
	}
	if !got.HasPermission("read", "user") {
		t.Fatal("scopes should map to permissions (read:user)")
	}
	if got.Roles()[0] != "admin" {
		t.Fatalf("roles = %v, want [admin]", got.Roles())
	}
}
