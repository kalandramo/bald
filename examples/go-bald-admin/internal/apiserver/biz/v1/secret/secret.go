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

	rediscache "github.com/kalandramo/bald-cache-redis"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// SecretBiz Secret 业务服务。
type SecretBiz struct {
	cache *rediscache.Cache // 可选；nil/禁用时直连 store（M6.2 Cache-Aside）
}

// New 构造 SecretBiz。注意：store 依赖不在构造期快照——wire 的 InitializeBiz 在
// main 构造期执行，彼时 InitBridges 尚未赋值 bootstrappkg.SecretStore，构造期快照
// 会把 nil 固化进 Biz（Get/Delete 即 nil panic，与 Signer 时序错位同款）；仓储改由
// 请求期经 store() 读取最新值（与 auth biz 直读包级变量同范式）。
func New(cache *rediscache.Cache) *SecretBiz {
	return &SecretBiz{cache: cache}
}

// SetCache 运行期注入缓存（main.go BeforeStart 在 InitBridges 之后调用）：
// 配置驱动的 cache.redis 段（含 password/db）只流向 bootstrap.RedisCache，
// wire 的 env 通道（BALD_ADMIN_REDIS_ADDR）拿不到完整参数；构造期值拷贝又会把
// nil/禁用态固化（与 file Biz 的 SetStorage 同款时序）。nil 不覆盖（保留 env 通道）。
func (b *SecretBiz) SetCache(c *rediscache.Cache) {
	if c != nil {
		b.cache = c
	}
}

// store 请求期解析仓储（读 bootstrap 包级桥接最新值）。
func (b *SecretBiz) store() *store.Store[authmodel.Secret] { return bootstrappkg.SecretStore }

// Item 是单个 Secret 的展示结构（供 handler 序列化）。
type Item struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Content  string `json:"content"`
	TenantID string `json:"tenant_id"`
}

// Get 按 ID 取 Secret，自动受调用方 ctx 中的租户隔离约束（M3/M4 多租户）。
// M6.2 起经 Cache-Aside 读 Redis（命中返缓存，未命中加载并回填）；cache 禁用时直连 store。
func (b *SecretBiz) Get(ctx context.Context, id string) (*Item, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	tenant := contextx.TenantIDFromContext(ctx)

	loader := func(c context.Context) (string, error) {
		s, err := b.store().Get(c, w.T(c))
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
		raw, err = b.cache.Get(ctx, rediscache.Key("secret", tenant, id), loader)
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
	ss, _, err := b.store().List(ctx, (&store.Where{}).T(ctx))
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
	if _, err := b.store().Get(ctx, w); err != nil {
		return false, fmt.Errorf("secret.Delete(%s): %w", id, err)
	}
	if err := b.store().Delete(ctx, w); err != nil {
		return false, fmt.Errorf("secret.Delete(%s): %w", id, err)
	}
	if b.cache != nil {
		tenant := contextx.TenantIDFromContext(ctx)
		_ = b.cache.Delete(ctx, rediscache.Key("secret", tenant, id))
	}
	return true, nil
}
