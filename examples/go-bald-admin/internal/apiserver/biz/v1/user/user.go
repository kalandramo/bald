// Package user 是用户管理业务（T2，自 go-wind-admin identity/user 域精简移植）。
//
// 租户内读写：读路径 Where.T(ctx) 自动注入 tenant_id 过滤（跨租户不可见），
// 写路径 injectWriteTenant 自动写入/覆写 ctx 租户（防越权改归属）——业务零手写。
package user

import (
	"context"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/store"
)

// Biz 用户管理业务。仓储经 store() 请求期读取（wire 构造期 bootstrap.UserStore
// 尚未初始化——构造期快照会把 nil 固化进来，见 SecretBiz 同款时序约定）。
type Biz struct{}

// New 构造用户业务。
func New() *Biz { return &Biz{} }

func (b *Biz) store() *store.Store[authmodel.User] { return bootstrappkg.UserStore }

// Get 取用户（租户内）。跨租户/不存在均 ErrNotFound（隔离由 Where.T 完成）。
func (b *Biz) Get(ctx context.Context, id string) (*authmodel.User, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	u, err := b.store().Get(ctx, w.T(ctx))
	if err != nil {
		return nil, fmt.Errorf("user.Get(%s): %w", id, err)
	}
	return u, nil
}

// List 列出当前租户用户。
func (b *Biz) List(ctx context.Context) ([]*authmodel.User, error) {
	users, _, err := b.store().List(ctx, (&store.Where{}).T(ctx))
	if err != nil {
		return nil, fmt.Errorf("user.List: %w", err)
	}
	return users, nil
}

// Create 创建用户（自动归属 ctx 租户）。密码 bcrypt 存储，不回显。
func (b *Biz) Create(ctx context.Context, id, username string, roles []string, password string) (*authmodel.User, error) {
	if id == "" || username == "" || password == "" {
		return nil, fmt.Errorf("user.Create: id, username and password are required")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return nil, fmt.Errorf("user.Create(%s): %w", id, err)
	}
	u := &authmodel.User{
		ID:           id,
		Username:     username,
		PasswordHash: string(hash),
		Roles:        joinCSV(roles),
		// TenantID 不手写：injectWriteTenant 从 ctx 自动注入（租户内创建）。
	}
	if err := b.store().Create(ctx, u); err != nil {
		return nil, fmt.Errorf("user.Create(%s): %w", id, err)
	}
	return u, nil
}

// Update 更新用户（租户内）。nil roles=不改；空 roles=清空；空密码=不改。
func (b *Biz) Update(ctx context.Context, id, username string, roles []string, password string) (*authmodel.User, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	u, err := b.store().Get(ctx, w.T(ctx))
	if err != nil {
		return nil, fmt.Errorf("user.Update(%s): %w", id, err)
	}
	if username != "" {
		u.Username = username
	}
	if roles != nil {
		u.Roles = joinCSV(roles)
	}
	if password != "" {
		hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return nil, fmt.Errorf("user.Update(%s): %w", id, err)
		}
		u.PasswordHash = string(hash)
	}
	if err := b.store().Update(ctx, u); err != nil {
		return nil, fmt.Errorf("user.Update(%s): %w", id, err)
	}
	return u, nil
}

// Delete 删除用户（租户内）。仅当存在才删；返回是否真正删除。
func (b *Biz) Delete(ctx context.Context, id string) (bool, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	if _, err := b.store().Get(ctx, w.T(ctx)); err != nil {
		return false, fmt.Errorf("user.Delete(%s): %w", id, err)
	}
	if err := b.store().Delete(ctx, w.T(ctx)); err != nil {
		return false, fmt.Errorf("user.Delete(%s): %w", id, err)
	}
	return true, nil
}

func joinCSV(ss []string) string { return strings.Join(ss, ",") }
