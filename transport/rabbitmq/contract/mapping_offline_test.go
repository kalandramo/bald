package contract

// mapping_offline_test.go —— server.rabbitmq 映射行的运行期判据（真 Init、真
// 拨号；「offline」指不需要任何 RabbitMQ 集群）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加、codec 落值」，证明不
// 了「Start 走的拨号目标就是契约地址」。broker 的 Init 把裸 host:port 规范成
// amqp:// URL 后写回 options.Addrs，Connect 再把 Addrs[0] 交进 AMQP 握手——这条
// 链断在任何一处，表现都是「配了 A 却拨到别处」，离线唯一能判的形状就是拒连端口
// 上错误信息里出现的那个地址。
//
// 离线边界（诚实登记，不拿假绿冒充线上证据）：Connect 是完整 AMQP 0-9-1 握手，
// 进程内没有可信的假后端（伪造帧序只会把「握手过没过」变成自证）。而 transport
// 的 Start 顺序是 Init → Connect → 订阅 flush——Connect 先红，flush 与
// started 落真都到不了。所以本层能判的是「地址映射 + fail-fast + 失败端点闸门」；
// 「Start 成功后端点变描述性」「订阅真的挂上队列」属真集群判据（与 kafka/redis
// 同口径归 CI 服务集成）。订阅腿只判失败形状不被登记动作污染这一件事。

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/kalandramo/bald/broker"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/rabbitmq"
)

type payload struct {
	Msg string `json:"msg"`
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err := ln.Close(); err != nil {
		t.Fatalf("release port: %v", err)
	}
	return port
}

func loopbackAddr(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("127.0.0.1:%d", freePort(t))
}

// newRabbitmq 构造但不 Start：本包所有腿的 Start 在离线都必红（拨的是拒连端口），
// 判的正是红的形状。
func newRabbitmq(t *testing.T, opts []Option, cfg *bootstrapv1.Server) *rabbitmq.Server {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	rsrv, ok := srv.(*rabbitmq.Server)
	if !ok {
		t.Fatalf("srv = %T, want *rabbitmq.Server", srv)
	}
	t.Cleanup(func() { _ = rsrv.Stop(context.Background()) })
	return rsrv
}

// TestMappingOffline_RefusedContractAddressDialsIt 契约地址就是拨号目标：
// 拒连端口的 Start 必红，且错误里出现的地址是契约那个 host:port（不是
// guest:guest@127.0.0.1:5672 兜底默认，也不是别的随机端口）。Address() 同时
// 判 Init 的规范化真的写回了 broker 地址面——拨号读的正是 Addrs[0]。
// 端点必须还是空：Start 失败不落 started，否则 waitForEndpoints 会把它当就绪。
func TestMappingOffline_RefusedContractAddressDialsIt(t *testing.T) {
	addr := loopbackAddr(t)
	rsrv := newRabbitmq(t, nil, rabbitmqServerCfg([]string{addr}, ""))

	err := rsrv.Start(context.Background())
	if err == nil {
		t.Fatalf("Start on refused %s succeeded, want AMQP dial to fail（映射错了会拨到别处甚至绿）", addr)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Fatalf("Start error = %v, want dial of contract address %s", err, addr)
	}
	if got := rsrv.Address(); got != "amqp://"+addr {
		t.Fatalf("Address() = %q, want amqp://%s（Init 的规范化没写回 options.Addrs？）", got, addr)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after failed Start = %q, want \"\"", ep)
	}
}

// TestMappingOffline_BusinessAddressOverridesContractDial 业务 WithAddress 顶掉
// 契约 addrs 之后（broker.Options 末者胜），拨的是业务地址：错误里出现业务地址
// 且不出现契约地址——覆盖序的线上形态，构造期那条腿判不到的那一半。
func TestMappingOffline_BusinessAddressOverridesContractDial(t *testing.T) {
	contractAddr := loopbackAddr(t)
	businessAddr := loopbackAddr(t)
	rsrv := newRabbitmq(t,
		[]Option{WithServerOptions(rabbitmq.WithAddress([]string{businessAddr}))},
		rabbitmqServerCfg([]string{contractAddr}, ""))

	err := rsrv.Start(context.Background())
	if err == nil {
		t.Fatalf("Start on refused %s succeeded, want dial error", businessAddr)
	}
	if !strings.Contains(err.Error(), businessAddr) {
		t.Fatalf("Start error = %v, want dial of business address %s", err, businessAddr)
	}
	if strings.Contains(err.Error(), contractAddr) {
		t.Fatalf("Start error = %v, want only business address %s, contract %s must be gone from dial target",
			err, businessAddr, contractAddr)
	}
	if got := rsrv.Address(); got != "amqp://"+businessAddr {
		t.Fatalf("Address() = %q, want amqp://%s", got, businessAddr)
	}
}

// TestMappingOffline_DialsFirstAddrOnly addrs 是列表，但 broker 只拨 Addrs[0]
// （connection.go 的 init 读 [0]，reconnect 也只用同一个 url）。登记这个事实：
// 第二元素在 rabbitmq 后端是装饰，不是多节点容错（与 kafka 的 addrs 语义不同）。
// 若哪天实现改成轮询多节点，本腿会红——那时要同步改契约文档而不是删腿。
func TestMappingOffline_DialsFirstAddrOnly(t *testing.T) {
	first := loopbackAddr(t)
	second := loopbackAddr(t)
	rsrv := newRabbitmq(t, nil, rabbitmqServerCfg([]string{first, second}, ""))

	err := rsrv.Start(context.Background())
	if err == nil {
		t.Fatal("Start on refused address succeeded, want dial error")
	}
	if !strings.Contains(err.Error(), first) {
		t.Fatalf("Start error = %v, want dial of addrs[0] %s", err, first)
	}
	if strings.Contains(err.Error(), second) {
		t.Fatalf("Start error = %v, want addrs[0] only, addrs[1] %s is inert on this backend", err, second)
	}
}

// TestMappingOffline_DeferredSubscriberDoesNotChangeFailureShape Start 之前的
// RegisterSubscriber 进 deferred map；本腿判三件事：钩子真的跑了、订阅登记不会
// 让失败的 Start 意外落真 started（端点仍空）、也不会 panic。
//
// 明确不判的：flush 是否真把订阅挂上队列——它在 Connect 之后，离线走不到（文件
// 头边界）。那条属真集群集成，不是本腿的证据。
func TestMappingOffline_DeferredSubscriberDoesNotChangeFailureShape(t *testing.T) {
	addr := loopbackAddr(t)
	var registered bool
	rsrv := newRabbitmq(t, []Option{WithAfterBuild(func(ctx context.Context, s *rabbitmq.Server) error {
		if err := s.RegisterSubscriber(ctx, "rmq.contract.topic",
			func(context.Context, broker.Event) error { return nil },
			func() any { return &payload{} }); err != nil {
			return err
		}
		registered = true
		return nil
	})}, rabbitmqServerCfg([]string{addr}, ""))

	if !registered {
		t.Fatal("RegisterSubscriber in after-build was not run")
	}
	if err := rsrv.Start(context.Background()); err == nil {
		t.Fatalf("Start on refused %s succeeded, want dial error", addr)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after failed Start = %q, want \"\"（订阅登记不该把未连上的服务报成就绪）", ep)
	}
}
