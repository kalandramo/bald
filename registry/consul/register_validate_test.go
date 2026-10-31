package consul

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/bald/registry"
)

// TestRegister_RejectsInvalidInstance 验证 consul 后端在触网前接入
// registry.ServiceInstance.Validate 单一闸门。
//
// 为什么逐后端断言：Validate 是单一真相源，但「每个后端确实调了它」需要
// 逐后端证明——只验一个后端不足以证明其余接线。
//
// 本测试离线（newRegistry(nil)，无真实 consul）：
//   - Register 的 Validate 闸门是第一行，在接触 client 之前返回；
//   - 若闸门被删除，Register 会走到 r.cli.Register（其内部 cli 为 nil）——
//     表现为 panic 或网络错误，而非校验错误 → 测试失败。
func TestRegister_RejectsInvalidInstance(t *testing.T) {
	r := newRegistry(nil) // 离线：不建真实 consul client
	ctx := context.Background()

	if err := r.Register(ctx, &registry.ServiceInstance{ID: "id-only"}); err == nil {
		t.Fatal("consul.Register 缺 Name 必须返回错误（而非静默注册到畸形 key）")
	} else if !strings.Contains(err.Error(), "Name") {
		t.Errorf("应为校验错误（提及 Name），got: %v", err)
	}

	if err := r.Register(ctx, &registry.ServiceInstance{Name: "svc"}); err == nil {
		t.Error("consul.Register 缺 ID 也必须被拒")
	}
}
