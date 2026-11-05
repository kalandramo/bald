package contract

// mapping_offline_test.go —— server.rocketmq 映射行的运行期判据
// （真 Start、真订阅 flush；「offline」指不需要任何 RocketMQ 集群）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加、codec 落值」，
// 证明不了「Start 之后契约地址活在服务端面上」。rocketmq 的地址经
// options.Context 传递、Init 才读出到 nameServers——构造期 Address() 读到的是
// 兜底默认，端点三态（构造 "" → Start "rocketmq://addrs[0]" → Stop ""）是唯一
// 能把「映射行 → 运行期面」连起来的离线判据，也是 appkit waitForEndpoints 的
// 数据源。
//
// 离线边界（诚实登记，不拿假集群冒充线上证据）：v2 broker 的 Connect 只翻
// 标志位，**无订阅者**的 Start 离线必成；但订阅 flush 走 SDK PushConsumer 的
// 同步启动（拉路由要真 NameServer），离线必红——所以订阅者两腿判的是「失败
// 形状」：flush/live 路径真的把订阅递给了 SDK（Start/RegisterSubscriber 报错）
// 且失败后 started 不落真、端点保持空。「订阅成功上线」属真集群判据（§7.24 同口径）。

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/kalandramo/bald/broker"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	rocketmq "github.com/kalandramo/bald/transport/rocketmq"
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

// startRocketmq 走 Provider 构造并 Start。**只能用于无订阅者的形状**：
// v2 的 Connect 是标志位（离线必成），但订阅 flush 离线会在 SDK 路由拉取处红
// （见文件头边界）——带订阅者的腿请直接用未 Start 的 server 判失败形状。
func startRocketmq(t *testing.T, opts []Option, cfg *bootstrapv1.Server) *rocketmq.Server {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	rsrv, ok := srv.(*rocketmq.Server)
	if !ok {
		t.Fatalf("srv = %T, want *rocketmq.Server", srv)
	}
	// Init 纯读表、Connect 只翻标志位——这里若红了，红的是装配本身，不是环境。
	if err := rsrv.Start(context.Background()); err != nil {
		t.Fatalf("Start (offline, no subscribers; Connect is flag-only, see broker/rocketmq/v2): %v", err)
	}
	t.Cleanup(func() { _ = rsrv.Stop(context.Background()) })
	return rsrv
}

// newBuiltRocketmq 构造但不 Start（订阅者腿用：Start 在离线必红，红的形状才是要判的）。
func newBuiltRocketmq(t *testing.T, opts []Option, cfg *bootstrapv1.Server) *rocketmq.Server {
	t.Helper()
	srv, _, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	rsrv, ok := srv.(*rocketmq.Server)
	if !ok {
		t.Fatalf("srv = %T, want *rocketmq.Server", srv)
	}
	return rsrv
}

// --- Endpoint 生命周期（readiness 闸门的数据源） -----------------------------

// TestMappingOffline_StartMakesEndpointDescriptiveAndStopResets 端点三态：
// 构造 "" → Start "rocketmq://<addrs[0]>" → Stop ""。中间态同时证明 Init 真的
// 把契约 NameServer 读进了 broker 地址面（兜底默认 127.0.0.1:9876 不会等于这
// 个随机端口）；末态保证 Stop 后不会被误判还在服务。
func TestMappingOffline_StartMakesEndpointDescriptiveAndStopResets(t *testing.T) {
	addr := loopbackAddr(t)
	secondary := loopbackAddr(t)
	cfg := rocketmqServerCfg([]string{addr, secondary}, "", "")

	rsrv := startRocketmq(t, nil, cfg)

	if ep := rsrv.Endpoint(); ep != "rocketmq://"+addr {
		t.Fatalf("Endpoint() after Start = %q, want rocketmq://%s（描述端点必须取契约 addrs[0]，不是兜底默认）", ep, addr)
	}
	if err := rsrv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after Stop = %q, want \"\"（started 复位是就绪闸门的一部分）", ep)
	}
}

// --- addrs 行在 Start 后的落点 ------------------------------------------------

