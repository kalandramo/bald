// Package secret 实现 Secret 业务层（M6.3 起落库，消除 handler 硬编码返回）。
//
// 设计约束：业务层只依赖 bald 核心的 store.Store 抽象（依赖倒置），不直接耦合 gorm/gin/grpc；
// 查询自动经 store 的多租户过滤（Where.T(ctx) 由 pkg/store 注入 TenantID），业务不手写隔离 SQL。
package secret

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	rediscache "github.com/kalandramo/bald/examples/go-bald-admin/internal/cache/redis"
)

// SecretBiz Secret 业务服务。
type SecretBiz struct {
	store *store.Store[authmodel.Secret]
	cache *rediscache.Cache // 可选；nil/禁用时直连 store（M6.2 Cache-Aside）
}

// New 构造 SecretBiz。注意：依赖经参数注入（M6.4 将由 wire 生成装配图）。
func New(cache *rediscache.Cache) *SecretBiz {
	return &SecretBiz{store: bootstrappkg.SecretStore, cache: cache}
}

// Item 是单个 Secret 的展示结构（供 handler 序列化）。
type Item struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TenantID string `json:"tenant_id"`
}

// Get 按 ID 取 Secret，自动受调用方 ctx 中的租户隔离约束（M3/M4 多租户）。
// M6.2 起经 Cache-Aside 读 Redis（命中返缓存，未命中加载并回填）；cache 禁用时直连 store。
func (b *SecretBiz) Get(ctx context.Context, id string) (*Item, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	tenant := contextx.TenantIDFromContext(ctx)

	loader := func(c context.Context) (string, error) {
		s, err := b.store.Get(c, w.T(c))
		if err != nil {
			return "", fmt.Errorf("secret.Get(%s): %w", id, err)
		}
		buf, err := json.Marshal(s)
		if err != nil {
			return "", fmt.Errorf("secret.Get marshal: %w", err)
		}
		return string(buf), nil
	}

	var raw string
	var err error
	if b.cache != nil {
		raw, err = b.cache.Get(ctx, rediscache.SecretKey(tenant, id), loader)
	} else {
		raw, err = loader(ctx)
	}
	if err != nil {
		return nil, err
	}
	var s authmodel.Secret
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return nil, fmt.Errorf("secret.Get unmarshal: %w", err)
	}
	return &Item{ID: s.ID, Name: s.Name, Content: s.Content, TenantID: s.TenantID}, nil
}

// List 列出调用方租户下的全部 Secret。
func (b *SecretBiz) List(ctx context.Context) ([]*Item, error) {
	ss, _, err := b.store.List(ctx, (&store.Where{}).T(ctx))
	if err != nil {
		return nil, fmt.Errorf("secret.List: %w", err)
	}
	items := make([]*Item, 0, len(ss))
	for _, s := range ss {
		items = append(items, &Item{ID: s.ID, Name: s.Name, Content: s.Content, TenantID: s.TenantID})
	}
	return items, nil
}

// Delete 删除调用方租户下的指定 Secret（自动受 ctx 租户隔离约束，跨租户删除被 store 拦为 NotFound）。
// 同时清理 Cache-Aside 缓存条目；cache 禁用时忽略。返回是否实际删除（true=命中并删除）。
func (b *SecretBiz) Delete(ctx context.Context, id string) (bool, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	w = w.T(ctx)

	// 先确认存在（命中租户隔离），不存在按 NotFound 处理。
	if _, err := b.store.Get(ctx, w); err != nil {
		return false, fmt.Errorf("secret.Delete(%s): %w", id, err)
	}
	if err := b.store.Delete(ctx, w); err != nil {
		return false, fmt.Errorf("secret.Delete(%s): %w", id, err)
	}
	if b.cache != nil {
		tenant := contextx.TenantIDFromContext(ctx)
		_ = b.cache.Delete(ctx, rediscache.SecretKey(tenant, id))
	}
	return true, nil
}
