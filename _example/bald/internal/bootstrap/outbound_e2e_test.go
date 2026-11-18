//go:build !grpcgw

// 出站按节点熔断的端到端腿：client 段出站经真实装配挂上 bald_selector +
// selector/circuitbreaker 装饰（sentinel 错误计数）后，熔断真的在调用侧
// 生效——喂失败跳闸 → 服务端健康但客户端快速失败 → 过开窗探针愈合。
//
// 失败源用「调用方 deadline < biz.greet.delay」：服务端等不到 handler 完成即
// 回 DEADLINE_EXCEEDED（服务侧失败，AR-18 失败口径下记 MarkFailure），发生在
// pick 之后、同一 subconn 上，Done 回调带着错误进 Mark*——刻意避开 TCP 层故障，
// 那个层面 gRPC 原生的 TRANSIENT_FAILURE 会先于熔断把节点摘掉，测不到 CB 层。
// 不能用 Unimplemented/4xx 喂：AR-18 之后客户端侧错误按「完成」记 MarkSuccess。
//
//	cd _example/bald && go test ./internal/bootstrap/ -run TestOutboundCircuitBreaker -v
package bootstrap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/registry"
	"github.com/kalandramo/bald/registry/inmemory"

	baldv1 "github.com/kalandramo/bald/example/bald/api/gen"
)

func TestOutboundCircuitBreakerTripsAndHeals(t *testing.T) {
	t.Chdir("../..")

	// 开窗压到 300ms，让「跳闸 → 愈合」两腿都落在亚秒窗口内；统计窗与
	// 错误阈值用接线默认（10s / 3 次）。
	t.Setenv("OUTBOUND_CB_RETRY_MS", "300")

	oldFlags := pflag.CommandLine
	pflag.CommandLine = pflag.NewFlagSet("e2e", pflag.ContinueOnError)
	defer func() { pflag.CommandLine = oldFlags }()

	grpcAddr := freeAddr(t)
	t.Setenv("BALD_DEMO_SERVER_GRPC_ADDR", grpcAddr)
	t.Setenv("BALD_DEMO_BIZ_GREET_GREETING", "hello")
	// delay 2s：调用方 500ms deadline 必先到 → 服务侧 DEADLINE_EXCEEDED（喂失败源）；
	// 愈合腿用 3s deadline（delay 内完成，探针放行即成功）。
	t.Setenv("BALD_DEMO_BIZ_GREET_DELAY", "2s")

	// 单实例登记：这条腿断的是「装配 → 出站 → 熔断」的纵向链路，按节点
	// 分流的形状（多实例硬排除）由 selector/balancer 的测试钉住。
	//
	// Metadata 打独立 ServiceKey 是刻意的：pickerBuilder 是 Activate 注册的
	// 全局单例，selector 按 (策略, 服务名) 缓存且**首次填充时绑定当时的装饰器
	// 与注册表**——前面的测试（如 TestClientGRPCSegmentReachesSelf）已经用空
	// service 键缓存过，本测试不点名就会复用别人的注册表：跳闸发生在别人的
	// 开窗参数上，愈合腿永远等不到。与 balancer_decorator_test 的轮次递增键
	// 是同一颗雷的两个爆炸面。
	reg := inmemory.New()
	if err := reg.Register(context.Background(), &registry.ServiceInstance{
		ID:        "client-cb",
		Name:      "bald-demo",
		Endpoints: []string{"grpc://" + grpcAddr},
		Metadata:  map[string]string{"bald.selector.service": "outbound-cb-e2e"},
	}); err != nil {
		t.Fatalf("register self instance: %v", err)
	}

	cfg := roleConfigWithClient(t, configFileForRole(RoleAPI), "self", "discovery:///bald-demo")

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

	greet := func() error { return greetWithDeadline(conn, 3*time.Second) }
	greetFail := func() error { return greetWithDeadline(conn, 500*time.Millisecond) }

	// 第一腿：经 bald_selector 的出站先证明是通的（discovery 解析 + picker 选点）。
	if err := greet(); err != nil {
		t.Fatalf("Greet through bald_selector before any failure: %v", err)
	}

	// 第二腿：喂 3 次服务侧失败（500ms deadline < 2s delay → 服务端回
	// DEADLINE_EXCEEDED，阈值 = 3，错误发生在 pick 之后的同一 subconn 上，
	// Done → MarkFailure）。第 3 发的上报让错误计数到线。
	for i := 0; i < 3; i++ {
		err := greetFail()
		if status.Code(err) != codes.DeadlineExceeded {
			t.Fatalf("trip call %d: code=%v, want DeadlineExceeded（喂失败源）", i, status.Code(err))
		}
	}

	// 第三腿：熔断开窗后，合法 Greet 也在客户端快速失败（Unavailable 且
	// 带熔断文案）——服务端是健康的，拒绝来自本进程的 CB 层。
	err := greet()
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("Greet after trip: code=%v err=%v, want Unavailable", status.Code(err), err)
	}
	if !strings.Contains(status.Convert(err).Message(), "circuit is open") {
		t.Errorf("Greet after trip: message = %q, want circuit-open text", status.Convert(err).Message())
	}

	// 旁证：同一时刻直连服务端仍然健康——排除的是客户端候选集，不是对端。
	direct, derr := grpc.NewClient(grpcAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if derr != nil {
		t.Fatalf("direct dial: %v", derr)
	}
	defer direct.Close()
	dc, dcancel := context.WithTimeout(context.Background(), 3*time.Second)
	_, derr = baldv1.NewGreetServiceClient(direct).Greet(dc, &baldv1.GreetRequest{Name: "direct"})
	dcancel()
	if derr != nil {
		t.Fatalf("direct Greet (server must stay healthy): %v", derr)
	}

	// 第四腿：过开窗（300ms）后探针愈合——合法 Greet 恢复成功。
	time.Sleep(500 * time.Millisecond)
	healDeadline := time.Now().Add(5 * time.Second)
	for {
		err = greet()
		if err == nil {
			return
		}
		if time.Now().After(healDeadline) {
			t.Fatalf("Greet after retry timeout: %v (half-open probe did not heal)", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// greetWithDeadline 以给定截止期经 bald_selector 出站调一次 Greet。
func greetWithDeadline(conn *grpc.ClientConn, d time.Duration) error {
	c, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_, err := baldv1.NewGreetServiceClient(conn).Greet(c, &baldv1.GreetRequest{Name: "cb"})
	return err
}
