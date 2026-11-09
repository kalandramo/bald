package crudbridge

import (
	"context"
	"testing"

	"github.com/kalandramo/bald-crud/viewer"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimpleViewer_Accessors(t *testing.T) {
	v := &SimpleViewer{PermsValue: []string{"read:user"}, Auditable: true}
	assert.Equal(t, []string{"read:user"}, v.Permissions())
	assert.True(t, v.ShouldAudit())
	assert.False(t, (&SimpleViewer{}).ShouldAudit())
}

func TestInjectViewerFromIdentity(t *testing.T) {
	ctx := InjectViewerFromIdentity(context.Background(), "7", "1001", "trace-1",
		[]string{"read:user"}, []string{"admin"}, false)
	vc, ok := viewer.FromContext(ctx)
	require.True(t, ok)
	assert.Equal(t, uint64(7), vc.UserID())
	assert.Equal(t, "1001", vc.TenantID())
	assert.Equal(t, "trace-1", vc.TraceID())
	assert.True(t, vc.HasPermission("read", "user"))
}

func TestViewerFromContext_Nil(t *testing.T) {
	v := ViewerFromContext(nil)
	require.NotNil(t, v)
	assert.False(t, v.IsPlatformContext())
	assert.False(t, v.IsTenantContext())
	assert.False(t, v.IsSystemContext())
}

func TestDataScopeFields_Unit(t *testing.T) {
	assert.Equal(t, "dept_id", DataScopeFields{}.unit())
	assert.Equal(t, "org_id", DataScopeFields{Unit: "org_id"}.unit())
}

// TestScopeBranch_AllAndUnknown 直测 scopeBranch 的两条 nil 分支：
// ALL 在 DataScopeFilter 上层被短路，故只能在此直接触达；未知类型不猜语义。
func TestScopeBranch_AllAndUnknown(t *testing.T) {
	vc := &SimpleViewer{UserIDValue: 7}
	f := DataScopeFields{}
	assert.Nil(t, scopeBranch(viewer.DataScope{ScopeType: viewer.ScopeTypeAll}, vc, f))
	assert.Nil(t, scopeBranch(viewer.DataScope{ScopeType: viewer.ScopeType("BOGUS")}, vc, f))
}
