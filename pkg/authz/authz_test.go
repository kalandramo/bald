package authz

import (
	"context"
	"testing"
)

// TestAllowAll / TestDenyAll：零策略两极端（认证与授权抽象设计决策④——
// 「零策略两极端……放核心让中间件开箱可用（不注入 Authorizer 时默认
// DenyAll，fail-closed）」）。R4 复审：零调用非删除依据，测试钉住语义。
func TestAllowAll(t *testing.T) {
	a := AllowAll()
	for _, args := range [][3]string{
		{"anyone", "anything", "anyhow"},
		{"", "", ""},
	} {
		ok, err := a.Authorize(context.Background(), args[0], args[1], args[2])
		if err != nil {
			t.Fatalf("AllowAll 不应返回 error: %v", err)
		}
		if !ok {
			t.Errorf("AllowAll 必须放行 (subject=%q, object=%q, action=%q)", args[0], args[1], args[2])
		}
	}
}

func TestDenyAll(t *testing.T) {
	a := DenyAll()
	ok, err := a.Authorize(context.Background(), "admin", "resource", "read")
	if err != nil {
		t.Fatalf("DenyAll 不应返回 error（明确拒绝≠判定出错，拦截器映射 403 而非 500）: %v", err)
	}
	if ok {
		t.Error("DenyAll 必须 fail-closed 拒绝（即使 subject=admin）")
	}
}

// TestFuncAdapter：普通函数适配为 Authorizer（内联实现/测试场景）。
func TestFuncAdapter(t *testing.T) {
	calls := 0
	f := Func(func(ctx context.Context, subject, object, action string) (bool, error) {
		calls++
		return subject == "bob", nil
	})

	var _ Authorizer = f // 编译期断言接口满足

	if ok, _ := f.Authorize(context.Background(), "bob", "x", "y"); !ok {
		t.Error("subject=bob 应放行")
	}
	if ok, _ := f.Authorize(context.Background(), "eve", "x", "y"); ok {
		t.Error("subject=eve 应拒绝")
	}
	if calls != 2 {
		t.Errorf("适配器应直通底层函数（无缓存/短路）, calls = %d, want 2", calls)
	}
}
