// 端到端测试的启动辅助（两种构建形态共用：默认 gin 面与 -tags grpcgw 的
// 转码面）。
package bootstrap

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/kalandramo/bald/pkg/appkit"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

func startRole(t *testing.T, role string) (grpcAddr, httpAddr string, stop func()) {
	t.Helper()

	t.Chdir("../..")

	oldFlags := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("e2e", pflag.ContinueOnError)
	defer func() { pflag.CommandLine = oldFlags }()

	// 显式申请空闲端口：HTTP 面需要已知地址；带 127.0.0.1 避免客户端
	// 解析到 IPv6。
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
		t.Fatalf("HTTP face not ready at %s", httpAddr)
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
