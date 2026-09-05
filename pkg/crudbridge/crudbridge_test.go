package crudbridge_test

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald-crud/viewer"
	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/crudbridge"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimpleViewer_ViewMutualExclusion(t *testing.T) {
	platform := &crudbridge.SimpleViewer{}
	assert.True(t, platform.IsPlatformContext())
	assert.False(t, platform.IsTenantContext())
	assert.False(t, platform.IsSystemContext())

	tenant := &crudbridge.SimpleViewer{TenantIDValue: 1001}
	assert.True(t, tenant.IsTenantContext())
	assert.False(t, tenant.IsPlatformContext())
	assert.False(t, tenant.IsSystemContext())

	system := &crudbridge.SimpleViewer{TenantIDValue: 1001, System: true}
	assert.True(t, system.IsSystemContext())
	assert.False(t, system.IsTenantContext(), "系统视图不得同时是租户视图，否则租户强制语义被绕过")
	assert.False(t, system.IsPlatformContext())
}

func TestSimpleViewer_HasPermission(t *testing.T) {
	v := &crudbridge.SimpleViewer{PermsValue: []string{"update:user", "read:order"}}
	assert.True(t, v.HasPermission("update", "user"))
	assert.True(t, v.HasPermission("read", "order"))
	assert.False(t, v.HasPermission("delete", "user"))
	assert.False(t, v.HasPermission("update", "order"))
	assert.False(t, (&crudbridge.SimpleViewer{}).HasPermission("read", "user"))
}

func TestViewerFromContext(t *testing.T) {
	t.Run("空上下文退回 noop（三视图全 false）", func(t *testing.T) {
		v := crudbridge.ViewerFromContext(context.Background())
		assert.False(t, v.IsPlatformContext())
		assert.False(t, v.IsTenantContext())
		assert.False(t, v.IsSystemContext())
	})

	t.Run("数字身份映射为租户视图", func(t *testing.T) {
		ctx := context.Background()
		ctx = contextx.WithUserID(ctx, "7")
		ctx = contextx.WithTenantID(ctx, "1001")
		ctx = contextx.WithTraceID(ctx, "trace-1")

		v := crudbridge.ViewerFromContext(ctx)
		assert.Equal(t, uint64(7), v.UserID())
		assert.Equal(t, uint64(1001), v.TenantID())
		assert.Equal(t, "trace-1", v.TraceID())
		assert.True(t, v.IsTenantContext())
	})

	t.Run("非数字租户按平台视图处理（行为锁定，见包注释警告）", func(t *testing.T) {
		ctx := contextx.WithTenantID(context.Background(), "acme")
		v := crudbridge.ViewerFromContext(ctx)
		assert.Equal(t, uint64(0), v.TenantID())
		assert.True(t, v.IsPlatformContext())
	})
}

func TestInjectViewerFromContext_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = contextx.WithUserID(ctx, "7")
	ctx = contextx.WithTenantID(ctx, "1001")

	injected := crudbridge.InjectViewerFromContext(ctx)
	vc, ok := viewer.FromContext(injected)
	require.True(t, ok)
	assert.Equal(t, uint64(1001), vc.TenantID())
}

func TestViewerFromIdentity(t *testing.T) {
	t.Run("平铺字段全空 → noop", func(t *testing.T) {
		v := crudbridge.ViewerFromIdentity("", "", "", nil, nil)
		assert.False(t, v.IsPlatformContext())
		assert.False(t, v.IsTenantContext())
	})

	t.Run("身份 + 权限/角色完整流转", func(t *testing.T) {
		v := crudbridge.ViewerFromIdentity("7", "1001", "trace-9",
			[]string{"read:user", "update:user"}, []string{"admin"})
		assert.Equal(t, uint64(7), v.UserID())
		assert.Equal(t, uint64(1001), v.TenantID())
		assert.Equal(t, "trace-9", v.TraceID())
		assert.Equal(t, []string{"admin"}, v.Roles())
		assert.True(t, v.HasPermission("read", "user"), "scopes(user:read 格式) 应直接对上 HasPermission")
		assert.False(t, v.HasPermission("delete", "user"))
		assert.True(t, v.IsTenantContext())
	})

	t.Run("仅携带权限（系统后台带身份的定时任务）→ 平台视图", func(t *testing.T) {
		v := crudbridge.ViewerFromIdentity("", "", "trace-9", []string{"read:report"}, nil)
		assert.True(t, v.IsPlatformContext())
		assert.True(t, v.HasPermission("read", "report"))
	})
}

func TestEnforceTenant_Integration(t *testing.T) {
	t.Run("缺 viewer → fail-closed", func(t *testing.T) {
		_, err := viewer.EnforceTenant(context.Background())
		require.Error(t, err)
		assert.True(t, errors.Is(err, viewer.ErrMissingViewer))
	})

	t.Run("桥接后的租户视图 → 强制注入", func(t *testing.T) {
		ctx := crudbridge.InjectViewerFromContext(contextx.WithTenantID(context.Background(), "1001"))
		dec, err := viewer.EnforceTenant(ctx)
		require.NoError(t, err)
		assert.True(t, dec.Enforce)
		assert.Equal(t, uint64(1001), dec.TenantID)
	})

	t.Run("显式系统视图 → pass-through", func(t *testing.T) {
		ctx := crudbridge.InjectViewer(context.Background(), &crudbridge.SimpleViewer{System: true})
		dec, err := viewer.EnforceTenant(ctx)
		require.NoError(t, err)
		assert.False(t, dec.Enforce)
	})
}
