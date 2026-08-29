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
	// 多租户需显式开启：注册 tenant_id 维度（业务真实用法）。
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { RegisterTenant("tenant_id", nil) })

	ctx := contextx.WithTenantID(context.Background(), "t-42")

	w := &Where{}
	mergeTenant(w, ctx)
	assert.Len(t, w.Filters, 1)
	assert.Equal(t, "tenant_id", w.Filters[0].GetField())
	assert.Equal(t, "t-42", w.Filters[0].GetValue())

	// 业务已手写同名 EQ 条件时，不重复注入。
	w2 := &Where{Filters: []*storev1.FilterCondition{Eq("tenant_id", "t-other")}}
	mergeTenant(w2, ctx)
	assert.Len(t, w2.Filters, 1, "业务已声明租户条件，不应重复注入")

	// 业务已写该列的非 EQ 条件（如 NEQ）时，也不应再叠加系统 EQ，
	// 避免 EQ + NEQ 叠加产生恒假/歧义（Issue 5）。
	w3 := &Where{Filters: []*storev1.FilterCondition{Ne("tenant_id", "t-other")}}
	mergeTenant(w3, ctx)
	assert.Len(t, w3.Filters, 1, "业务已写该列任意 Op，系统不应再注入 EQ")
}

func TestMergeDataScope(t *testing.T) {
	// 显式开启租户维度。
	RegisterTenant("tenant_id", DefaultTenantFunc)
	t.Cleanup(func() { RegisterTenant("tenant_id", nil) })

	// 注册一个"仅本人"数据范围策略。
	RegisterDataScope(func(_ context.Context, c *authn.AuthClaims) []*storev1.FilterCondition {
		if c == nil {
			return nil
		}
		return []*storev1.FilterCondition{Eq("owner", c.Subject)}
	})
	t.Cleanup(func() { dataScopes = nil })

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
