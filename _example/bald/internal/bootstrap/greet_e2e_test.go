//go:build grpcgw

// 端到端回归：启动真实应用（gRPC + grpc-gateway 转码面 + 校验链路），
// 验证两层校验机制真实生效（proto 注解 + 手写 Go 规则）。
//
//	cd _example/bald && go test -tags grpcgw ./internal/bootstrap/ -v
package bootstrap

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/berrors/grpcerr"
	"github.com/kalandramo/bald/pkg/appkit"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

func startRole(t *testing.T, role string) (grpcAddr, httpAddr string, stop func()) {
	t.Helper()

	t.Chdir("../..")

	oldFlags := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("e2e", pflag.ContinueOnError)
	defer func() { pflag.CommandLine = oldFlags }()

	// 显式申请空闲端口：网关转码面需要已知的 gRPC 地址；带 127.0.0.1 避免
	// 客户端解析到 IPv6。
	grpcAddr = freeAddr(t)
	t.Setenv("BALD_DEMO_SERVER_GRPC_ADDR", grpcAddr)
	if role == RoleAPI {
		httpAddr = freeAddr(t)
		t.Setenv("BALD_DEMO_SERVER_HTTP_ADDR", httpAddr)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- appkit.Serve(ctx, Options(role)...) }()

	stop = func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(10 * time.Second):
			t.Error("app did not stop within timeout")
		}
	}

	if !waitForReady(t, grpcAddr, 5*time.Second) {
		var runErr error
		select {
		case runErr = <-errCh:
		default:
		}
		cancel()
		t.Fatalf("gRPC server not ready at %s; app.Run err = %v", grpcAddr, runErr)
	}
	if httpAddr != "" && !waitForReady(t, httpAddr, 5*time.Second) {
		cancel()
		t.Fatalf("REST face (gateway transcoding) not ready at %s", httpAddr)
	}
	return grpcAddr, httpAddr, stop
}

func startApp(t *testing.T) (grpcAddr, httpAddr string, stop func()) {
	t.Helper()
	return startRole(t, RoleAPI)
}

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen 127.0.0.1:0: %v", err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func waitForReady(t *testing.T, addr string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func newClient(t *testing.T, target string) baldv1.GreetServiceClient {
	t.Helper()
	target = strings.TrimPrefix(target, "grpc://")
	target = strings.TrimPrefix(target, "http://")

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return baldv1.NewGreetServiceClient(conn)
}

// TestWorkerRoleServesGRPCOnly worker 只装配 gRPC 面（契约无 server.http，也不装域）。
func TestWorkerRoleServesGRPCOnly(t *testing.T) {
	target, httpAddr, stop := startRole(t, RoleWorker)
	defer stop()
	if httpAddr != "" {
		t.Fatalf("worker role must not open an HTTP face, got %q", httpAddr)
	}

	conn, err := grpc.NewClient(strings.TrimPrefix(target, "grpc://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	defer func() { _ = conn.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := grpc_health_v1.NewHealthClient(conn).Check(ctx, &grpc_health_v1.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("gRPC health check: %v", err)
	}
	if resp.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
		t.Errorf("gRPC health = %v, want SERVING", resp.GetStatus())
	}
}

// TestUnknownRoleFailsToAssemble 未知角色必须是启动期错误（Options 返回 nil）。
func TestUnknownRoleFailsToAssemble(t *testing.T) {
	if opts := Options("nosuchrole"); opts != nil {
		t.Fatalf("Options(unknown role) = %d options, want nil", len(opts))
	}
}

func TestGreet_ValidRequest(t *testing.T) {
	target, _, stop := startApp(t)
	defer stop()

	cli := newClient(t, target)
	resp, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: "world"})
	if err != nil {
		t.Fatalf("Greet(valid) unexpected error: %v", err)
	}
	if !strings.Contains(resp.GetGreet(), "world") {
		t.Errorf("greet = %q, want containing %q", resp.GetGreet(), "world")
	}
}

