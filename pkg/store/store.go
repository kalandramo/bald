// Package store 定义 bald 框架的数据访问层（DAL）抽象。
//
// 设计主线（与 pkg/registry、pkg/config 一致）：核心只定最小接口与契约，
// 不绑定任何具体存储引擎；GORM / MongoDB 等实现作为独立子模块，经
// DBProvider 桥接注入。本包零引擎依赖，内置 inmemory 实现（pkg/store/inmemory）
// 供 e2e 演示与测试零外部依赖使用。
//
// 泛型 Store[T] 是引擎无关的 CRUD 门面；真正的查询/写入由后端实现的
// Queryable[T] 完成。Where 以「DTO 字段名 + 操作符」表达条件，由后端翻译成
// 各自的 SQL / NoSQL，核心不感知引擎方言。
package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
)

// DefaultPageSize 是分页默认每页条数。
const DefaultPageSize = 10

// MaxPageSize 是分页每页条数上限，防止恶意大页请求。
const MaxPageSize = 100

// Queryable 是后端对具体存储引擎的 CRUD 实现契约（依赖倒置）。
// Store[T] 不直接接触引擎，只调用本接口。每种后端（inmemory/gorm/mongo）
// 各自实现。
type Queryable[T any] interface {
	Create(ctx context.Context, obj *T) error
	Update(ctx context.Context, obj *T) error
	Delete(ctx context.Context, where *Where) error
	Get(ctx context.Context, where *Where) (*T, error)
	List(ctx context.Context, where *Where) (items []*T, total int64, err error)
	Count(ctx context.Context, where *Where) (int64, error)
	// Migrate 建立/更新表结构或集合（可选，业务按需调用）。
	Migrate(ctx context.Context, models ...any) error
}

// DBProvider 提供存储句柄，由调用方注入具体引擎实现。
// 例：bald-store-gorm 的 NewGormProvider(db) 返回实现了本接口的提供者。
type DBProvider[T any] interface {
	DB(ctx context.Context) (Queryable[T], error)
	// Close 释放底层连接（可选）。
	Close() error
}

// Store 是引擎无关的泛型 CRUD 门面。
type Store[T any] struct {
	provider DBProvider[T]
	logger   Logger
	opts     options
}

type options struct {
	pageSize int
	maxSize  int
	logger   Logger
}

// Option 配置 Store 的行为。
type Option[T any] func(*options)

// WithPageSize 设置默认每页条数（默认 DefaultPageSize）。
func WithPageSize[T any](n int) Option[T] {
	return func(o *options) { o.pageSize = n }
}

// WithMaxPageSize 设置每页条数上限（默认 MaxPageSize）。
func WithMaxPageSize[T any](n int) Option[T] {
	return func(o *options) { o.maxSize = n }
}

// WithLogger 注入日志句柄（默认 NopLogger）。
func WithLogger[T any](l Logger) Option[T] {
	return func(o *options) { o.logger = l }
}

// NewStore 构造 Store[T]。
func NewStore[T any](provider DBProvider[T], opts ...Option[T]) *Store[T] {
	o := options{pageSize: DefaultPageSize, maxSize: MaxPageSize}
	for _, fn := range opts {
		fn(&o)
	}
	if o.logger == nil {
		o.logger = NopLogger{}
	}
	return &Store[T]{provider: provider, logger: o.logger, opts: o}
}

// Provider 返回底层 DBProvider（供需要直接访问引擎的场景）。
func (s *Store[T]) Provider() DBProvider[T] { return s.provider }

// Create 插入一条记录。
func (s *Store[T]) Create(ctx context.Context, obj *T) error {
	injectWriteTenant(ctx, obj) // 写路径多租户：自动写入当前租户，避免误建到他租户
	q, err := s.provider.DB(ctx)
	if err != nil {
		return err
	}
	return q.Create(ctx, obj)
}

// Update 更新一条记录。
func (s *Store[T]) Update(ctx context.Context, obj *T) error {
	injectWriteTenant(ctx, obj) // 写路径多租户：自动覆写租户，防止越权更新改租户归属
	q, err := s.provider.DB(ctx)
	if err != nil {
		return err
	}
	return q.Update(ctx, obj)
}

