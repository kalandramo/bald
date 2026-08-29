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

	storev1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/store/v1"
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
	q, err := s.provider.DB(ctx)
	if err != nil {
		return err
	}
	return q.Create(ctx, obj)
}

// Update 更新一条记录。
func (s *Store[T]) Update(ctx context.Context, obj *T) error {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return err
	}
	return q.Update(ctx, obj)
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
	where, meta, err := s.translate(req)
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
func (s *Store[T]) translate(req *storev1.PagingRequest) (*Where, *storev1.PaginationResponseMeta, error) {
	where := &Where{Sorting: req.GetSorting()}
	if fe := req.GetFilterExpr(); fe != nil {
		filters, err := flatten(fe)
		if err != nil {
			return nil, nil, err
		}
		where.Filters = filters
	}
	meta := &storev1.PaginationResponseMeta{}

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
