package store

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/authn"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/stretchr/testify/assert"
)

func TestMergeTenant(t *testing.T) {
	// 重置全局注册表状态（测试隔离）：仅验证默认 tenant_id 维度。
	ctx := contextx.WithTenantID(context.Background(), "t-42")

	w := &Where{}
	mergeTenant(w, ctx)
	assert.Len(t, w.Filters, 1)
	assert.Equal(t, "tenant_id", w.Filters[0].GetField())
	assert.Equal(t, "t-42", w.Filters[0].GetValue())

	// 业务已手写同名条件时，不重复注入。
	w2 := &Where{Filters: []*storev1.FilterCondition{Eq("tenant_id", "t-other")}}
	mergeTenant(w2, ctx)
	assert.Len(t, w2.Filters, 1, "业务已声明租户条件，不应重复注入")
}

func TestMergeDataScope(t *testing.T) {
	// 注册一个"仅本人"数据范围策略。
	RegisterDataScope(func(_ context.Context, c *authn.AuthClaims) []*storev1.FilterCondition {
		if c == nil {
			return nil
		}
		return []*storev1.FilterCondition{Eq("owner", c.Subject)}
	})

	ctx := authn.ContextWithAuthClaims(context.Background(), &authn.AuthClaims{Subject: "u-1", TenantID: "t-9"})
	w := &Where{}
	mergeTenant(w, ctx)
	mergeDataScope(w, ctx)

	// 租户隔离 + 数据范围两条条件都注入。
	fields := map[string]string{}
	for _, f := range w.Filters {
		fields[f.GetField()] = f.GetValue()
	}
	assert.Equal(t, "t-9", fields["tenant_id"])
	assert.Equal(t, "u-1", fields["owner"])
}
