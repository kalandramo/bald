package grpc

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/authz"

	"github.com/kalandramo/bald/pkg/authn"
	grpcmw "github.com/kalandramo/bald/pkg/middleware/grpc"
	"github.com/kalandramo/bald/pkg/server"

	adminv1 "github.com/kalandramo/bald/examples/go-bald-admin/gen/secretv1"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
)

func listenLocal() (net.Listener, error) { return net.Listen("tcp", ":0") }

func statusCode(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		return "OK"
	}
	return status.Code(err).String()
}

// startServer 构造受 Authn+Authz 保护的真实 gRPC server（:0 动态端口，标准 proto codec）。
func startServer(t *testing.T) (*server.GRPCServer, string) {
	t.Helper()
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	authnI := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return grpcmw.AuthnInterceptor(bootstrappkg.Authenticator)(ctx, req, info, handler)
	}
	authzI := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return grpcmw.AuthzInterceptor(bootstrappkg.Authorizer,
			grpcmw.WithObjectResolver(authz.DefaultGRPCObject),
			grpcmw.WithActionResolver(authz.DefaultGRPCAction))(ctx, req, info, handler)
	}
	srv := server.NewGRPCServerWithRegister(
		&confv1.Grpc{Addr: ":0"},
		[]grpc.ServerOption{
			// ErrorInterceptor 必须最外层，收口 authn/authz 返回的 berrors -> gRPC status。
			grpc.ChainUnaryInterceptor(grpcmw.ErrorInterceptor(), authnI, authzI),
		},
		func(s *grpc.Server) { adminv1.RegisterSecretServiceServer(s, NewServer()) },
		nil,
	)
	lis, err := listenLocal()
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	return srv, lis.Addr().String()
}

// dial 建立标准 proto codec 的 gRPC 客户端连接。
func dial(t *testing.T, addr string) *grpc.ClientConn {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return conn
}

func token(t *testing.T, username, userID, role, tenantID string) string {
	t.Helper()
	claims := authn.AuthClaims{Issuer: "go-bald-admin", Subject: userID, TenantID: tenantID, Roles: []string{role}, Name: username}
	tok, err := bootstrappkg.Signer.IssueToken(claims, 2*time.Hour)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	return tok
}

func callGet(t *testing.T, conn *grpc.ClientConn, tok string) error {
	t.Helper()
	ctx := context.Background()
	if tok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}
	resp := new(adminv1.GetSecretResponse)
	return conn.Invoke(ctx, "/go.bald.admin.v1.SecretService/GetSecret", &adminv1.GetSecretRequest{Id: "s-db-pwd"}, resp)
}

func callDelete(t *testing.T, conn *grpc.ClientConn, tok string) error {
	t.Helper()
	ctx := context.Background()
	if tok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}
	resp := new(adminv1.DeleteSecretResponse)
	return conn.Invoke(ctx, "/go.bald.admin.v1.SecretService/DeleteSecret", &adminv1.DeleteSecretRequest{Id: "s-db-pwd"}, resp)
}

// TestGRPCTransport_NoToken 真实传输：无 token 调 GetSecret 应 Unauthenticated。
func TestGRPCTransport_NoToken(t *testing.T) {
	srv, addr := startServer(t)
	defer srv.Stop(context.Background())
	conn := dial(t, addr)
	defer conn.Close()

	if err := callGet(t, conn, ""); statusCode(t, err) != "Unauthenticated" {
		t.Fatalf("want Unauthenticated, got %v", err)
	}
}

// TestGRPCTransport_Admin_OK 真实传输：admin 调 Get/Delete 均成功。
func TestGRPCTransport_Admin_OK(t *testing.T) {
	srv, addr := startServer(t)
	defer srv.Stop(context.Background())
	conn := dial(t, addr)
	defer conn.Close()

	tok := token(t, "admin", "u-admin", "admin", "t-default")
	if err := callGet(t, conn, tok); err != nil {
		t.Fatalf("admin GetSecret: want ok, got %v", err)
	}
	if err := callDelete(t, conn, tok); err != nil {
		t.Fatalf("admin DeleteSecret: want ok, got %v", err)
	}
}

// TestGRPCTransport_Viewer_DeleteForbidden 真实传输：viewer 删机密应 PermissionDenied。
func TestGRPCTransport_Viewer_DeleteForbidden(t *testing.T) {
	srv, addr := startServer(t)
	defer srv.Stop(context.Background())
	conn := dial(t, addr)
	defer conn.Close()

	tok := token(t, "alice", "u-alice", "viewer", "t-default")
	if err := callGet(t, conn, tok); err != nil {
		t.Fatalf("viewer GetSecret: want ok, got %v", err)
	}
	if err := callDelete(t, conn, tok); statusCode(t, err) != "PermissionDenied" {
		t.Fatalf("viewer DeleteSecret: want PermissionDenied, got %v", err)
	}
}

func callListUsers(t *testing.T, conn *grpc.ClientConn, tok string) (*adminv1.ListUsersResponse, error) {
	t.Helper()
	ctx := context.Background()
	if tok != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+tok)
	}
	resp := new(adminv1.ListUsersResponse)
	err := conn.Invoke(ctx, "/go.bald.admin.v1.SecretService/ListUsers", &adminv1.ListUsersRequest{}, resp)
	return resp, err
}

