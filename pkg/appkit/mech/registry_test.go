package mech

import (
	"fmt"
	"sync"
	"testing"
)

// TestRegistry：泛型注册表契约（R4 复审——运行期可逆挂载的基座）。
// 钉住：Register 重名报错不覆盖（防静默踩踏）、MustRegister 重名 panic、
// Get 未命中返回 (零值, false)、List 升序稳定输出。
func TestRegistry(t *testing.T) {
	r := NewRegistry[string]()

	// 空注册表：Get 未命中、List 为空。
	if v, ok := r.Get("missing"); ok || v != "" {
		t.Errorf("空注册表 Get 应返回 (零值, false), got (%q, %v)", v, ok)
	}
	if got := r.List(); len(got) != 0 {
		t.Errorf("空注册表 List 应为空, got %v", got)
	}

	// 正常注册。
	if err := r.Register("alpha", "1"); err != nil {
		t.Fatalf("首次 Register 不应报错: %v", err)
	}

	// 重名：报错且不覆盖。
	if err := r.Register("alpha", "2"); err == nil {
		t.Fatal("重名 Register 必须报错（防静默踩踏）")
	}
	if v, _ := r.Get("alpha"); v != "1" {
		t.Fatalf("重名注册不得覆盖原值: got %q, want %q", v, "1")
	}

	// MustRegister：新名不 panic，重名 panic。
	r.MustRegister("beta", "2")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("MustRegister 重名必须 panic（装配期场景快速失败）")
			}
		}()
		r.MustRegister("beta", "x")
	}()

	// List 升序。
	got := r.List()
	if len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("List 应升序返回全部名字, got %v", got)
	}
}

// TestRegistryConcurrent：注册表并发读写安全（-race 下验证 RWMutex 契约；
// 重名冲突是预期的，只验证不 panic、不丢已注册项）。
func TestRegistryConcurrent(t *testing.T) {
	r := NewRegistry[int]()
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = r.Register(fmt.Sprintf("k%d", i%8), i) // 故意重名
			_, _ = r.Get("k0")
			_ = r.List()
		}(i)
	}
	wg.Wait()

	if got := r.List(); len(got) != 8 {
		t.Errorf("并发注册后应有 8 个唯一名字, got %d: %v", len(got), got)
	}
}

// ---- Registry[T].Mount/Unmount ----

// TestRegistry_MountUndo 契约：挂载生效 → undo 后失效 → undo 幂等（重复无副作用）。
func TestRegistry_MountUndo(t *testing.T) {
	r := NewRegistry[string]()
	undo, err := r.Mount("tool-a", "impl-a")
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if got, ok := r.Get("tool-a"); !ok || got != "impl-a" {
		t.Fatalf("after mount Get = (%q,%v), want (impl-a,true)", got, ok)
	}

	undo()
	if got, ok := r.Get("tool-a"); ok || got != "" {
		t.Fatalf("after undo Get = (%q,%v), want (\"\",false)", got, ok)
	}
	undo() // 幂等：二次调用无 panic 无副作用
	if _, ok := r.items["tool-a"]; ok {
		t.Fatal("undo must be idempotent")
	}
}

// TestRegistry_MountConflict 契约：与 Register/Mount 互斥冲突——同名已存在
// （无论谁写入）挂载失败且不覆盖。
func TestRegistry_MountConflict(t *testing.T) {
	r := NewRegistry[string]()
	_ = r.Register("tool", "from-register")
	if _, err := r.Mount("tool", "from-mount"); err == nil {
		t.Fatal("Mount over Register name must fail")
	}
	if got, ok := r.Get("tool"); !ok || got != "from-register" {
		t.Fatalf("Mount must not overwrite, got (%q,%v)", got, ok)
	}

	undo, err := r.Mount("tool2", "m1")
	if err != nil {
		t.Fatalf("Mount tool2: %v", err)
	}
	if _, err := r.Mount("tool2", "m2"); err == nil {
		t.Fatal("second Mount same name must fail")
	}
	if got, ok := r.Get("tool2"); !ok || got != "m1" {
		t.Fatalf("second Mount must not overwrite, got (%q,%v)", got, ok)
	}
	undo()
}

// TestRegistry_UnmountMissing 契约：卸载不存在的条目返回零值+false（无 panic）。
func TestRegistry_UnmountMissing(t *testing.T) {
	r := NewRegistry[string]()
	got, ok := r.Unmount("nope")
	if ok || got != "" {
		t.Fatalf("Unmount missing = (%q,%v), want (%q,false)", got, ok, "")
	}
}
