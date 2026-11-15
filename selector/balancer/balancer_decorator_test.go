// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package balancer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/attributes"
	"google.golang.org/grpc/resolver"

	"github.com/kalandramo/bald/selector"
)

// 本文件钉 UseDecorator 的接线行为：装饰在 (策略, 服务名) 缓存填充点套用、
// picker 的 Done 反馈经装饰层转发进内层策略、被装饰层硬排除的节点让 Pick
// 报错（全排除）或绕开（部分排除）。装饰实现用进程内 fake——真熔断实现
// （sentinel）的跳闸/愈合行为在 selector/circuitbreaker 的集成测试里，
// 本文件只钉 picker 侧的管道，不把 sentinel 依赖拖进 balancer module。

// excludeDecorator 是测试用装饰器：被标黑的地址不进内层候选集（模拟按节点
// 熔断的硬排除），每次 Done 反馈计数。buildN 记装饰器被调用的次数——缓存
// 语义要求每个 (策略, 服务名) 只装饰一次。
type excludeDecorator struct {
	mu       sync.Mutex
	excluded map[string]bool
	dones    atomic.Int32
	buildN   atomic.Int32
}

func newExcludeDecorator(addrs ...string) *excludeDecorator {
	m := make(map[string]bool, len(addrs))
	for _, a := range addrs {
		m[a] = true
	}
	return &excludeDecorator{excluded: m}
}

func (d *excludeDecorator) exclude(addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.excluded[addr] = true
}

func (d *excludeDecorator) admit(addr string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.excluded, addr)
}

func (d *excludeDecorator) decorate(sel selector.Selector) selector.Selector {
	d.buildN.Add(1)
	return &excludeSel{d: d, inner: sel}
}

// excludeSel 是按黑名单硬排除的 selector 装饰（与 selector/circuitbreaker
// 同形：过滤候选集、转发 Done，只是判据换成测试可控的 map）。
type excludeSel struct {
	d     *excludeDecorator
	inner selector.Selector
}

func (s *excludeSel) Select(ctx context.Context, nodes []selector.Node) (selector.Node, selector.DoneFunc, error) {
	healthy := make([]selector.Node, 0, len(nodes))
	s.d.mu.Lock()
	for _, n := range nodes {
		if !s.d.excluded[n.Address()] {
			healthy = append(healthy, n)
		}
	}
	s.d.mu.Unlock()
	if len(healthy) == 0 {
		return nil, nil, errors.New("fake circuit open: all nodes excluded")
	}
	node, done, err := s.inner.Select(ctx, healthy)
	if err != nil {
		return nil, nil, err
	}
	return node, func(callCtx context.Context, di selector.DoneInfo) {
		s.d.dones.Add(1)
		if done != nil {
			done(callCtx, di)
		}
	}, nil
}

// decoratorSvcSeq 让每轮测试用独立的 ServiceKey：pickerBuilder 是 Activate
// 注册的全局单例，(策略, 服务名) 缓存跨 `-count` 轮次存活——固定键会让第二
// 轮命中第一轮绑定了旧装饰器的缓存实例，装饰器断言全部落空。键空间允许
// 每轮独立，所以轮次递增是最便宜的隔离。
var decoratorSvcSeq atomic.Int64

// TestUseDecorator_ThroughGRPC 走真 gRPC 钉四件事：
//  1. 装饰器在 selector 缓存填充时被套用（每个 (策略, 服务名) 恰好一次）；
//  2. RPC 完成后 picker 的 Done 反馈到达装饰层；
//  3. 全部节点被排除时 RPC 失败（装饰层的错误原样决定 Pick 结果）；
//  4. 解除排除后 RPC 恢复——排除是动态的，随每次 Select 重算。
func TestUseDecorator_ThroughGRPC(t *testing.T) {
	Activate()

	d := newExcludeDecorator()
	UseDecorator(d.decorate)
	t.Cleanup(func() { UseDecorator(nil) }) // 进程级槽，别污染同包其它拨号用例

	// ServiceKey 按轮次取独立值：见 decoratorSvcSeq 的注释——全局缓存跨
	// `-count` 轮次存活，固定键会让第二轮直接命中第一轮的裸/旧装饰实例。
	svc := fmt.Sprintf("svc-decorator-%d", decoratorSvcSeq.Add(1))
	addrs := []resolver.Address{
		{Addr: "bufnet-a", Attributes: attributes.New(ServiceKey, svc)},
		{Addr: "bufnet-b", Attributes: attributes.New(ServiceKey, svc)},
	}
	lis := startBufconnServer(t)
	conn, _ := dialWithAddresses(t, lis, "baldgap-decorator", addrs)

	// 1+2：RPC 成功且 Done 反馈进了装饰层。
	if err := checkHealth(t, conn, 5*time.Second); err != nil {
		t.Fatalf("RPC through decorated picker: %v", err)
	}
	if d.dones.Load() == 0 {
		t.Fatal("picker Done feedback never reached the decorator")
	}
	if n := d.buildN.Load(); n != 1 {
		t.Fatalf("decorator applied %d times, want 1 (cached per strategy+service)", n)
	}

	// 3：全部节点排除 → Pick 报错 → RPC 失败。
	d.exclude("bufnet-a")
	d.exclude("bufnet-b")
	err := checkHealth(t, conn, time.Second)
	if err == nil {
		t.Fatal("expected RPC failure with all nodes excluded")
	}
	if !strings.Contains(err.Error(), "fake circuit open") {
		t.Errorf("error = %v, want decorator's exclusion error text", err)
	}

	// 4：解除一个节点 → 恢复，且后续反馈继续到达装饰层。
	d.admit("bufnet-a")
	if err := checkHealth(t, conn, 5*time.Second); err != nil {
		t.Fatalf("RPC after admitting a node: %v", err)
	}
	after := d.dones.Load()
	if after < 2 {
		t.Fatalf("done count = %d, want >=2 after recovery RPC", after)
	}
}

// UseDecorator 未设置时 picker 原样装配——既有行为零改动（守门：装饰是
// opt-in，不是隐式默认）。
func TestUseDecorator_NilByDefault(t *testing.T) {
	Activate()
	UseDecorator(nil)
	t.Cleanup(func() { UseDecorator(nil) })

	lis := startBufconnServer(t)
	conn, _ := dialWithAddresses(t, lis, "baldgap-nodecorator", []resolver.Address{
		{Addr: "bufnet", Attributes: attributes.New(ServiceKey, "svc-nodecorator")},
	})
	if err := checkHealth(t, conn, 5*time.Second); err != nil {
		t.Fatalf("RPC without decorator: %v", err)
	}
}

// 编译期守门：Decorator 形状能接受 selector/circuitbreaker.WithCircuitBreaker
// 同构的装饰函数（形状契约，不 import 熔断实现）。
var _ Decorator = func(sel selector.Selector) selector.Selector { return sel }