// injectWriteTenant 写路径多租户注入：依据全局租户注册表，把 ctx 中解析到的各租户
// 维度值反射写回实体对应字段。列名 key（如 "tenant_id"）按 gorm column/json 约定
// 解析为字段（snake_case → CamelCase，如 TenantID）。业务无需手写，避免漏写租户列
// 导致写入他租户归属（与读路径 mergeTenant 对称，闭环隔离）。
//
// 仅在 ctx 提供该维度值时注入；非多租户应用未注册维度则无操作。反射写入已跳过非导出
// 字段与已有非空值之外的全部维度；若实体无对应字段（如非租户实体）则静默跳过该维度。
func injectWriteTenant(ctx context.Context, obj any) {
	tenantMu.RLock()
	extractors := make(map[string]TenantValueFunc, len(tenantExtractors))
	for k, v := range tenantExtractors {
		extractors[k] = v
	}
	tenantMu.RUnlock()
	if len(extractors) == 0 {
		return
	}
	rv := reflect.ValueOf(obj)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return
	}
	rt := rv.Type()
	for key, fn := range extractors {
		val, ok := fn(ctx)
		if !ok || val == "" {
			continue
		}
		idx := tenantFieldIndex(rt, key)
		if idx < 0 {
			continue
		}
		fd := rv.Field(idx)
		if !fd.CanSet() || fd.Kind() != reflect.String {
			continue
		}
		fd.SetString(val)
	}
}

// tenantFieldIndex 按租户列名（snake_case，如 "tenant_id"）找到实体字段索引。
// 匹配优先级：gorm column tag > json tag > 字段名转 snake_case 与 key 直接相等
// （如 User.TenantID → "tenant_id" == key）。找不到返回 -1。
func tenantFieldIndex(rt reflect.Type, key string) int {
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		// gorm:"column:tenant_id"
		if tag := f.Tag.Get("gorm"); tag != "" {
			for _, part := range splitTag(tag) {
				if v, ok := cutPrefix(part, "column:"); ok && v == key {
					return i
				}
			}
		}
		// json:"tenant_id"
		if j := f.Tag.Get("json"); j != "" {
			if name, _, _ := strings.Cut(j, ","); name == key {
				return i
			}
		}
		// 字段名 CamelCase → snake_case 与 key 比对（如 TenantID → tenant_id）。
		if fieldToSnake(f.Name) == key {
			return i
		}
	}
	return -1
}

// fieldToSnake 把 CamelCase 字段名转为 snake_case 列名（如 TenantID → tenant_id、
// UserName → user_name）。连续大写缩写按整体处理（ID 末尾不拆成 i_d）。
func fieldToSnake(name string) string {
	var b strings.Builder
	var prevLower, prevDigit bool
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && (prevLower || prevDigit || (i+1 < len(name) && name[i+1] >= 'a' && name[i+1] <= 'z')) {
				b.WriteByte('_')
			}
			b.WriteByte(byte(r - 'A' + 'a'))
			prevLower, prevDigit = false, false
		case r >= '0' && r <= '9':
			if i > 0 && prevLower {
				b.WriteByte('_')
			}
			b.WriteByte(byte(r))
			prevLower, prevDigit = false, true
		default:
			b.WriteByte(byte(r))
			prevLower, prevDigit = true, false
		}
	}
	return b.String()
}

func splitTag(tag string) []string {
	return strings.Split(tag, ";")
}

func cutPrefix(s, prefix string) (string, bool) {
	if strings.HasPrefix(s, prefix) {
		return s[len(prefix):], true
	}
	return s, false
}

// Delete 按条件删除（建议 where 至少含主键）。
func (s *Store[T]) Delete(ctx context.Context, where *Where) error {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return err
	}
	return q.Delete(ctx, where)
}

// Get 按条件取单条；未命中返回 (nil, nil)。
func (s *Store[T]) Get(ctx context.Context, where *Where) (*T, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return nil, err
	}
	return q.Get(ctx, where)
}

// List 无条件（仅偏移/限制）列出。
func (s *Store[T]) List(ctx context.Context, where *Where) ([]*T, int64, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return nil, 0, err
	}
	return q.List(ctx, where)
}

// Count 统计符合条件记录数。
func (s *Store[T]) Count(ctx context.Context, where *Where) (int64, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return 0, err
	}
	return q.Count(ctx, where)
}

// PagingResult 是带分页元数据的列表结果（Go 层泛型返回）。
type PagingResult[T any] struct {
	Items []*T
	Meta  *storev1.PaginationResponseMeta
}

