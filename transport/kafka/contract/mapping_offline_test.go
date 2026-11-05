package contract

// mapping_offline_test.go —— server.kafka 映射行的运行期判据
// （真 Start、真订阅 flush；「offline」指不需要任何 Kafka 集群）。
//
// 为什么要这一层：contract_test.go 只证明「Option 被追加、broker.Options 落值」，
// 证明不了「Start 之后这些值活在服务端面上」。broker 型传输的线上面不是 HTTP
// 请求——kafka 不开监听——而是两件事：
//   - Endpoint() 的生命周期形状：构造期空（waitForEndpoints 判未就绪）→
//     Start 成功后描述性 kafka://addr → Stop 后回空；这条线直接决定
//     声明式装配下 kafka 段能不能起得来（端点恒空会被就绪闸门掐死启动）；
//   - Start 的订阅 flush：deferred map 在 Start 里统一 Subscribe，
//     任一失败 errors.Join 上抛——「登记了却没 flush 上」必须让 Start 变红。
//
// 消息投递的端到端（publish→handler 收到）需要真集群，归 broker 层的
// live-cluster 集成判据，不在本文件范围（§1.3 那轮的教训：离线判离线面，
// 别拿假集群冒充线上证据）。

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/kalandramo/bald/broker"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/kafka"
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

// startKafka 走 Provider 构造并 Start（kafka 的 Start 同步完成 Init+Connect+flush
// 后即返回，不阻塞），注册 Stop 清理。
func startKafka(t *testing.T, opts []Option, cfg *bootstrapv1.Server) *kafka.Server {
	t.Helper()
	srv, cleanup, err := Provider(opts...)(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	ksrv, ok := srv.(*kafka.Server)
	if !ok {
		t.Fatalf("srv = %T, want *kafka.Server", srv)
	}
	// kafka 的 Connect 只翻标志位（不拨号），离线 Start 必然成功——
	// 这里若红了，红的是装配本身，不是环境。
	if err := ksrv.Start(context.Background()); err != nil {
		t.Fatalf("Start (offline; Connect is flag-only, see broker/kafka): %v", err)
	}
	t.Cleanup(func() { _ = ksrv.Stop(context.Background()) })
	return ksrv
}

// --- Endpoint 生命周期（readiness 闸门的数据源） -----------------------------

// TestMappingOffline_StartMakesEndpointDescriptiveAndStopResets 端点三态：
// 构造 "" → Start "kafka://<addrs[0]>" → Stop ""。中间那态是 appkit
// waitForEndpoints 认「就绪」的根据，末态保证 Stop 后不会被误判还在服务。
func TestMappingOffline_StartMakesEndpointDescriptiveAndStopResets(t *testing.T) {
	addr := loopbackAddr(t)
	secondary := loopbackAddr(t)
	cfg := &bootstrapv1.Server{Kafka: &bootstrapv1.Server_Kafka{
		Addrs: []string{addr, secondary},
	}}

	ksrv := startKafka(t, nil, cfg)

	if ep := ksrv.Endpoint(); ep != "kafka://"+addr {
		t.Fatalf("Endpoint() after Start = %q, want kafka://%s（描述端点必须取 addrs[0]，不是兜底默认）", ep, addr)
	}
	if err := ksrv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if ep := ksrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() after Stop = %q, want \"\"（started 复位是就绪闸门的一部分）", ep)
	}
}

// --- codec / addrs 行在 Start 后的落点 ---------------------------------------

// TestMappingOffline_BusinessAddressOverridesContract 业务 WithAddress 顶掉契约
// addrs 之后，Start 后端点读的是业务地址——覆盖序的线上形态（构造期证据见
// TestProvider_BusinessOptionsAppendAfterContract）。
func TestMappingOffline_BusinessAddressOverridesContract(t *testing.T) {
	contractAddr := loopbackAddr(t)
	businessAddr := loopbackAddr(t)
	ksrv := startKafka(t,
		[]Option{WithServerOptions(kafka.WithAddress([]string{businessAddr}))},
		&bootstrapv1.Server{Kafka: &bootstrapv1.Server_Kafka{Addrs: []string{contractAddr}}})
	if ep := ksrv.Endpoint(); ep != "kafka://"+businessAddr {
		t.Fatalf("Endpoint() = %q, want kafka://%s（契约地址 %s 不该还在服务面上）", ep, businessAddr, contractAddr)
	}
}

// --- 订阅 flush（deferred map → Start） --------------------------------------

// TestMappingOffline_SubscriberFromAfterBuildFlushesAtStart WithAfterBuild 登记的
// 消费者在 Start 的 flush 里真的被 Subscribe：flush 失败会 errors.Join 上抛、
// started 不落真——端点保持描述性即证明这条链走通。
func TestMappingOffline_SubscriberFromAfterBuildFlushesAtStart(t *testing.T) {
	addr := loopbackAddr(t)
	var registered bool
	ksrv := startKafka(t, []Option{WithAfterBuild(func(ctx context.Context, s *kafka.Server) error {
		if err := s.RegisterSubscriber(ctx, "kt.contract.topic", "q1", false,
			func(context.Context, broker.Event) error { return nil },
			func() any { return &payload{} }); err != nil {
			return err
		}
		registered = true
		return nil
	})}, &bootstrapv1.Server{Kafka: &bootstrapv1.Server_Kafka{Addrs: []string{addr}}})

	if !registered {
		t.Fatal("RegisterSubscriber in after-build was not run")
	}
	if ep := ksrv.Endpoint(); ep != "kafka://"+addr {
		t.Fatalf("Endpoint() = %q, want kafka://%s（带订阅者的 Start 没走完 flush？）", ep, addr)
	}
}

// TestMappingOffline_RegisterSubscriberAfterStartUsesLivePath Start 之后的
// RegisterSubscriber 走 doRegisterSubscriber 直连路径（不再是 deferred map），
// 离线同样必须成功——两个分支都要有判据，否则 flush 路径绿掩盖 live 路径坏。
func TestMappingOffline_RegisterSubscriberAfterStartUsesLivePath(t *testing.T) {
	addr := loopbackAddr(t)
	ksrv := startKafka(t, nil, &bootstrapv1.Server{Kafka: &bootstrapv1.Server_Kafka{Addrs: []string{addr}}})

	if err := ksrv.RegisterSubscriber(context.Background(), "kt.live.topic", "q2", false,
		func(context.Context, broker.Event) error { return nil },
		func() any { return &payload{} }); err != nil {
		t.Fatalf("RegisterSubscriber after Start: %v", err)
	}
	if ep := ksrv.Endpoint(); ep != "kafka://"+addr {
		t.Fatalf("Endpoint() = %q, want kafka://%s", ep, addr)
	}
}
