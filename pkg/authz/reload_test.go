package authz

import (
	"context"
	"errors"
	"testing"
)

// ---------------------------------------------------------------------------
// D7：可选重载能力（authz.ReloadableAuthorizer + Reload 助手）
//
// 缺陷：Authorizer 仅单方法 Authorize，策略在构造期一次性装载，运行期变更
// （新注册用户）须重启才生效（端到端实测：注册后立即访问受保护资源 → 403）。
// 修法：新增**可选**扩展接口（不动 Authorizer 本身），能力发现走类型断言。
// ---------------------------------------------------------------------------

// reloadableStub 是支持重载的桩，记录 Reload 次数并可注入错误。
type reloadableStub struct {
	allow  bool
	calls  int
	err    error
	after  bool // Reload 后 allow 切换为 true
	closed bool
}

func (s *reloadableStub) Authorize(context.Context, string, string, string) (bool, error) {
	return s.allow, nil
}

func (s *reloadableStub) Reload() error {
	s.calls++
	if s.err != nil {
		return s.err
	}
	if s.after {
		s.allow = true
	}
	return nil
}

// TestReloadableAuthorizer_DoesNotWidenAuthorizer Authorizer 保持单方法契约：
// 普通实现（Func/AllowAll/DenyAll）**不**被迫实现 Reload，且不被误判为可重载。
func TestReloadableAuthorizer_DoesNotWidenAuthorizer(t *testing.T) {
	for name, a := range map[string]Authorizer{
		"AllowAll": AllowAll(),
		"DenyAll":  DenyAll(),
		"Func":     Func(func(context.Context, string, string, string) (bool, error) { return true, nil }),
	} {
		if _, ok := a.(ReloadableAuthorizer); ok {
			t.Errorf("%s 不应被判定为 ReloadableAuthorizer（Authorizer 契约不应被加宽）", name)
		}
		// 助手对不支持者返回 (false, nil) —— 正常情形而非错误。
		supported, err := Reload(a)
		if supported || err != nil {
			t.Errorf("Reload(%s) = (%v, %v), want (false, nil)", name, supported, err)
		}
	}
}

// TestReload_Supported 支持重载的实现：助手转发并返回其执行结果。
func TestReload_Supported(t *testing.T) {
	s := &reloadableStub{allow: false}
	supported, err := Reload(s)
	if !supported {
		t.Fatal("应判定为支持重载")
	}
	if err != nil {
		t.Fatalf("Reload 不应报错: %v", err)
	}
	if s.calls != 1 {
		t.Fatalf("Reload 调用次数 = %d, want 1", s.calls)
	}
}

// TestReload_ErrorPropagated 重载失败的错误必须透传（调用方需据此记录/重试）。
func TestReload_ErrorPropagated(t *testing.T) {
	sentinel := errors.New("policy source unavailable")
	s := &reloadableStub{err: sentinel}
	supported, err := Reload(s)
	if !supported {
		t.Fatal("应判定为支持重载")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误应透传，got %v", err)
	}
}
