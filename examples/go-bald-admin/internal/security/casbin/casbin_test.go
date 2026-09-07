package casbin

// casbin_test.go T3 验收点「无策略时拒绝默认生效」（fail-closed）：
// 空 csv 构造的授权器对任何 subject/object/action 一律拒绝——
// 授权不因缺数据而放开（DB 策略清空 + 重启 = 全 403）。
//
// DB 数据化装载的正向行为（p 行放行 / g 行 subject→角色）由
// apiserver 包 e2e 覆盖（TestT3Authz_DataDrivenPolicy）。

import (
	"context"
	"testing"
)

func TestEmptyPolicy_FailClosed(t *testing.T) {
	az, err := New("")
	if err != nil {
		t.Fatalf("New(empty csv): %v", err)
	}
	ctx := context.Background()
	for _, tc := range []struct{ subject, object, action string }{
		{"admin", "secret", "get"},
		{"admin", "tenant", "delete"},
		{"u-admin", "menu", "write"}, // g 行也没有：subject 未经绑定
	} {
		allow, err := az.Authorize(ctx, tc.subject, tc.object, tc.action)
		if err != nil {
			t.Fatalf("Authorize(%v): %v", tc, err)
		}
		if allow {
			t.Fatalf("empty policy must deny %s/%s/%s (fail-closed)", tc.subject, tc.object, tc.action)
		}
	}
}