// TestMappingOffline_BusinessAddressOverridesContract 业务 WithNameServer 顶掉
// 契约 addrs 之后（context 键末者胜），Start 后端点读的是业务地址——覆盖序的
// 线上形态（构造期该映射没有公开可观测面，这里一并判掉）。
func TestMappingOffline_BusinessAddressOverridesContract(t *testing.T) {
	contractAddr := loopbackAddr(t)
	businessAddr := loopbackAddr(t)
	rsrv := startRocketmq(t,
		[]Option{WithServerOptions(rocketmq.WithNameServer([]string{businessAddr}))},
		rocketmqServerCfg([]string{contractAddr}, "", ""))
	if ep := rsrv.Endpoint(); ep != "rocketmq://"+businessAddr {
		t.Fatalf("Endpoint() = %q, want rocketmq://%s（契约地址 %s 不该还在服务面上）",
			ep, businessAddr, contractAddr)
	}
}

// --- 订阅 flush（deferred map → Start）与 live 路径 ---------------------------

// TestMappingOffline_DeferredSubscriberFailsFlushAtStartOffline WithAfterBuild
// 登记的消费者在 Start 的 flush 里真的被递进 SDK：离线没有 NameServer，SDK 的
// 同步启动必红 → flush errors.Join 上抛 → Start 红且 started 不落真、端点保持
// 空。「登记了但没 flush 上」（钩子没跑/映射断）与「flush 了但离线起不来」在
// 这里可分：前者 registered 为假或 Start 意外绿，后者是本腿的形状。
//
// 判据不锁 SDK 错误文案，只锁失败形状；离线路由超时由 SDK 常量兜住
// （NewPushConsumer 内部有限等待），测试总超时在 CI 侧统一给。
func TestMappingOffline_DeferredSubscriberFailsFlushAtStartOffline(t *testing.T) {
	addr := loopbackAddr(t)
	var registered bool
	rsrv := newBuiltRocketmq(t, []Option{WithAfterBuild(func(ctx context.Context, s *rocketmq.Server) error {
		if err := s.RegisterSubscriber(ctx, "rt.contract.topic", "q1",
			func(context.Context, broker.Event) error { return nil },
			func() any { return &payload{} }); err != nil {
			return err
		}
		registered = true
		return nil
	})}, rocketmqServerCfg([]string{addr}, "", ""))

	err := rsrv.Start(context.Background())
	t.Cleanup(func() { _ = rsrv.Stop(context.Background()) })

	if !registered {
		t.Fatal("RegisterSubscriber in after-build was not run")
	}
	if err == nil {
		t.Fatal("Start with a deferred subscriber succeeded offline, want SDK route fetch to fail（flush 路径没把订阅递出去？）")
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after failed Start = %q, want \"\"（失败后 started 必须不落真）", ep)
	}
}

// TestMappingOffline_LiveSubscribePathAttemptedAfterStart Start 之后的
// RegisterSubscriber 走 doRegisterSubscriber 直连路径（不再是 deferred map）：
// 离线同样必须把订阅递进 SDK 并红回来——两个分支都要有判据，否则 flush 路径的
// 红掩盖 live 路径根本没接（与 kafka 同名腿互补：kafka 的 live 离线绿，
// rocketmq 的 live 离线红，红的形状恰恰证明调用真的发生）。
func TestMappingOffline_LiveSubscribePathAttemptedAfterStart(t *testing.T) {
	addr := loopbackAddr(t)
	rsrv := startRocketmq(t, nil, rocketmqServerCfg([]string{addr}, "", ""))

	err := rsrv.RegisterSubscriber(context.Background(), "rt.live.topic", "q2",
		func(context.Context, broker.Event) error { return nil },
		func() any { return &payload{} })
	if err == nil {
		t.Fatal("live RegisterSubscriber succeeded offline, want SDK route fetch to fail（live 路径没接 Subscribe？）")
	}
	if ep := rsrv.Endpoint(); ep != "rocketmq://"+addr {
		t.Fatalf("Endpoint() = %q, want rocketmq://%s（失败的 live 订阅不该掀掉已在服务的端点）", ep, addr)
	}
}
