// Package dict 是字典管理业务（T4，自 go-wind-admin dict 域 sys_dict_types/
// sys_dict_entries 精简移植）。
//
// 字典是租户级业务数据：查询自动经 store 的 P8 隔离（Where.T 注入 TenantID），
// Cache-Aside 键含租户维度（dict:entries:<tenant>:<typeCode>）防跨租户缓存泄漏。
// 写穿透失效：条目/类型写操作后 Delete 对应缓存键；Redis 故障由 rediscache 的
// 降级语义兜底（直连 loader，缓存故障不放大为业务故障）。
// 授权约束（admin 写 / viewer 读）在中间件层经 casbin 策略完成，biz 不感知角色。
package dict

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/kalandramo/bald/pkg/contextx"
	"github.com/kalandramo/bald/pkg/store"

	rediscache "github.com/kalandramo/bald-cache-redis"
	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// Biz 字典管理业务。cache 可选（nil/禁用时直连 store）；仓储经 store() 请求期
// 读取（T0 确立的时序约定：biz 引用 bootstrap 包级桥接禁止构造期快照）。
type Biz struct {
	cache *rediscache.Cache
}

// New 构造字典业务。
func New(cache *rediscache.Cache) *Biz { return &Biz{cache: cache} }

func (b *Biz) typeStore() *store.Store[authmodel.DictType]  { return bootstrappkg.DictTypeStore }
func (b *Biz) entryStore() *store.Store[authmodel.DictEntry] { return bootstrappkg.DictEntryStore }

// ---- 字典类型 ----

// ListTypes 列出调用方租户的字典类型，SortOrder 升序稳定排序。
func (b *Biz) ListTypes(ctx context.Context) ([]*authmodel.DictType, int, error) {
	ts, total, err := b.typeStore().List(ctx, (&store.Where{}).T(ctx))
	if err != nil {
		return nil, 0, fmt.Errorf("dict.ListTypes: %w", err)
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].SortOrder < ts[j].SortOrder })
	return ts, int(total), nil
}

// GetType 取字典类型。
func (b *Biz) GetType(ctx context.Context, id string) (*authmodel.DictType, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	t, err := b.typeStore().Get(ctx, w.T(ctx))
	if err != nil {
		return nil, fmt.Errorf("dict.GetType(%s): %w", id, err)
	}
	return t, nil
}

// Create 创建字典类型（默认启用）。ID 即类型编码，租户内唯一（同编码重复返回冲突）。
func (b *Biz) CreateType(ctx context.Context, id, typeName string, sortOrder int32, remark string) (*authmodel.DictType, error) {
	if id == "" {
		return nil, fmt.Errorf("dict.CreateType: id is required")
	}
	t := &authmodel.DictType{
		ID: id, TypeName: typeName, SortOrder: sortOrder, Enabled: true, Remark: remark,
	}
	if err := b.typeStore().Create(ctx, t); err != nil {
		return nil, fmt.Errorf("dict.CreateType(%s): %w", id, err)
	}
	return t, nil
}

// Update 更新字典类型展示属性（type_code 不可变）。空值/未置位字段不改。
func (b *Biz) UpdateType(ctx context.Context, id, typeName string, sortOrder int32, sortOrderSet bool, enabled, enabledSet bool, remark string) (*authmodel.DictType, error) {
	t, err := b.GetType(ctx, id)
	if err != nil {
		return nil, err
	}
	if typeName != "" {
		t.TypeName = typeName
	}
	if sortOrderSet {
		t.SortOrder = sortOrder
	}
	if enabledSet {
		t.Enabled = enabled
	}
	if remark != "" {
		t.Remark = remark
	}
	if err := b.typeStore().Update(ctx, t); err != nil {
		return nil, fmt.Errorf("dict.UpdateType(%s): %w", id, err)
	}
	return t, nil
}

// Delete 删除字典类型（级联删除其全部条目）并失效该类型缓存键。返回实际删除数
// （1 = 类型本身；0 = 类型不存在）。
func (b *Biz) DeleteType(ctx context.Context, id string) (int, error) {
	if _, err := b.GetType(ctx, id); err != nil {
		return 0, err // 类型不存在（404）
	}
	// 级联删条目（源 entsql OnDelete: Cascade 语义的显式实现）。
	entries, err := b.listEntriesDirect(ctx, id)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		w := &store.Where{}
		w.Filters = append(w.Filters, store.Eq("id", e.ID))
		if err := b.entryStore().Delete(ctx, w.T(ctx)); err != nil {
			return 0, fmt.Errorf("dict.DeleteType(%s): cascade entry %s: %w", id, e.ID, err)
		}
	}
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	if err := b.typeStore().Delete(ctx, w.T(ctx)); err != nil {
		return 0, fmt.Errorf("dict.DeleteType(%s): %w", id, err)
	}
	b.invalidate(ctx, id)
	return 1, nil
}

// ---- 字典项 ----