// TestGreet_ProtoAnnotationRules 验证第 1 层：buf.validate 注解生效。
func TestGreet_ProtoAnnotationRules(t *testing.T) {
	target, _, stop := startApp(t)
	defer stop()
	cli := newClient(t, target)

	cases := []struct {
		name    string
		reqName string
		wantIn  string
	}{
		{"空值违反 min_len=1", "", "at least 1 characters"},
		{"超长违反 max_len=32", strings.Repeat("a", 33), "at most 32 characters"},
		{"非法字符违反 pattern", "bad name!", "does not match regex pattern"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: tc.reqName})
			if err == nil {
				t.Fatalf("Greet(%q) succeeded, want validation error", tc.reqName)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("status code = %v, want InvalidArgument", got)
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not contain %q — 注解规则可能未生效", err.Error(), tc.wantIn)
			}
		})
	}
}

// TestGreet_CustomValidator 验证第 2 层：手写 Go 校验器生效。
func TestGreet_CustomValidator(t *testing.T) {
	target, _, stop := startApp(t)
	defer stop()

	cli := newClient(t, target)
	_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: "root"})
	if err == nil {
		t.Fatal(`Greet("root") succeeded, want RESERVED_NAME error`)
	}

	restored := grpcerr.FromStatus(status.Convert(err))
	berr, ok := berrors.FromError(restored)
	if !ok {
		t.Fatalf("FromStatus result is not *berrors.Error: %T", restored)
	}
	if berr.Reason != "RESERVED_NAME" {
		t.Errorf("reason = %q, want RESERVED_NAME", berr.Reason)
	}
	if !strings.Contains(err.Error(), "为系统保留字") {
		t.Errorf("error %q does not contain the message text", err.Error())
	}
}

// TestGreet_REST_SharesSameRules 同一份 proto 注解在 REST 与 gRPC 下都生效。
func TestGreet_REST_SharesSameRules(t *testing.T) {
	_, restAddr, stop := startApp(t)
	defer stop()

	base := "http://" + restAddr

	resp, err := http.Post(base+"/v1/greet", "application/json", strings.NewReader(`{"name":"world"}`))
	if err != nil {
		t.Fatalf("POST /v1/greet: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "world") {
		t.Errorf("body = %s, want containing world", body)
	}

	resp2, err := http.Post(base+"/v1/greet", "application/json", strings.NewReader(`{"name":""}`))
	if err != nil {
		t.Fatalf("POST /v1/greet (invalid): %v", err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("invalid name should be rejected, got 200; body = %s", body2)
	}
	if !strings.Contains(string(body2), "at least 1 characters") {
		t.Errorf("body = %s, want containing the annotation rule message", body2)
	}
}

// TestGreet_ErrorMessageIsStructured 多条违规应聚合为一条消息。
func TestGreet_ErrorMessageIsStructured(t *testing.T) {
	target, _, stop := startApp(t)
	defer stop()

	cli := newClient(t, target)
	bad := strings.Repeat("a", 40) + "!"
	_, err := cli.Greet(context.Background(), &baldv1.GreetRequest{Name: bad})
	if err == nil {
		t.Fatal("expected validation error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "at most 32 characters") ||
		!strings.Contains(msg, "does not match regex pattern") {
		t.Errorf("error %q should aggregate all violations (max_len + pattern)", msg)
	}
}

// TestHealthProbesE2E /healthz 恒 200、/readyz 依赖全 Up 时 200；
// 探针包装不得吞掉业务路由。
func TestHealthProbesE2E(t *testing.T) {
	_, restAddr, stop := startApp(t)
	defer stop()

	base := "http://" + restAddr
	client := &http.Client{Timeout: 2 * time.Second}
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := client.Get(base + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d, want 200; body = %s", path, resp.StatusCode, body)
		}
	}

	resp, err := http.Post(base+"/v1/greet", "application/json", strings.NewReader(`{"name":"probe"}`))
	if err != nil {
		t.Fatalf("POST /v1/greet: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("POST /v1/greet = %d, want 200 (probe wrapper must not shadow business routes)", resp.StatusCode)
	}
}
