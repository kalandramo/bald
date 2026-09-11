// Package tenant 是租户管理业务（T2，自 go-wind-admin identity 域精简移植）。
//
// 平台侧管理面：本表（model.Tenant）无 TenantID 字段，查询不调 Where.T——
// P8 隔离对租户管理面天然不生效，全量读写即平台语义（源 PlatformTenantID=0 等价物）。
// 授权约束（仅 platform:admin 可操作）在中间件层经 casbin 策略完成，biz 不感知角色。
package tenant

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/berrors"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/store"
)

// Biz 租户管理业务。仓储经 store() 请求期读取（wire 构造期 bootstrap.TenantStore
// 尚未初始化，构造期快照会把 nil 固化进来——见 SecretBiz 同款时序约定）。
type Biz struct{}

// New 构造租户业务。
func New() *Biz { return &Biz{} }

func (b *Biz) store() *store.Store[authmodel.Tenant] { return bootstrappkg.TenantStore }

// Get 取租户（按编码）。不存在返回 ErrNotFound。
func (b *Biz) Get(ctx context.Context, id string) (*authmodel.Tenant, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	t, err := b.store().Get(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("tenant.Get(%s): %w", id, err)
	}
	return t, nil
}

// List 全量列出租户（平台语义，不分页——量级小，分页列后续迭代）。
func (b *Biz) List(ctx context.Context) ([]*authmodel.Tenant, error) {
	ts, _, err := b.store().List(ctx, &store.Where{})
	if err != nil {
		return nil, fmt.Errorf("tenant.List: %w", err)
	}
	return ts, nil
}

// Create 创建租户。编码即主键（客户端指定），冲突返回 ErrConflict。
func (b *Biz) Create(ctx context.Context, id, name, remark string) (*authmodel.Tenant, error) {
	if id == "" || name == "" {
		return nil, berrors.BadRequest("tenant/missing_required_fields").
			WithMessage("tenant.Create: id and name are required")
	}
	t := &authmodel.Tenant{ID: id, Name: name, Status: "ON", Remark: remark}
	if err := b.store().Create(ctx, t); err != nil {
		return nil, fmt.Errorf("tenant.Create(%s): %w", id, err)
	}
	return t, nil
}

// Update 更新租户。空值/UNSPECIFIED 字段不改（源 UpdateTenantRequest 语义）。
// status 合法值："" 不改、"ON"、"OFF"、"FREEZE"。
func (b *Biz) Update(ctx context.Context, id, name, status, remark string) (*authmodel.Tenant, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	t, err := b.store().Get(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("tenant.Update(%s): %w", id, err)
	}
	if name != "" {
		t.Name = name
	}
	if status != "" {
		switch status {
		case "ON", "OFF", "FREEZE":
			t.Status = status
		default:
			return nil, berrors.BadRequest("tenant/invalid_status").
				WithMessage("tenant.Update(%s): invalid status %q", id, status)
		}
	}
	if remark != "" {
		t.Remark = remark
	}
	if err := b.store().Update(ctx, t); err != nil {
		return nil, fmt.Errorf("tenant.Update(%s): %w", id, err)
	}
	return t, nil
}

// Delete 删除租户。仅当存在才删；返回是否真正删除。
// 平台租户（platform）为保护对象，拒绝删除。
func (b *Biz) Delete(ctx context.Context, id string) (bool, error) {
	if id == "platform" {
		return false, berrors.FailedPrecondition("tenant/platform_protected").
			WithMessage("tenant.Delete: platform tenant is protected")
	}
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	if _, err := b.store().Get(ctx, w); err != nil {
		return false, fmt.Errorf("tenant.Delete(%s): %w", id, err)
	}
	if err := b.store().Delete(ctx, w); err != nil {
		return false, fmt.Errorf("tenant.Delete(%s): %w", id, err)
	}
	return true, nil
}