// ListWithPaging 按 PagingRequest 翻译分页/过滤/排序后列出。
// 返回 items 与分页元数据（total / current_page / next_token 等）。
func (s *Store[T]) ListWithPaging(ctx context.Context, req *storev1.PagingRequest) (*PagingResult[T], error) {
	where, meta, err := s.translate(ctx, req)
	if err != nil {
		return nil, err
	}
	q, err := s.provider.DB(ctx)
	if err != nil {
		return nil, err
	}
	items, total, err := q.List(ctx, where)
	if err != nil {
		return nil, err
	}
	fillTotal(meta, total, where, s.opts)
	return &PagingResult[T]{Items: items, Meta: meta}, nil
}

// translate 把 PagingRequest 翻译成 Where + 分页元数据骨架。
// 分页 offset/limit 的解析交由分页策略（pkg/store/paging.go）完成，
// 与具体策略解耦；本函数仅负责按所选策略填充对应元数据字段。
func (s *Store[T]) translate(ctx context.Context, req *storev1.PagingRequest) (*Where, *storev1.PaginationResponseMeta, error) {
	where := &Where{Sorting: req.GetSorting()}
	if fe := req.GetFilterExpr(); fe != nil {
		filters, err := flatten(fe)
		if err != nil {
			return nil, nil, err
		}
		where.Filters = filters
	}
	meta := &storev1.PaginationResponseMeta{}

	// 多租户隔离：租户条件下沉 DAL，自动注入（优先于业务条件，不可被覆盖）。
	// 即使 NoPaging 全量列出也必须隔离，防止跨租户数据泄漏。
	mergeTenant(where, ctx)
	// 数据权限范围：在租户隔离基础上进一步收窄可见行（P9，Viewer 五级范围）。
	mergeDataScope(where, ctx)

	if req.GetNoPaging() {
		// 不分页：全量列出，不填页元数据。
		off, limit, err := noPaginator{}.Resolve(req, s.opts.pageSize, s.opts.maxSize)
		if err != nil {
			return nil, nil, err
		}
		where.Offset, where.Limit = off, limit
		return where, meta, nil
	}

	// 选择并解析分页策略。
	p := detectStrategy(req)
	off, limit, err := p.Resolve(req, s.opts.pageSize, s.opts.maxSize)
	if err != nil {
		return nil, nil, err
	}
	where.Offset, where.Limit = off, limit

	// 按策略类型填充元数据。
	ps := limit
	if ps <= 0 {
		ps = s.opts.pageSize
	}
	meta.PageSize = uint32p(uint32(ps))
	switch p.(type) {
	case pagePaginator:
		page := off/ps + 1
		if page < 1 {
			page = 1
		}
		meta.CurrentPage = uint32Ptr(uint32(page))
	case offsetPaginator:
		meta.CurrentOffset = uint64Ptr(uint64(off))
	}
	// NextToken 必须等 fillTotal 拿到 total 后才能判定是否还有下一页，
	// 改到 fillTotal 末尾统一填充，避免最后一页产生空翻页。
	return where, meta, nil
}

// flatten 把嵌套 FilterExpr 展平为条件列表。
//
// 每个 FilterExpr 节点自带 Type（AND/OR），下辖 Conditions 与 Groups。
// 当前 inmemory / gorm 后端仅支持扁平 AND 过滤；一旦出现 OR 节点（顶层或
// 任意嵌套），直接报错，提示后端需支持组级逻辑，而非静默降级为 AND
// （避免组合查询语义被悄悄改写，返回错误结果且不报错）。
func flatten(fe *storev1.FilterExpr) ([]*storev1.FilterCondition, error) {
	if fe.GetType() == storev1.FilterExpr_OR {
		return nil, fmt.Errorf("store: OR filter not supported by flat translate; " +
			"use a backend with group-aware Where")
	}
	var out []*storev1.FilterCondition
	out = append(out, fe.GetConditions()...)
	for _, g := range fe.GetGroups() {
		sub, err := flatten(g)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

func clampPageSize(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func fillTotal(meta *storev1.PaginationResponseMeta, total int64, where *Where, o options) {
	if meta.Total == nil {
		meta.Total = uint64Ptr(uint64(total))
	}
	if meta.TotalPages == nil && meta.PageSize != nil && *meta.PageSize > 0 {
		pages := (uint32(total) + *meta.PageSize - 1) / *meta.PageSize
		meta.TotalPages = uint32Ptr(pages)
	}
	// 仅当确实还有下一页时才下发 token，避免最后一页产生空翻页死循环。
	hasMore := where.Limit > 0 && total > int64(where.Offset)+int64(where.Limit)
	if hasMore {
		meta.NextToken = strp(encodeToken(where.Offset + where.Limit))
	}
}
