// Package menu 是菜单管理业务（T3，自 go-wind-admin permission 域 sys_menus 精简移植）。
//
// 平台侧管理面：本表（model.Menu）无 TenantID 字段，查询不调 Where.T——
// P8 隔离对菜单管理面天然不生效，全量读写即平台语义（同 Tenant Biz）。
// 授权约束（仅 admin 可操作）在中间件层经 casbin 策略完成，biz 不感知角色。
package menu

import (
	"context"
	"fmt"
	"sort"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
	"github.com/kalandramo/bald/pkg/store"
)

// Biz 菜单管理业务。仓储经 store() 请求期读取（wire 构造期 bootstrap.MenuStore
// 尚未初始化——T0 确立的时序约定：biz 引用 bootstrap 包级桥接禁止构造期快照）。
type Biz struct{}

// New 构造菜单业务。
func New() *Biz { return &Biz{} }

func (b *Biz) store() *store.Store[authmodel.Menu] { return bootstrappkg.MenuStore }

// Get 取菜单节点。不存在返回 error（上层转 404/NotFound）。
func (b *Biz) Get(ctx context.Context, id string) (*authmodel.Menu, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	m, err := b.store().Get(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("menu.Get(%s): %w", id, err)
	}
	return m, nil
}

// ListTree 返回菜单树：全表读出后内存组树（源 pagination.BuildTree 同语义：
// 根 = ParentID 空，孤儿节点跳过），各层按 Order 升序稳定排序，返回根数组。
// total 为全部节点数（含子孙）。
func (b *Biz) ListTree(ctx context.Context) ([]*authmodel.Menu, int, error) {
	ms, _, err := b.store().List(ctx, &store.Where{})
	if err != nil {
		return nil, 0, fmt.Errorf("menu.ListTree: %w", err)
	}
	roots := buildTree(ms)
	return roots, len(ms), nil
}

// Create 创建菜单节点。ID 客户端指定（语义编码）；ParentID 非空时校验父存在。
func (b *Biz) Create(ctx context.Context, id, parentID, typ, name, path, component, title, icon string, order int32, remark string) (*authmodel.Menu, error) {
	if id == "" || typ == "" {
		return nil, fmt.Errorf("menu.Create: id and type are required")
	}
	switch typ {
	case "CATALOG", "MENU", "BUTTON":
	default:
		return nil, fmt.Errorf("menu.Create(%s): invalid type %q", id, typ)
	}
	if parentID != "" {
		if _, err := b.Get(ctx, parentID); err != nil {
			return nil, fmt.Errorf("menu.Create(%s): parent %s: %w", id, parentID, err)
		}
	}
	m := &authmodel.Menu{
		ID: id, ParentID: parentID, Type: typ, Name: name, Path: path,
		Component: component, Title: title, Icon: icon, Order: order,
		Status: "ON", Remark: remark,
	}
	if err := b.store().Create(ctx, m); err != nil {
		return nil, fmt.Errorf("menu.Create(%s): %w", id, err)
	}
	return m, nil
}

