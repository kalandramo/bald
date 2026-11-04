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
	return startRoleWithConfig(t, role, "")
}

// startRoleWithConfig 与 startRole 同构，额外接受一份契约文件覆盖
// （appkit.ConfigFile 追加在 Options(role) 之后：同名单值字段末者胜）；
// cfg 为空则用角色自己的 configs/<app>-<role>.yaml。
func startRoleWithConfig(t *testing.T, role, cfg string) (grpcAddr, httpAddr string, stop func()) {
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
	// 业务值同样钉在 env 层：-tags nacos 构建带配置中心源层，那份 dataID 里的
	// biz.greet.greeting 会真实进到 Settings()——端到端断的是绑定/路由/限流，
	// 不是「本机正好连着的那个 nacos 里写了什么」。env 在声明式源之上（优先级
	// flag > env > 本地文件 > 源层 > 远程桥），所以这一钉对两种构建形态都成立；
	// 无 tag 时它等于域内默认值，断言文案不变。
	t.Setenv("BALD_DEMO_BIZ_GREET_GREETING", "hello")

	opts := Options(role)
	if cfg != "" {
		opts = append(opts, appkit.ConfigFile(cfg))
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- appkit.Serve(ctx, opts...) }()

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
