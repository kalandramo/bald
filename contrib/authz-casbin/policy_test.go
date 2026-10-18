package authzcasbin

import (
	"context"
	"strings"
	"testing"
)

// TestNewRows_LoadAndEnforce 策略装载语义：行数据进引擎，g 行绑定决定放行面
// （p 行只有声明权——无 g 绑定的 subject 恒拒绝，见 TestNewRows_GrantRequired）。
func TestNewRows_LoadAndEnforce(t *testing.T) {
	rows := []PolicyRow{
		{Role: "admin", Object: "greet", Action: "get"},
		{Role: "admin", Object: "greet", Action: "post"},
		{Role: "viewer", Object: "greet", Action: "get"},
	}
	grants := []Grant{
		{Subject: "u-admin", Role: "admin"},
		{Subject: "u-viewer", Role: "viewer"},
	}
	az, err := NewRows(rows, grants)
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	ctx := context.Background()
	cases := []struct {
		name          string
		sub, obj, act string
		want          bool
	}{
		{"admin post greet", "u-admin", "greet", "post", true},
		{"admin get greet", "u-admin", "greet", "get", true},
		{"viewer get greet", "u-viewer", "greet", "get", true},
		{"viewer post greet denied", "u-viewer", "greet", "post", false},
		{"unbound subject denied", "u-mallory", "greet", "get", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := az.Authorize(ctx, c.sub, c.obj, c.act)
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if got != c.want {
				t.Fatalf("Authorize(%q,%q,%q)=%v want %v", c.sub, c.obj, c.act, got, c.want)
			}
		})
	}
}

// TestNewRows_GrantRequired 只有 p 行没有 g 行：matcher g(r.sub, p.sub) 无从
// 命中——恒拒绝（默认拒绝语义的机械保证）。
func TestNewRows_GrantRequired(t *testing.T) {
	az, err := NewRows([]PolicyRow{{Role: "admin", Object: "greet", Action: "get"}}, nil)
	if err != nil {
		t.Fatalf("NewRows: %v", err)
	}
	got, err := az.Authorize(context.Background(), "u-admin", "greet", "get")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if got {
		t.Fatal("p rows without grants must deny every subject")
	}
}

// TestNewRows_EmptySet 空集（nil 与空切片）是合法输入：构造成功 + fail-closed。
func TestNewRows_EmptySet(t *testing.T) {
	for name, rows := range map[string][]PolicyRow{"nil": nil, "empty": {}} {
		az, err := NewRows(rows, nil)
		if err != nil {
			t.Fatalf("%s: NewRows: %v", name, err)
		}
		got, err := az.Authorize(context.Background(), "u-admin", "greet", "get")
		if err != nil {
			t.Fatalf("%s: Authorize: %v", name, err)
		}
		if got {
			t.Fatalf("%s: empty policy set must deny (fail-closed)", name)
		}
	}
}

// TestNewRows_DuplicateRows 重复行幂等：同一权限行装载两次与一次语义相同
// （对账面可能重复产出，装载层不放大权限）。
func TestNewRows_DuplicateRows(t *testing.T) {
	row := PolicyRow{Role: "admin", Object: "greet", Action: "get"}
	once, err := NewRows([]PolicyRow{row}, []Grant{{Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows once: %v", err)
	}
	twice, err := NewRows([]PolicyRow{row, row}, []Grant{{Subject: "u-admin", Role: "admin"}, {Subject: "u-admin", Role: "admin"}})
	if err != nil {
		t.Fatalf("NewRows twice: %v", err)
	}
	ctx := context.Background()
	for name, az := range map[string]*Authorizer{"once": once, "twice": twice} {
		got, err := az.Authorize(ctx, "u-admin", "greet", "get")
		if err != nil || !got {
			t.Fatalf("%s: Authorize=%v err=%v, want true/nil", name, got, err)
		}
		got, err = az.Authorize(ctx, "u-admin", "greet", "delete")
		if err != nil || got {
			t.Fatalf("%s: ungranted action must stay denied, got %v err %v", name, got, err)
		}
	}
}

// TestNewRows_RejectsFieldInjection 字段含逗号/换行：csv 行注入面，构造期拒绝。
func TestNewRows_RejectsFieldInjection(t *testing.T) {
	cases := map[string][]PolicyRow{
		"comma in role":  {{Role: "admin, viewer", Object: "greet", Action: "get"}},
		"newline in obj": {{Role: "admin", Object: "greet\nother", Action: "get"}},
		"cr in action":   {{Role: "admin", Object: "greet", Action: "get\r"}},
	}
	for name, rows := range cases {
		if _, err := NewRows(rows, nil); err == nil {
			t.Errorf("%s: must reject, got nil error", name)
		} else if !strings.Contains(err.Error(), "comma or newline") {
			t.Errorf("%s: error must name the injection, got %v", name, err)
		}
	}
	if _, err := NewRows(nil, []Grant{{Subject: "u,g", Role: "admin"}}); err == nil {
		t.Error("grant with comma must be rejected")
	}
}
