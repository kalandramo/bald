//go:build !grpcgw

// client 段的取用腿：给 api 角色的契约补一条 grpc 出站（目标写服务名），
// 经 appkit.Serve 的真实装配后用 clientcontract.GRPC 按名字取连接，真的调到
// 同一次装配注册进内存注册中心的 gRPC 服务面。地址是随机端口且只以名字登记，
// 调用成功即证明「契约 → Provider → 发现半边 → 连接」这条链是通的。
//
//	cd _example/bald && go test ./internal/bootstrap/ -run TestClientGRPCSegment -v
package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/registry/inmemory"
	clientcontract "github.com/kalandramo/bald/transport/client/contract"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

func TestClientGRPCSegmentReachesSelf(t *testing.T) {
	t.Chdir("../..")

	oldFlags := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("e2e", pflag.ContinueOnError)
	defer func() { pflag.CommandLine = oldFlags }()

	grpcAddr := freeAddr(t)
	t.Setenv("BALD_DEMO_SERVER_GRPC_ADDR", grpcAddr)
	// 业务问候语钉在 env 层，理由同其它端到端用例：本条断的是 client 段的接线，
	// 不该被 -tags nacos 带进来的配置中心内容改写。
	t.Setenv("BALD_DEMO_BIZ_GREET_GREETING", "hello")

	// 注册中心显式钉成内存实例（末者胜），并在启动前先登记本进程的服务名：
	// 出站契约只写名字，解析走的就是这条登记。
	reg := inmemory.New()
	if err := reg.Register(context.Background(), &registry.ServiceInstance{
		ID:        "client-self",
		Name:      "bald-demo",
		Endpoints: []string{"grpc://" + grpcAddr},
	}); err != nil {
		t.Fatalf("register self instance: %v", err)
	}

	cfg := roleConfigWithClient(t, configFileForRole(RoleAPI), "self", "discovery:///bald-demo")

	// 取用探针必须经 Serve 的真实装配链：NewApp+a.Run 不消费契约段，
	// 资源面上永远不会有 client 产物。
	probe := &clientProbe{conns: make(chan *grpc.ClientConn, 1)}
	opts := append(Options(RoleAPI),
		appkit.ConfigFile(cfg),
		appkit.Registrar(reg),
		probe,
	)

	ctx, cancel := context.WithCancel(context.Background())
	var serveErr error
	serveDone := make(chan struct{})
	go func() { serveErr = appkit.Serve(ctx, opts...); close(serveDone) }()
	stop := func() {
		cancel()
		select {
		case <-serveDone:
		case <-time.After(10 * time.Second):
			t.Error("app did not stop within 10s")
		}
	}
	t.Cleanup(stop)

	if !waitForReady(t, grpcAddr, 10*time.Second) {
		stop()
		if serveErr != nil {
			t.Fatalf("gRPC server not ready at %s; Serve err = %v", grpcAddr, serveErr)
		}
		t.Fatalf("gRPC server not ready at %s", grpcAddr)
	}

	var conn *grpc.ClientConn
	select {
	case conn = <-probe.conns:
	case <-time.After(5 * time.Second):
		t.Fatal("client.grpc.self 未落资源面——client 段没有走到 App 产物")
	}
	cli := baldv1.NewGreetServiceClient(conn)

	// 服务面刚起、实例登记与 Watch 推送有先后：拨号是惰性的，解析到空集时
	// 重试若干次，与真实消费方在滚动发布期间的处置相同。
	deadline := time.Now().Add(5 * time.Second)
	for {
		resp, err := cli.Greet(ctx, &baldv1.GreetRequest{Name: "client"})
		if err == nil {
			if !strings.Contains(resp.GetGreet(), "client") {
				t.Fatalf("greet = %q, want containing %q", resp.GetGreet(), "client")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Greet via client.grpc.self: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// clientProbe 是消费面的取用探针：声明 client.grpc 子段（精确路径不与 client
// Provider 的顶层路径冲突），节点优先级排在 client 之后构建，从资源容器按名字
// 取句柄交给测试。生产消费方在请求期取用；测试没有请求期入口，故在装配期取
// 同一句柄——连接本身仍是 Provider 构造的那一条。
type clientProbe struct{ conns chan *grpc.ClientConn }

func (p *clientProbe) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) }

func (p *clientProbe) Segments() []string { return []string{"client.grpc"} }

func (p *clientProbe) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:      "test.client.probe",
		Kind:     "test",
		Segments: []string{"client.grpc"},
		Phase:    appkit.PhaseBootstrap,
		Priority: appkit.PriorityClient + 1,
		Reload:   appkit.ReloadStatic,
	}}, nil
}

func (p *clientProbe) Build(_ context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	conn, ok := clientcontract.GRPC(s.App, "self")
	if !ok {
		return nil, nil, fmt.Errorf("client.grpc.self is not on the resource face")
	}
	p.conns <- conn
	return nil, nil, nil
}

// roleConfigWithClient 复制角色契约并在末尾追加顶层 client 段（一条 grpc 出站）。
// 追加而不是插进 server 段：client 与 server 同级，落点写错会让测试静默测成
// 「没有 client 段」。
func roleConfigWithClient(t *testing.T, src, name, addr string) string {
	t.Helper()
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	block := strings.Join([]string{
		"",
		"client:",
		"    grpc:",
		"        entries:",
		"            - name: " + name,
		`              addr: "` + addr + `"`,
		"              timeout: 3s",
		"",
	}, "\n")
	dst := filepath.Join(t.TempDir(), filepath.Base(src))
	if err := os.WriteFile(dst, []byte(string(raw)+block), 0o600); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
	return dst
}
