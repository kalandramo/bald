package nacos

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/bald/registry"
)

// TestRegister_RejectsInvalidInstance 验证 nacos 后端的 Register 接了契约层
// 校验（单一真相源 registry.ServiceInstance.Validate）：缺 Name 与缺 ID 都必须被拒。
//
// 离线：fakeNamingClient 内嵌 nil INamingClient，任何 client 方法调用都会 panic
// ——恰好保证本测试只触发校验路径（若闸门被删，会 panic 而非返回校验错误）。
func TestRegister_RejectsInvalidInstance(t *testing.T) {
	cli := &fakeNamingClient{}
	r := newRegistry(cli, newOptions(), false)
	ctx := context.Background()

	if err := r.Register(ctx, &registry.ServiceInstance{ID: "id-only"}); err == nil {
		t.Fatal("nacos.Register 缺 Name 必须返回错误")
	} else if !strings.Contains(err.Error(), "Name") {
		t.Errorf("应为校验错误（提及 Name），got: %v", err)
	}

	if err := r.Register(ctx, &registry.ServiceInstance{Name: "svc"}); err == nil {
		t.Error("nacos.Register 缺 ID 必须被拒")
	}
}
