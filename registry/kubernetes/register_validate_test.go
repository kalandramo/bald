package kubernetes

import (
	"context"
	"strings"
	"testing"

	"github.com/kalandramo/bald/registry"
)

// TestRegister_RejectsInvalidInstance 是 D15 在 kubernetes 后端的**离线**接线验证。
//
// D15 是同族缺陷（四个 registry 后端原先都缺前置校验）。kubernetes 此前无任何
// 测试文件，其接线从未被断言。
//
// 离线构造：用零值 &Registry{}（本测试在包内，可直接构造）。Register 的 Validate
// 闸门是第一行——在任何 clientSet/informer/podLister 访问之前返回，故零值结构体
// 足以驱动校验路径。若闸门被删，Register 会走到 marshal 之后的 clientSet 路径，
// 对 nil clientSet 表现为 panic，而非「提及 Name 的校验错误」→ 测试失败。
func TestRegister_RejectsInvalidInstance(t *testing.T) {
	r := &Registry{}
	ctx := context.Background()

	if err := r.Register(ctx, &registry.ServiceInstance{ID: "id-only"}); err == nil {
		t.Fatal("D15：kubernetes.Register 缺 Name 必须返回错误")
	} else if !strings.Contains(err.Error(), "Name") {
		t.Errorf("应为校验错误（提及 Name），got: %v", err)
	}

	if err := r.Register(ctx, &registry.ServiceInstance{Name: "svc"}); err == nil {
		t.Error("D15：kubernetes.Register 缺 ID 也必须被拒")
	}
}
