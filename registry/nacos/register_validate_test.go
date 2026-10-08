package nacos

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/bald/registry"
)

// TestRegister_RejectsInvalidInstance 是 D15 在 nacos 后端的**离线**接线验证。
//
// nacos 原先有一段内联的 `si.Name == ""` 判断（仅 Name、无 ID）；D15 修法把
// 它统一收编到 registry.ServiceInstance.Validate，并**补上此前缺失的 ID 校验**。
// 本测试同时钉住这两点：缺 Name 被拒、缺 ID 也被拒。
//
// 离线：fakeNamingClient 内嵌 nil INamingClient，任何 client 方法调用都会 panic
// ——恰好保证本测试只触发校验路径（若闸门被删，会 panic 而非返回校验错误）。
func TestRegister_RejectsInvalidInstance(t *testing.T) {
	cli := &fakeNamingClient{}
	r := newRegistry(cli, newOptions(), false)
	ctx := context.Background()

	if err := r.Register(ctx, &registry.ServiceInstance{ID: "id-only"}); err == nil {
		t.Fatal("D15：nacos.Register 缺 Name 必须返回错误")
	} else if !strings.Contains(err.Error(), "Name") {
		t.Errorf("应为校验错误（提及 Name），got: %v", err)
	}

	// 缺 ID 是 D15 新增覆盖的场景（此前 nacos 只查 Name）。
	if err := r.Register(ctx, &registry.ServiceInstance{Name: "svc"}); err == nil {
		t.Error("D15：nacos.Register 缺 ID 必须被拒（原先只校验 Name）")
	}
}
