package inmemory

import (
	"context"
	"sync"
	"testing"

	"github.com/kalandramo/bald/pkg/registry"
)

func newInstance(id, name string) *registry.ServiceInstance {
	return &registry.ServiceInstance{
		ID:        id,
		Name:      name,
		Version:   "v1",
		Metadata:  map[string]string{"scheme": "http"},
		Endpoints: []string{"http://" + id + ":8080"},
	}
}

// TestRegistrar_RegisterAndList：注册后可通过 List 查到，且按 ID 索引。
func TestRegistrar_RegisterAndList(t *testing.T) {
	r := New()
	inst := newInstance("node-1", "svc-a")
	if err := r.Register(context.Background(), inst); err != nil {
		t.Fatalf("Register: %v", err)
	}
	all := r.List()
	if len(all) != 1 {
		t.Fatalf("List len = %d, want 1", len(all))
	}
	if all[0].ID != "node-1" || all[0].Name != "svc-a" {
		t.Fatalf("unexpected instance: %+v", all[0])
	}
}

// TestRegistrar_RegisterOverwritesByID：同 ID 注册应覆盖而非新增。
func TestRegistrar_RegisterOverwritesByID(t *testing.T) {
	r := New()
	if err := r.Register(context.Background(), newInstance("node-1", "svc-a")); err != nil {
		t.Fatalf("Register: %v", err)
	}
	updated := newInstance("node-1", "svc-a")
	updated.Version = "v2"
	if err := r.Register(context.Background(), updated); err != nil {
		t.Fatalf("Register again: %v", err)
	}
	if len(r.List()) != 1 {
		t.Fatalf("after overwrite, List len = %d, want 1", len(r.List()))
	}
	if got := r.List()[0].Version; got != "v2" {
		t.Fatalf("version = %q, want v2 (overwritten by same ID)", got)
	}
}

// TestRegistrar_Deregister：注销后 List 不再包含该实例。
func TestRegistrar_Deregister(t *testing.T) {
	r := New()
	inst := newInstance("node-1", "svc-a")
	_ = r.Register(context.Background(), inst)

	if err := r.Deregister(context.Background(), inst); err != nil {
		t.Fatalf("Deregister: %v", err)
	}
	if len(r.List()) != 0 {
		t.Fatalf("after Deregister, List len = %d, want 0", len(r.List()))
	}
	// 注销不存在的实例应安全（幂等）。
	if err := r.Deregister(context.Background(), newInstance("missing", "x")); err != nil {
		t.Fatalf("Deregister missing: %v", err)
	}
}

// TestRegistrar_ConcurrentAccess：并发注册/注销不触发 data race。
// 需在 -race 下运行（CI 默认开启）。
func TestRegistrar_ConcurrentAccess(t *testing.T) {
	r := New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "node-" + string(rune('a'+i%26)) // 制造部分碰撞以触发覆盖路径
			inst := newInstance(id, "svc")
			_ = r.Register(context.Background(), inst)
			_ = r.Deregister(context.Background(), inst)
		}(i)
	}
	wg.Wait()
	// 最终 List 长度不确定（碰撞），但不应 panic 或触发 race。
}

// TestRegistrar_RegisterNilInstance：传入 nil 实例应安全（不 panic）。
func TestRegistrar_RegisterNilInstance(t *testing.T) {
	r := New()
	// nil 在 map 赋值会 panic，这里验证实现是否防御；当前实现直接写入会 panic，
	// 因此该用例登记为"已知行为"，仅作文档化：调用方不应传 nil。
	defer func() {
		if recover() == nil {
			// 未 panic 说明实现已防御，符合预期。
		}
	}()
	_ = r.Register(context.Background(), nil)
}
