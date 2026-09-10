// Package permission 是权限管理业务（T3，自 go-wind-admin permission 域精简移植）。
//
// 两块职责（与 proto 对应）：
//  1. 权限点注册表（model.Permission）：权限码 → 名称/菜单可见性；
//  2. 角色策略（model.RolePolicy）：casbin p 行的数据化持久层（D3 策略数据化）。
//
// 平台侧管理面（无 TenantID 字段，同 Tenant/Menu Biz）；授权在中间件层完成。
package permission

import (
	"context"
	"errors"
	"fmt"

	berrors "github.com/kalandramo/bald/berrors"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/store"
)

// Biz 权限管理业务。仓储请求期读取（时序约定同 Tenant/Menu Biz）。
type Biz struct{}

// New 构造权限业务。
func New() *Biz { return &Biz{} }

func (b *Biz) permStore() *store.Store[authmodel.Permission] { return bootstrappkg.PermissionStore }

func (b *Biz) policyStore() *store.Store[authmodel.RolePolicy] {
	return bootstrappkg.RolePolicyStore
}

// ---- 权限点注册表 ----

// GetPermission 取权限点。
func (b *Biz) GetPermission(ctx context.Context, code string) (*authmodel.Permission, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", code))
	p, err := b.permStore().Get(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("permission.Get(%s): %w", code, err)
	}
	return p, nil
}

// ListPermissions 全量列出权限点。
func (b *Biz) ListPermissions(ctx context.Context) ([]*authmodel.Permission, error) {
	ps, _, err := b.permStore().List(ctx, &store.Where{})
	if err != nil {
		return nil, fmt.Errorf("permission.List: %w", err)
	}
	return ps, nil
}

// CreatePermission 创建权限点（ID 即权限码）。
func (b *Biz) CreatePermission(ctx context.Context, code, name string, menuIDs []string, remark string) (*authmodel.Permission, error) {
	if code == "" {
		return nil, berrors.BadRequest("permission/missing_required_fields").
			WithMessage("permission.Create: code is required")
	}
	p := &authmodel.Permission{ID: code, Name: name, MenuIDs: joinCSV(menuIDs), Remark: remark}
	if err := b.permStore().Create(ctx, p); err != nil {
		return nil, fmt.Errorf("permission.Create(%s): %w", code, err)
	}
	return p, nil
}

// UpdatePermission 更新权限点。name/remark 空不改；menuIDs 仅 menuIDsSet 为 true
// 时才改（空数组是合法值 = 清空关联）。
func (b *Biz) UpdatePermission(ctx context.Context, code, name string, menuIDs []string, menuIDsSet bool, remark string) (*authmodel.Permission, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", code))
	p, err := b.permStore().Get(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("permission.Update(%s): %w", code, err)
	}
	if name != "" {
		p.Name = name
	}
	if menuIDsSet {
		p.MenuIDs = joinCSV(menuIDs)
	}
	if remark != "" {
		p.Remark = remark
	}
	if err := b.permStore().Update(ctx, p); err != nil {
		return nil, fmt.Errorf("permission.Update(%s): %w", code, err)
	}
	return p, nil
}

// DeletePermission 删除权限点。仅删元数据，不联动 Role.Perms/RolePolicy
// （权限码不存在时 casbin 无对应 p 行，天然无授权效果）。
// 返回 (false, nil) 表示权限点不存在（与系统错误区分，handler 映射 404）。
func (b *Biz) DeletePermission(ctx context.Context, code string) (bool, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", code))
	if _, err := b.permStore().Get(ctx, w); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("permission.Delete(%s): %w", code, err)
	}
	if err := b.permStore().Delete(ctx, w); err != nil {
		return false, fmt.Errorf("permission.Delete(%s): %w", code, err)
	}
	return true, nil
}

// ---- 角色策略（casbin p 行数据化） ----

// ListRolePolicies 列出策略行（role 空 = 全量）。
func (b *Biz) ListRolePolicies(ctx context.Context, role string) ([]*authmodel.RolePolicy, error) {
	w := &store.Where{}
	if role != "" {
		w.Filters = append(w.Filters, store.Eq("role", role))
	}
	ps, _, err := b.policyStore().List(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("permission.ListRolePolicies(%s): %w", role, err)
	}
	return ps, nil
}

// CreateRolePolicy 新增策略行（重启后经数据化装载生效；本子集不做运行期热重载）。
// ID 即业务键 role:object:action——重复策略 Create 冲突（ErrConflict）天然防重。
func (b *Biz) CreateRolePolicy(ctx context.Context, role, object, action string) (*authmodel.RolePolicy, error) {
	if role == "" || object == "" || action == "" {
		return nil, berrors.BadRequest("permission/missing_required_fields").
			WithMessage("permission.CreateRolePolicy: role/object/action are required")
	}
	p := &authmodel.RolePolicy{ID: role + ":" + object + ":" + action, Role: role, Object: object, Action: action}
	if err := b.policyStore().Create(ctx, p); err != nil {
		return nil, fmt.Errorf("permission.CreateRolePolicy(%s,%s,%s): %w", role, object, action, err)
	}
	return p, nil
}

// DeleteRolePolicy 删除策略行。返回 (false, nil) 表示策略行不存在（→ 404）。
func (b *Biz) DeleteRolePolicy(ctx context.Context, id string) (bool, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	if _, err := b.policyStore().Get(ctx, w); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, fmt.Errorf("permission.DeleteRolePolicy(%s): %w", id, err)
	}
	if err := b.policyStore().Delete(ctx, w); err != nil {
		return false, fmt.Errorf("permission.DeleteRolePolicy(%s): %w", id, err)
	}
	return true, nil
}

// joinCSV 序列化 MenuIDs（与 model.splitCSV 逆操作）。
func joinCSV(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
