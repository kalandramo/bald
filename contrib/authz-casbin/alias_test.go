package authzcasbin

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald/pkg/authz"
)

// aliasFixture 构造「策略行走 gRPC 命名空间 + 别名表 greet→greets」的桥
// （gen 产物方向：p 行 object = DefaultGRPCObject，别名表 gRPC 名 → HTTP 名）。
func aliasFixture(t *testing.T, aliases map[string]string) authz.Authorizer {
	t.Helper()
	rows := []PolicyRow{
		{Role: "admin", Object: "greet", Action: "get"},
		{Role: "admin", Object: "greet", Action: "post"},
		{Role: "viewer", Object: "greet", Action: "get"},
	}
	inner, err := NewRows(rows, []Grant{
		{Subject: "u-admin", Role: "admin"},
		{Subject: "u-viewer", Role: "viewer"},
	})
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	return NewAlias(inner, aliases)
}

// TestAlias_ReverseLookup 生成物方向（策略行走 gRPC 命名）：HTTP 面请求
// （object=greets）内层未命中 → 反查等价键 greet → 内层放行。
func TestAlias_ReverseLookup(t *testing.T) {
	az := aliasFixture(t, map[string]string{"greet": "greets"})
	ctx := context.Background()
	if got, err := az.Authorize(ctx, "u-admin", "greets", "post"); err != nil || !got {
		t.Fatalf("admin POST /v1/greets: got=%v err=%v, want true/nil", got, err)
	}
	if got, err := az.Authorize(ctx, "u-viewer", "greets", "get"); err != nil || !got {
		t.Fatalf("viewer GET /v1/greets: got=%v err=%v, want true/nil", got, err)
	}
	// 别名命中仍需内层放行：viewer 的 post 两个名字都无权 → 保持拒绝。
	if got, err := az.Authorize(ctx, "u-viewer", "greets", "post"); err != nil || got {
		t.Fatalf("viewer POST /v1/greets: got=%v err=%v, want false/nil", got, err)
	}
}

