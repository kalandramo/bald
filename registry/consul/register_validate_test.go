package consul

import (
	"context"
	"strings"
	"testing"
	"time"

	registry "github.com/kalandramo/bald/registry"
)

// TestRegister_RejectsInvalidInstance 验证 consul 后端在触网前接入
// registry.ServiceInstance.Validate 单一闸门。
//
// 为什么逐后端断言：Validate 是单一真相源，但「每个后端确实调了它」需要
// 逐后端证明——只验一个后端不足以证明其余接线。
//
// 为什么用「不可达地址」而不是 nil client：nil client 也能判闸门（删了闸门会
// panic），但那只证明「走不到 client」，不证明「闸门在网络动作之前」。这里用
// api.NewClient 建一个连不上的真 client（consul SDK 建 client 不触网），
// 于是三种情况可分辨：
//   - 闸门在位 → 立刻返回校验错误；
//   - 闸门被删 → 走 HTTP 请求，拿到 connection refused（错误里不含 Name/ID 字样）；
//   - 一律拒绝（闸门写坏）→ 合法实例也会被校验错误拦下，被反例判红。
//
// 离线恒运行，不需要真实 consul。
func TestRegister_RejectsInvalidInstance(t *testing.T) {
	// 127.0.0.1:1 无监听：任何网络动作都会立即失败，不会与校验错误混淆。
	r, err := New(WithAddress("127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New（不触网）不应失败: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 缺 Name。
	start := time.Now()
	err = r.Register(ctx, &registry.ServiceInstance{ID: "id-only"})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("consul.Register 缺 Name 必须返回错误（而非静默注册到畸形 key）")
	}
	if !strings.Contains(err.Error(), "Name") {
		t.Errorf("应为校验错误（提及 Name），got: %v（耗时 %v）", err, elapsed)
	}

	// 缺 ID。
	if err := r.Register(ctx, &registry.ServiceInstance{Name: "svc"}); err == nil {
		t.Error("consul.Register 缺 ID 也必须被拒")
	} else if !strings.Contains(err.Error(), "ID") {
		t.Errorf("应为校验错误（提及 ID），got: %v", err)
	}

	// 反例对照：合法实例越过闸门，走到网络动作才失败——错误必须是网络类错误。
	networkErr := r.Register(ctx, &registry.ServiceInstance{
		Name: "svc", ID: "svc-1", Endpoints: []string{"http://127.0.0.1:18080"},
	})
	if networkErr == nil {
		t.Fatal("对不可达端点注册合法实例应失败，不应成功")
	}
	if strings.Contains(networkErr.Error(), "is required") {
		t.Errorf("合法实例不该被校验闸门拦下，got: %v", networkErr)
	}
	// 闸门在任何网络动作之前 → 校验错误不该等到 HTTP 往返。
	if elapsed > time.Second {
		t.Errorf("校验闸门应在触网前返回，缺 Name 那次耗时 %v 已接近一次 HTTP 往返", elapsed)
	}
}
