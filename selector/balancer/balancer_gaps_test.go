// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package balancer

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/balancer"
	"google.golang.org/grpc/balancer/base"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/test/bufconn"

	"github.com/kalandramo/bald/selector"
)

// 本文件用进程内 gRPC 服务补 Build/Pick 的端到端行为：grpc v1.83 不允许
// 外部实现 balancer.SubConn，只有真拨号才能拿到 ReadySCs。

// staticResolverBuilder 向 gRPC 推送固定地址表（带属性）。
type staticResolverBuilder struct {
	scheme string
	addrs  []resolver.Address
}

func (b *staticResolverBuilder) Scheme() string { return b.scheme }

func (b *staticResolverBuilder) Build(_ resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) (resolver.Resolver, error) {
	r := &staticResolver{cc: cc, addrs: b.addrs}
	r.ResolveNow(resolver.ResolveNowOptions{})
	return r, nil
}

type staticResolver struct {
	cc    resolver.ClientConn
	addrs []resolver.Address
}

func (r *staticResolver) ResolveNow(resolver.ResolveNowOptions) {
	_ = r.cc.UpdateState(resolver.State{Addresses: r.addrs})
}

func (r *staticResolver) Close() {}

// startBufconnServer 起一个进程内 gRPC health 服务端。
func startBufconnServer(t *testing.T) *bufconn.Listener {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis
}

// dialWithAddresses 用给定地址表（含属性）拨号，负载均衡策略固定为
// bald_selector。scheme 每个用例唯一：resolver 全局注册表不允许重复注册。
func dialWithAddresses(t *testing.T, lis *bufconn.Listener, scheme string, addrs []resolver.Address) (*grpc.ClientConn, *staticResolverBuilder) {
	t.Helper()
	rb := &staticResolverBuilder{scheme: scheme, addrs: addrs}
	conn, err := grpc.NewClient(
		scheme+":///svc",
		grpc.WithResolvers(rb),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithDefaultServiceConfig(DefaultServiceConfig),
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, rb
}

// checkHealth 发起一次 unary RPC，WaitForReady 让 gRPC 等到连接可用再选点。
func checkHealth(t *testing.T, conn *grpc.ClientConn, timeout time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(true))
	return err
}

// p2c 策略经地址属性透传：Build 读到 strategy/service/weight，Pick 成功，
// Done 回调在 RPC 完成后把结果反馈给策略。
func TestPicker_P2CThroughGRPC(t *testing.T) {
	Activate()
	lis := startBufconnServer(t)

	addr := resolver.Address{
		Addr: "bufnet",
		Attributes: attributes.New(StrategyKey, "p2c").
			WithValue(ServiceKey, "svc").
			WithValue(WeightKey, "150"),
	}
	conn, _ := dialWithAddresses(t, lis, "baldgap-p2c", []resolver.Address{addr})

	if err := checkHealth(t, conn, 5*time.Second); err != nil {
		t.Fatalf("RPC through p2c picker: %v", err)
	}
}

// 未携带策略属性时回退 round_robin、权重默认 100；该策略 Done 为 nil，
// Pick 的收尾分支按无回调处理。
func TestPicker_RoundRobinDefaultThroughGRPC(t *testing.T) {
	Activate()
	lis := startBufconnServer(t)

	conn, _ := dialWithAddresses(t, lis, "baldgap-rr", []resolver.Address{{Addr: "bufnet"}})
	if err := checkHealth(t, conn, 5*time.Second); err != nil {
		t.Fatalf("RPC through round_robin picker: %v", err)
	}
}

// 策略 Select 报错时 Pick 原样上抛，RPC 失败。
func TestPicker_SelectErrorThroughGRPC(t *testing.T) {
	Activate()
	registerGapStrategies()
	lis := startBufconnServer(t)

	addr := resolver.Address{
		Addr:       "bufnet",
		Attributes: attributes.New(StrategyKey, "bald_gap_err"),
	}
	conn, _ := dialWithAddresses(t, lis, "baldgap-err", []resolver.Address{addr})

	err := checkHealth(t, conn, time.Second)
	if err == nil {
		t.Fatal("expected RPC failure when strategy Select errors")
	}
	if !strings.Contains(err.Error(), "gap select boom") {
		t.Errorf("error = %v, want strategy error text", err)
	}
}

// 策略选出的地址不在本轮 ReadySCs 里时返回 ErrNoSubConnAvailable。
func TestPicker_MissingSubConnThroughGRPC(t *testing.T) {
	Activate()
	registerGapStrategies()
	lis := startBufconnServer(t)

	addr := resolver.Address{
		Addr:       "bufnet",
		Attributes: attributes.New(StrategyKey, "bald_gap_ghost"),
	}
	conn, _ := dialWithAddresses(t, lis, "baldgap-ghost", []resolver.Address{addr})

	if err := checkHealth(t, conn, 500*time.Millisecond); err == nil {
		t.Fatal("expected RPC failure when selected address has no SubConn")
	}
}

// 无 ReadySCs 时 Build 直接返回错误 Picker（fail-fast，不退化成猜测转发）。
func TestBuild_EmptyReadySCs(t *testing.T) {
	b := &pickerBuilder{}
	p := b.Build(base.PickerBuildInfo{})
	if p == nil {
		t.Fatal("Build returned nil picker")
	}
	if _, err := p.Pick(balancer.PickInfo{}); !errors.Is(err, balancer.ErrNoSubConnAvailable) {
		t.Fatalf("Pick error = %v, want ErrNoSubConnAvailable", err)
	}
}

// 策略工厂返回 nil（注册面允许 Factory 返回任意 Selector，含 nil）时
// Build 得到 nil selector，必须返回「无可用策略」错误而不是解引用崩溃。
func TestPicker_NilSelectorThroughGRPC(t *testing.T) {
	Activate()
	registerGapStrategies()
	lis := startBufconnServer(t)

	addr := resolver.Address{
		Addr:       "bufnet",
		Attributes: attributes.New(StrategyKey, "bald_gap_nil"),
	}
	conn, _ := dialWithAddresses(t, lis, "baldgap-nil", []resolver.Address{addr})

	err := checkHealth(t, conn, time.Second)
	if err == nil {
		t.Fatal("expected RPC failure when strategy factory returns nil")
	}
	if !strings.Contains(err.Error(), "no selector for strategy") {
		t.Errorf("error = %v, want no-selector picker error", err)
	}
}

var registerGapStrategiesOnce sync.Once

func registerGapStrategies() {
	registerGapStrategiesOnce.Do(func() {
		selector.MustRegister("bald_gap_err", func() selector.Selector {
			return gapSelector{err: errors.New("gap select boom")}
		})
		selector.MustRegister("bald_gap_ghost", func() selector.Selector {
			return gapSelector{addr: "ghost"}
		})
		selector.MustRegister("bald_gap_nil", func() selector.Selector { return nil })
	})
}

type gapSelector struct {
	err  error
	addr string
}

func (g gapSelector) Select(context.Context, []selector.Node) (selector.Node, selector.DoneFunc, error) {
	if g.err != nil {
		return nil, nil, g.err
	}
	return selector.NewNode(g.addr, "", "", 100, nil), nil, nil
}