// Update 更新菜单节点。空值/UNSPECIFIED 字段不改；orderSet 显式区分「改 order 为 0」
// 与「不改」（int32 零值是合法序号）。ParentID 非空时校验父存在且不形成环。
func (b *Biz) Update(ctx context.Context, id, parentID, typ, name, path, component, title, icon string, order int32, orderSet bool, status, remark string) (*authmodel.Menu, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	m, err := b.store().Get(ctx, w)
	if err != nil {
		return nil, fmt.Errorf("menu.Update(%s): %w", id, err)
	}
	if parentID != "" {
		if parentID == id {
			return nil, fmt.Errorf("menu.Update(%s): parent cannot be self", id)
		}
		if _, err := b.Get(ctx, parentID); err != nil {
			return nil, fmt.Errorf("menu.Update(%s): parent %s: %w", id, parentID, err)
		}
		// 环检测：沿候选父节点的祖先链上溯，回到自身即拒绝（此前仅防 self-parent，
		// menu-a→menu-b→menu-a 成环后两节点从 buildTree 的根可达集中静默消失——
		// 数据在库而 API 不可达）。全表读出建索引后 O(链长) 判定。
		all, _, err := b.store().List(ctx, &store.Where{})
		if err != nil {
			return nil, fmt.Errorf("menu.Update(%s): %w", id, err)
		}
		byID := make(map[string]*authmodel.Menu, len(all))
		for _, n := range all {
			byID[n.ID] = n
		}
		for cur := byID[parentID]; cur != nil && cur.ParentID != ""; cur = byID[cur.ParentID] {
			if cur.ParentID == id {
				return nil, fmt.Errorf("menu.Update(%s): parent %s would form a cycle", id, parentID)
			}
		}
		m.ParentID = parentID
	}
	if typ != "" {
		switch typ {
		case "CATALOG", "MENU", "BUTTON":
			m.Type = typ
		default:
			return nil, fmt.Errorf("menu.Update(%s): invalid type %q", id, typ)
		}
	}
	if name != "" {
		m.Name = name
	}
	if path != "" {
		m.Path = path
	}
	if component != "" {
		m.Component = component
	}
	if title != "" {
		m.Title = title
	}
	if icon != "" {
		m.Icon = icon
	}
	if orderSet {
		m.Order = order
	}
	if status != "" {
		switch status {
		case "ON", "OFF":
			m.Status = status
		default:
			return nil, fmt.Errorf("menu.Update(%s): invalid status %q", id, status)
		}
	}
	if remark != "" {
		m.Remark = remark
	}
	if err := b.store().Update(ctx, m); err != nil {
		return nil, fmt.Errorf("menu.Update(%s): %w", id, err)
	}
	return m, nil
}

// Delete 删除菜单节点（级联删子树——源 QueryAllChildrenIds 语义：全表读出后
// BFS 收集子孙 ID 逐个删除）。返回实际删除数。
func (b *Biz) Delete(ctx context.Context, id string) (int, error) {
	all, _, err := b.store().List(ctx, &store.Where{})
	if err != nil {
		return 0, fmt.Errorf("menu.Delete(%s): %w", id, err)
	}
	children := make(map[string][]string)
	for _, m := range all {
		if m.ParentID != "" {
			children[m.ParentID] = append(children[m.ParentID], m.ID)
		}
	}
	ids := []string{id}
	for i := 0; i < len(ids); i++ { // BFS 逐层展开子孙（含自身）
		ids = append(ids, children[ids[i]]...)
	}
	deleted := 0
	for _, mid := range ids {
		w := &store.Where{}
		w.Filters = append(w.Filters, store.Eq("id", mid))
		if err := b.store().Delete(ctx, w); err != nil {
			return deleted, fmt.Errorf("menu.Delete(%s): %w", mid, err)
		}
		deleted++
	}
	return deleted, nil
}

// buildTree 两遍 O(n) 组树：map 索引 → 挂父子；孤儿节点跳过（父不存在不进结果）；
// 各层按 Order 升序稳定排序（源项目展示顺序由 meta.order 控制，BuildTree 不排序、
// 本实现在内存统一排，输出确定）。
func buildTree(ms []*authmodel.Menu) []*authmodel.Menu {
	byID := make(map[string]*authmodel.Menu, len(ms))
	for _, m := range ms {
		byID[m.ID] = m
	}
	var roots []*authmodel.Menu
	for _, m := range ms {
		if m.ParentID == "" {
			roots = append(roots, m)
			continue
		}
		if p, ok := byID[m.ParentID]; ok {
			p.Children = append(p.Children, m)
		}
		// 孤儿（父不在表中）跳过——源 BuildTree 同语义。
	}
	var sortLevel func(nodes []*authmodel.Menu)
	sortLevel = func(nodes []*authmodel.Menu) {
		sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].Order < nodes[j].Order })
		for _, n := range nodes {
			sortLevel(n.Children)
		}
	}
	sortLevel(roots)
	return roots
}
