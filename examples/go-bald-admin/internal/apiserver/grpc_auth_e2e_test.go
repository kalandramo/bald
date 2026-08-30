package apiserver

import (
	"context"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/authz"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"

	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// grpcAuthCtx 构造带 Bearer token 的 gRPC incoming context；token 为空则不带。
func grpcAuthCtx(token string) context.Context {
	if token == "" {
		return context.Background()
	}
	return metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		"authorization": "Bearer " + token,
	}))
}

// invoke 以给定 token 调用 fullMethod，串联 Authn + Authz 拦截器。
func invoke(t *testing.T, token, fullMethod string) error {
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
	_, err := authnWrapped(grpcAuthCtx(token), nil, info, func(c context.Context, r any) (any, error) {
		return authzWrapped(c, r, info, handler)
	})
	return err
}

// issueToken 签发一个 gRPC 调用可用的 Bearer token（模拟登录后下发）。
func issueToken(t *testing.T, username, userID, role string) string {
	t.Helper()
	claims := authn.AuthClaims{
		Issuer:   "go-bald-admin",
		Subject:  userID,
		TenantID: "t-default",
		Roles:    []string{role},
		Name:     username,
	}
	tok, err := bootstrappkg.Signer.IssueToken(claims, 2*time.Hour)
	if err != nil {
		t.Fatalf("issue token (RSA signer): %v", err)
	}
	return tok
}

// TestGRPCAuthn_NoToken 无 token 调用受保护 gRPC 方法应 Unauthenticated。
func TestGRPCAuthn_NoToken(t *testing.T) {
	if err := invoke(t, "", "/go.bald.admin.v1.SecretService/GetSecret"); err == nil {
		t.Fatal("want Unauthenticated, got nil")
	}
}

// TestGRPCBadToken 伪造 token 应 Unauthenticated。
func TestGRPCBadToken(t *testing.T) {
	if err := invoke(t, "not-a-valid-token", "/go.bald.admin.v1.SecretService/GetSecret"); err == nil {
		t.Fatal("want Unauthenticated for bad token, got nil")
	}
}

// TestGRPCAuthz_Admin_OK admin 调用 SecretService 应放行。
func TestGRPCAuthz_Admin_OK(t *testing.T) {
	tok := issueToken(t, "admin", "u-admin", "admin")
	if err := invoke(t, tok, "/go.bald.admin.v1.SecretService/GetSecret"); err != nil {
		t.Fatalf("admin gRPC call: want ok, got %v", err)
	}
}

// TestGRPCAuthz_Viewer_DeleteForbidden viewer 调用 DeleteSecret 应 PermissionDenied。
func TestGRPCAuthz_Viewer_DeleteForbidden(t *testing.T) {
	tok := issueToken(t, "alice", "u-alice", "viewer")
	if err := invoke(t, tok, "/go.bald.admin.v1.SecretService/DeleteSecret"); err == nil {
		t.Fatal("viewer gRPC delete: want PermissionDenied, got nil")
	}
}
