//go:build !grpcgw

// 消费者端到端：段驱动装配的 gateway 把后端地址写成 discovery:///<服务名>，
// 由**同一次装配**注册进内存注册中心的 gRPC 端点解析出地址——业务代码一行不改，
// 只改配置。这条链把 §2 的「会注册、不会解析」合上：
//
//	appkit.register → inmemory 注册（端点带 grpc:// scheme）
//	  → gatewaycontract.AppProvider 的 scope 取到同一个注册中心
//	  → resolver 剥 scheme、丢弃同实例的 http 面
//	  → REST 请求经转码面打到 gRPC 面
//
//	cd _example/bald && go test ./internal/bootstrap/ -run TestGatewayBackendViaDiscovery -v
package bootstrap

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	gatewaycontract "github.com/kalandramo/bald/transport/gateway/contract"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry/inmemory"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

// registerGreet 把 GreetService 的转码路由注册进 mux（与 grpcgw 变体同源，
// 这里独立声明以便默认构建也编译——测试要跑的是「配置切到发现」这一步）。
func registerGreet(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	mux := runtime.NewServeMux()
	if err := baldv1.RegisterGreetServiceHandler(ctx, mux, conn); err != nil {
		return nil, err
	}
	return mux, nil
}

func TestGatewayBackendViaDiscovery(t *testing.T) {
	t.Chdir("../..")

	oldFlags := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("e2e", pflag.ContinueOnError)
	defer func() { pflag.CommandLine = oldFlags }()

	grpcAddr := freeAddr(t)
	gwAddr := freeAddr(t)
	t.Setenv("BALD_DEMO_SERVER_GRPC_ADDR", grpcAddr)
	// 业务问候语钉在 env 层，理由同 startRoleWithConfig：本条断的是「发现解析
	// 出后端并把 REST 打到 gRPC 面」，不该被 -tags nacos 带进来的配置中心内容改写。
	t.Setenv("BALD_DEMO_BIZ_GREET_GREETING", "hello")

	// 网关段走临时契约文件：§7.7 起 env 也能表达多词键（backend_grpc_addr 在
	// 合树前折回规范键），但这里要同时改监听地址和后端（换成服务名），一份补丁
	// 文件比两个 env 更贴近网关配置本来的书写面。
	// 唯一的改动点：后端不写 host:port，写服务名。
	cfg := roleConfigWithGateway(t, configFileForRole(RoleAPI), gwAddr, "discovery:///bald-demo")

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	// 注册中心显式钉成内存实例：本条断的是「同一次装配里注册进去的端点被网关解析
	// 回来」这条接线，不是后端语义。不钉它就跟着构建形态漂——-tags nacos 下同一次
	// 装配接的是真 nacos，服务名口径变成 <名>.grpc（见
	// TestNacosDiscoveryNameCarriesSchemeSuffix），discovery:///bald-demo 就一个实例
	// 也拿不到。后端本身由 discovery_registry_e2e_test.go 按契约段那三条判。
	opts := append(Options(RoleAPI),
		appkit.ConfigFile(cfg),
		appkit.Registrar(inmemory.New()),
		gatewaycontract.AppProvider(registerGreet),
	)
	go func() { errCh <- appkit.Serve(ctx, opts...) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve: %v", err)
			}
		case <-time.After(10 * time.Second):
			t.Error("app did not stop within 10s")
		}
	})

	if !waitForReady(t, gwAddr, 10*time.Second) {
		var runErr error
		select {
		case runErr = <-errCh:
		default:
		}
		t.Fatalf("gateway face not ready at %s; Serve err = %v", gwAddr, runErr)
	}

	// 转码面先于注册完成 Start 是正常时序（resolver 解析到 0 实例时保持订阅），
	// 首跳可能还在 TransientFailure——按退避重试到有响应为止。
	var status int
	var body string
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		status, body = postGreet(t, gwAddr, `{"name":"discovery"}`)
		if status == http.StatusOK {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != http.StatusOK || !strings.Contains(body, "hello, discovery") {
		t.Fatalf("REST via discovery = %d %s, want 200 hello, discovery", status, body)
	}
}

func postGreet(t *testing.T, addr, payload string) (int, string) {
	t.Helper()
	resp, err := http.Post("http://"+addr+"/v1/greet", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatalf("POST /v1/greet: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

// roleConfigWithGateway 把角色的契约文件复制进临时目录，并在 server 段下插入
// gateway 子段（addr + backend_grpc_addr）。逐行找顶层 `server:` 后插入，缩进
// 与该文件的既有风格（4 空格）一致；找不到即失败——不能静默产出一份没有该段的
// 配置，那会让测试测的是直连。
func roleConfigWithGateway(t *testing.T, src, gwAddr, backend string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	lines := strings.Split(string(raw), "\n")
	block := []string{
		"    gateway:",
		`        addr: "` + gwAddr + `"`,
		`        backend_grpc_addr: "` + backend + `"`,
	}
	for i, l := range lines {
		if l != "server:" {
			continue
		}
		out := append(append([]string{}, lines[:i+1]...), block...)
		out = append(out, lines[i+1:]...)
		dst := filepath.Join(t.TempDir(), filepath.Base(src))
		if err := os.WriteFile(dst, []byte(strings.Join(out, "\n")), 0o600); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
		return dst
	}
	t.Fatalf("config %s has no top-level server: section", src)
	return ""
}