// TestAlias_ForwardLookup bald-admin 方向（策略行走 HTTP 命名）：gRPC 面请求
// （object=greet）内层未命中 → 正查等价名 greets → 内层放行。
func TestAlias_ForwardLookup(t *testing.T) {
	rows := []PolicyRow{{Role: "admin", Object: "greets", Action: "get"}}
	inner, err := NewRows(rows, []Grant{{Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	az := NewAlias(inner, map[string]string{"greet": "greets"})
	got, err := az.Authorize(context.Background(), "u-admin", "greet", "get")
	if err != nil || !got {
		t.Fatalf("grpc-name request via forward alias: got=%v err=%v, want true/nil", got, err)
	}
}

// TestAlias_NoAliasDenyPreserved 无等价名的 object：拒绝语义原样保留
// （别名表只桥接业务声明过的等价关系，不做模糊匹配）。
func TestAlias_NoAliasDenyPreserved(t *testing.T) {
	az := aliasFixture(t, map[string]string{"greet": "greets"})
	got, err := az.Authorize(context.Background(), "u-admin", "unrelated", "get")
	if err != nil || got {
		t.Fatalf("object without equivalence must deny: got=%v err=%v", got, err)
	}
}

// TestAlias_AliasTargetNeedsPolicy 等价名也必须命中策略行：别名是换名再问，
// 不是放行通道。
func TestAlias_AliasTargetNeedsPolicy(t *testing.T) {
	rows := []PolicyRow{{Role: "admin", Object: "greet", Action: "get"}}
	inner, err := NewRows(rows, []Grant{{Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	// greet → greets，但策略只有 greet：greets 侧请求两个名字都无权。
	az := NewAlias(inner, map[string]string{"greets": "greet"})
	got, err := az.Authorize(context.Background(), "u-admin", "greets", "delete")
	if err != nil || got {
		t.Fatalf("ungranted action must stay denied via alias retry: got=%v err=%v", got, err)
	}
}

// countingAuthz 记录调用轨迹的桩（调用次数与错误注入语义用）。
type countingAuthz struct {
	calls int
	log   [][3]string
	err   error
	allow func(sub, obj, act string) bool
}

func (c *countingAuthz) Authorize(_ context.Context, sub, obj, act string) (bool, error) {
	c.calls++
	c.log = append(c.log, [3]string{sub, obj, act})
	if c.err != nil {
		return false, c.err
	}
	return c.allow(sub, obj, act), nil
}

// TestAlias_InnerErrorNotRetried 内层引擎错误原样透传且不重试（故障不能被
// 误判成命名不匹配，更不能被别名路径吞掉）。
func TestAlias_InnerErrorNotRetried(t *testing.T) {
	boom := errors.New("engine down")
	inner := &countingAuthz{err: boom}
	az := NewAlias(inner, map[string]string{"greet": "greets"})
	got, err := az.Authorize(context.Background(), "u-admin", "greet", "get")
	if !errors.Is(err, boom) {
		t.Fatalf("inner error must pass through, got %v", err)
	}
	if got {
		t.Fatal("error path must not allow")
	}
	if inner.calls != 1 {
		t.Fatalf("inner must be called exactly once on error, got %d", inner.calls)
	}
}

// TestAlias_AllowShortCircuits 内层放行不再触发别名重试（单次调用）。
func TestAlias_AllowShortCircuits(t *testing.T) {
	inner := &countingAuthz{allow: func(string, string, string) bool { return true }}
	az := NewAlias(inner, map[string]string{"greet": "greets"})
	if got, err := az.Authorize(context.Background(), "u-admin", "greet", "get"); err != nil || !got {
		t.Fatalf("Authorize=%v err=%v, want true/nil", got, err)
	}
	if inner.calls != 1 {
		t.Fatalf("allowed request must not retry, got %d inner calls", inner.calls)
	}
}

// TestAlias_NilAliasesDelegates 空/nil 别名表 = 纯委托（语义等价内层）。
func TestAlias_NilAliasesDelegates(t *testing.T) {
	for name, m := range map[string]map[string]string{"nil": nil, "empty": {}} {
		inner := &countingAuthz{allow: func(string, string, string) bool { return false }}
		az := NewAlias(inner, m)
		got, err := az.Authorize(context.Background(), "u-admin", "greet", "get")
		if err != nil || got {
			t.Fatalf("%s: delegate deny: got=%v err=%v", name, got, err)
		}
		if inner.calls != 1 {
			t.Fatalf("%s: exactly one inner call, got %d", name, inner.calls)
		}
	}
}

// TestAlias_CollisionUnion 反向键冲突的等价并集：两个 gRPC 名映射同一 HTTP
// 名时，候选按键序逐一尝试，任一命中即放行（确定性：a 先于 b）。
func TestAlias_CollisionUnion(t *testing.T) {
	rows := []PolicyRow{{Role: "admin", Object: "greetb", Action: "get"}}
	inner, err := NewRows(rows, []Grant{{Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	az := NewAlias(inner, map[string]string{"greeta": "greets", "greetb": "greets"})
	got, err := az.Authorize(context.Background(), "u-admin", "greets", "get")
	if err != nil || !got {
		t.Fatalf("union semantics must reach greetb: got=%v err=%v", got, err)
	}
}

// TestAlias_SelfAliasSkipped 别名指向自身/空串的条目不进表（不自旋）。
func TestAlias_SelfAliasSkipped(t *testing.T) {
	rows := []PolicyRow{{Role: "admin", Object: "greet", Action: "get"}}
	inner, err := NewRows(rows, []Grant{{Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	az := NewAlias(inner, map[string]string{"greet": "greet", "loop": "", "greet2": "greet"})
	// greet2 的自旋条目被剔除后，"other" 无任何候选 → 一次内层拒绝即终局。
	got, err := az.Authorize(context.Background(), "u-admin", "other", "get")
	if err != nil || got {
		t.Fatalf("self aliases must be skipped: got=%v err=%v", got, err)
	}
}

// TestAlias_ReloadForwarding 能力转发：内层可重载时装饰器必须转发 Reload
// （包一层别名装饰器不得让 authz.Reload 静默失效）；不可重载时合法无操作。
func TestAlias_ReloadForwarding(t *testing.T) {
	rows := []PolicyRow{{Role: "admin", Object: "greet", Action: "get"}}
	casbinAz, err := NewRows(rows, []Grant{{Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	// casbin 桥（字符串适配器）实现 ReloadableAuthorizer：装饰器转发 Reload。
	az := NewAlias(casbinAz, map[string]string{"greet": "greets"})
	if err := az.Reload(); err != nil {
		t.Fatalf("Reload must forward to inner casbin authorizer: %v", err)
	}
	// 不可重载内层：合法无操作（nil 错误）。
	plain := NewAlias(authz.AllowAll(), nil)
	if err := plain.Reload(); err != nil {
		t.Fatalf("Reload on non-reloadable inner must be a no-op, got %v", err)
	}
}