// TestGRPCTransport_MultiTenant_Isolation 真实传输：多租户隔离（P8 自动注入 tenant_id）。
//
// t-default 的 admin 只能看到本租户用户（u-admin/u-alice）；t-other 的 bob 只能看到
// 自己（u-bob）。未见 cross-tenant 泄漏。store.Where.T(ctx) 由 pkg/store 在翻译查询时
// 自动追加 tenant_id 等值条件，业务 handler 无需手写。
func TestGRPCTransport_MultiTenant_Isolation(t *testing.T) {
	srv, addr := startServer(t)
	defer srv.Stop(context.Background())
	conn := dial(t, addr)
	defer conn.Close()

	adminTok := token(t, "admin", "u-admin", "admin", "t-default")
	adminResp, err := callListUsers(t, conn, adminTok)
	if err != nil {
		t.Fatalf("admin ListUsers: %v", err)
	}
	if !contains(adminResp.GetUsers(), "u-admin") || !contains(adminResp.GetUsers(), "u-alice") {
		t.Fatalf("t-default admin should see u-admin/u-alice, got %v", adminResp.GetUsers())
	}
	if contains(adminResp.GetUsers(), "u-bob") {
		t.Fatalf("t-default admin must NOT see t-other's u-bob, got %v", adminResp.GetUsers())
	}

	bobTok := token(t, "bob", "u-bob", "viewer", "t-other")
	bobResp, err := callListUsers(t, conn, bobTok)
	if err != nil {
		t.Fatalf("bob ListUsers: %v", err)
	}
	if !contains(bobResp.GetUsers(), "u-bob") {
		t.Fatalf("t-other bob should see itself, got %v", bobResp.GetUsers())
	}
	if contains(bobResp.GetUsers(), "u-admin") || contains(bobResp.GetUsers(), "u-alice") {
		t.Fatalf("t-other bob must NOT see t-default users, got %v", bobResp.GetUsers())
	}
}

// TestRESTGateway_MultiTenant_Isolation M5：grpc-gateway REST 转码，复用同一 gRPC 拦截器
// 链（认证/授权/多租户）。REST GET /v1/users 经转码进入 SecretService.ListUsers，
// 跨租户隔离同样生效。
func TestRESTGateway_MultiTenant_Isolation(t *testing.T) {
	if err := bootstrappkg.InitBridges(context.Background()); err != nil {
		t.Fatalf("InitBridges: %v", err)
	}
	// 后端 gRPC（:0 不可用于转码转发，故用 freeAddr 确定端口）。
	grpcLis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("grpc listen: %v", err)
	}
	grpcAddr := grpcLis.Addr().String()
	authnI := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return grpcmw.AuthnInterceptor(bootstrappkg.Authenticator)(ctx, req, info, handler)
	}
	authzI := func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		return grpcmw.AuthzInterceptor(bootstrappkg.Authorizer,
			grpcmw.WithObjectResolver(authz.DefaultGRPCObject),
			grpcmw.WithActionResolver(authz.DefaultGRPCAction))(ctx, req, info, handler)
	}
	grpcSrv := server.NewGRPCServerWithRegister(
		&confv1.Grpc{Addr: grpcAddr},
		[]grpc.ServerOption{grpc.ChainUnaryInterceptor(grpcmw.ErrorInterceptor(), authnI, authzI)},
		func(s *grpc.Server) { adminv1.RegisterSecretServiceServer(s, NewServer()) },
		nil,
	)
	go func() { _ = grpcSrv.Serve(grpcLis) }()
	defer grpcSrv.Stop(context.Background())

	// 网关：指向真实 grpc 地址，转发 REST 到 gRPC。监听地址用 :0 由框架分配，
	// 真实端口经 Endpoint() 取（不可自行预监听，否则与 HTTPServer 内部监听冲突）。
	gwSrv, gwErr := server.NewGatewayServer(
		&confv1.Http{Addr: ":0"},
		&confv1.Grpc{Addr: grpcAddr},
		func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
			mux := runtime.NewServeMux()
			if err := adminv1.RegisterSecretServiceHandler(ctx, mux, conn); err != nil {
				return nil, err
			}
			return mux, nil
		},
		nil,
	)
	if gwErr != nil {
		t.Fatalf("new gateway server: %v", gwErr)
	}
	go func() { _ = gwSrv.Start(context.Background()) }()
	defer gwSrv.Stop(context.Background())
	time.Sleep(150 * time.Millisecond) // 等网关建立到后端连接并监听
	gwAddr := gwSrv.Endpoint()

	// callRESTUsers 经 grpc-gateway 转发 REST → gRPC，返回状态码与解析后的用户列表。
	callRESTUsers := func(tok string) (int, *adminv1.ListUsersResponse) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, gwAddr+"/v1/users", nil)
		if tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("rest call: %v", err)
		}
		defer resp.Body.Close()
		out := new(adminv1.ListUsersResponse)
		if resp.StatusCode == http.StatusOK {
			if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
		}
		return resp.StatusCode, out
	}

	adminTok := token(t, "admin", "u-admin", "admin", "t-default")
	code, admin := callRESTUsers(adminTok)
	if code != http.StatusOK {
		t.Fatalf("REST admin status=%d", code)
	}
	if !contains(admin.GetUsers(), "u-admin") || !contains(admin.GetUsers(), "u-alice") || contains(admin.GetUsers(), "u-bob") {
		t.Fatalf("REST admin tenant leak: %v", admin.GetUsers())
	}

	bobTok := token(t, "bob", "u-bob", "viewer", "t-other")
	_, bob := callRESTUsers(bobTok)
	if !contains(bob.GetUsers(), "u-bob") || contains(bob.GetUsers(), "u-admin") || contains(bob.GetUsers(), "u-alice") {
		t.Fatalf("REST bob tenant leak: %v", bob.GetUsers())
	}

	// 无 token 经 REST 应 401（认证拦截器对转码请求同样生效）。
	if code, _ := callRESTUsers(""); code != http.StatusUnauthorized {
		t.Fatalf("REST no-token want 401, got %d", code)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