// ListEntries 列出字典项（SortOrder 升序）。typeCode 非空走 Cache-Aside：
// 键 dict:entries:<tenant>:<typeCode>，缓存该类型全部条目 JSON；未命中（或 Redis
// 故障降级）经 loader 读 store 回填。typeCode 为空全量直连（跨类型聚合不缓存）。
func (b *Biz) ListEntries(ctx context.Context, typeCode string) ([]*authmodel.DictEntry, int, error) {
	if typeCode == "" {
		es, total, err := b.entryStore().List(ctx, (&store.Where{}).T(ctx))
		if err != nil {
			return nil, 0, fmt.Errorf("dict.ListEntries: %w", err)
		}
		b.sortEntries(es)
		return es, int(total), nil
	}
	if _, err := b.GetType(ctx, typeCode); err != nil {
		return nil, 0, fmt.Errorf("dict.ListEntries: type %s: %w", typeCode, err)
	}
	tenant := contextx.TenantIDFromContext(ctx)
	key := rediscache.Key("dict:entries", tenant, typeCode)
	loader := func(c context.Context) (string, error) {
		es, err := b.listEntriesDirect(c, typeCode)
		if err != nil {
			return "", err
		}
		buf, err := json.Marshal(es)
		if err != nil {
			return "", fmt.Errorf("dict.ListEntries marshal: %w", err)
		}
		return string(buf), nil
	}
	var raw string
	var err error
	if b.cache != nil {
		raw, err = b.cache.Get(ctx, key, loader)
	} else {
		raw, err = loader(ctx)
	}
	if err != nil {
		return nil, 0, err
	}
	var es []*authmodel.DictEntry
	if err := json.Unmarshal([]byte(raw), &es); err != nil {
		return nil, 0, fmt.Errorf("dict.ListEntries unmarshal: %w", err)
	}
	return es, len(es), nil
}

// GetEntry 取字典项。
func (b *Biz) GetEntry(ctx context.Context, id string) (*authmodel.DictEntry, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	e, err := b.entryStore().Get(ctx, w.T(ctx))
	if err != nil {
		return nil, fmt.Errorf("dict.GetEntry(%s): %w", id, err)
	}
	return e, nil
}

// CreateEntry 创建字典项（校验类型存在；业务键 type_code:value 租户内唯一，
// 冲突即重复条目）。写穿透：失效该类型缓存键。
func (b *Biz) CreateEntry(ctx context.Context, typeCode, value, label string, numeric *int32, sortOrder int32, remark string) (*authmodel.DictEntry, error) {
	if typeCode == "" || value == "" {
		return nil, fmt.Errorf("dict.CreateEntry: type_code and value are required")
	}
	if _, err := b.GetType(ctx, typeCode); err != nil {
		return nil, fmt.Errorf("dict.CreateEntry: type %s: %w", typeCode, err)
	}
	e := &authmodel.DictEntry{
		ID: typeCode + ":" + value, TypeCode: typeCode, Value: value,
		Label: label, Numeric: numeric, SortOrder: sortOrder, Enabled: true, Remark: remark,
	}
	if err := b.entryStore().Create(ctx, e); err != nil {
		return nil, fmt.Errorf("dict.CreateEntry(%s): %w", e.ID, err)
	}
	b.invalidate(ctx, typeCode)
	return e, nil
}

// UpdateEntry 更新字典项展示属性（type_code/value 业务键不可变）。写穿透失效。
// numeric 用显式 numeric_set 位（nil+set=清空数值；nil+unset=不改）。
func (b *Biz) UpdateEntry(ctx context.Context, id, label string, numeric *int32, numericSet bool, sortOrder int32, sortOrderSet bool, enabled, enabledSet bool, remark string) (*authmodel.DictEntry, error) {
	e, err := b.GetEntry(ctx, id)
	if err != nil {
		return nil, err
	}
	if label != "" {
		e.Label = label
	}
	if numericSet {
		e.Numeric = numeric
	}
	if sortOrderSet {
		e.SortOrder = sortOrder
	}
	if enabledSet {
		e.Enabled = enabled
	}
	if remark != "" {
		e.Remark = remark
	}
	if err := b.entryStore().Update(ctx, e); err != nil {
		return nil, fmt.Errorf("dict.UpdateEntry(%s): %w", id, err)
	}
	b.invalidate(ctx, e.TypeCode)
	return e, nil
}

// DeleteEntry 删除字典项并失效缓存。返回是否实际删除。
func (b *Biz) DeleteEntry(ctx context.Context, id string) (bool, error) {
	e, err := b.GetEntry(ctx, id)
	if err != nil {
		return false, err
	}
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	if err := b.entryStore().Delete(ctx, w.T(ctx)); err != nil {
		return false, fmt.Errorf("dict.DeleteEntry(%s): %w", id, err)
	}
	b.invalidate(ctx, e.TypeCode)
	return true, nil
}

// listEntriesDirect 直连 store 读某类型全部条目（loader 体内用，不带排序回填）。
func (b *Biz) listEntriesDirect(ctx context.Context, typeCode string) ([]*authmodel.DictEntry, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("type_code", typeCode))
	es, _, err := b.entryStore().List(ctx, w.T(ctx))
	if err != nil {
		return nil, fmt.Errorf("dict.listEntriesDirect(%s): %w", typeCode, err)
	}
	b.sortEntries(es)
	return es, nil
}

// sortEntries SortOrder 升序稳定排序（缓存 JSON 内保持排定形态，输出确定）。
func (b *Biz) sortEntries(es []*authmodel.DictEntry) {
	sort.SliceStable(es, func(i, j int) bool { return es[i].SortOrder < es[j].SortOrder })
}

// invalidate 写穿透失效某类型缓存键（cache 禁用时 no-op；失效失败静默——
// TTL 5min 兜底过期，缓存故障不放大为写故障）。
func (b *Biz) invalidate(ctx context.Context, typeCode string) {
	if b.cache == nil {
		return
	}
	tenant := contextx.TenantIDFromContext(ctx)
	_ = b.cache.Delete(ctx, rediscache.Key("dict:entries", tenant, typeCode))
}
