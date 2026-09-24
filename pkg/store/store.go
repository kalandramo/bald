// Package store 定义 bald 框架的数据访问层（DAL）抽象。
//
// 设计主线（与 registry、pkg/config 一致）：核心只定最小接口与契约，
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
	"reflect"
	"strings"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/kalandramo/bald/pkg/contextx"
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
	Update(ctx context.Context, obj *T) (rows int64, err error)
	Delete(ctx context.Context, where *Where) (rows int64, err error)
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
	// platformLevel 标记该实体为「平台级」——表本身无租户维度（如 menu /
	// language / permission / role），租户隔离对其不适用。默认 false（隔离生效），
	// 必须经 WithPlatformLevel 显式声明才豁免（fail-closed）。
	platformLevel bool
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

// WithPlatformLevel 声明该实体为**平台级**——表无租户维度，跳过租户隔离注入。
//
// 用途：菜单、语言、权限点、角色这类**全平台共享**的表，数据模型里没有
// tenant_id 列。若不声明，Get/List/Count/Delete 会注入 `tenant_id = ?` 谓词，
// 对无此列的表产生 `no such column: tenant_id` 错误（v0.9.0 起 applyIsolation
// 自动注入引入的行为）。
//
// **fail-closed 约定**：默认（不调用本 option）隔离始终生效。豁免必须显式声明，
// 且声明后该 Store 的所有读/写都不再受租户过滤——误标的后果是隔离静默失效，
// 故仅对**确实无租户维度**的表使用。
//
// 与 `Where.T(ctx)` 的关系：后者是**单次查询**的显式隔离声明；本 option 是
// **实体级**的豁免声明。两者语义正交。
func WithPlatformLevel[T any]() Option[T] {
	return func(o *options) { o.platformLevel = true }
}

// IsPlatformLevel 报告该 Store 是否被声明为平台级（供 applyIsolation 决策）。
func (s *Store[T]) IsPlatformLevel() bool { return s.opts.platformLevel }

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

// Update 更新一条记录，返回受影响行数。
//
// **幂等语义**（与 Delete 一致）：目标记录不存在时返回 `(0, nil)`，不报错——
// 更新的目标是让数据变成目标值，本来就是目标值，操作本身成功；SQL 原生
// `UPDATE` 影响 0 行是正常执行，ORM 不擅自增加底层没有的错误。
//
// 影响行数属**业务状态**，交由业务代码判断；`Result.Error` 只承载系统错误
// （网络、锁冲突等）。
//
// ⚠️ **`rows == 0` 不是「记录不存在」的可靠代理**。返回的是**受影响（被改变）
// 行数**，不是**匹配行数**，且该语义**跨后端不一致**：MySQL 默认（未开
// `CLIENT_FOUND_ROWS`）对「UPDATE 到与现有值相同」返回 0（行确实存在），而
// SQLite 返回 1（本仓 `update_idempotent_test.go` 实测锁定）。故需要精确
// 「必须存在才能更新」语义时，**不要**用 `if rows == 0` 判定——MySQL 下会把
// 「幂等更新到相同值」误判为不存在。应显式 `Get` 判存在，或先
// `SELECT ... FOR UPDATE` 再更新（顺带解决并发）：
//
//	// ❌ MySQL 下误判：更新到相同值也会返回 0
//	rows, err := store.Update(ctx, obj)
//	if err != nil { return err }
//	if rows == 0 { return ErrNotFound }
//
//	// ✅ 精确存在性判断（与后端无关）
//	if _, err := store.Get(ctx, where); err != nil { return err } // ErrNotFound 即不存在
//	if _, err := store.Update(ctx, obj); err != nil { return err }
//
// `rows` 仍适合「本次是否真的改变了数据」这类**观测**用途（如决定要不要触发
// 下游副作用）——注意该观测同样受上述跨后端差异影响。
func (s *Store[T]) Update(ctx context.Context, obj *T) (int64, error) {
	injectWriteTenant(ctx, obj) // 写路径多租户：自动覆写租户，防止越权更新改租户归属
	q, err := s.provider.DB(ctx)
	if err != nil {
		return 0, err
	}
	return q.Update(ctx, obj)
}

// injectWriteTenant 写路径多租户注入：依据全局租户注册表，把 ctx 中解析到的各租户
// 维度值反射写回实体对应字段。列名 key（如 "tenant_id"）按 gorm column/json 约定
// 解析为字段（snake_case → CamelCase，如 TenantID）。业务无需手写，避免漏写租户列
// 导致写入他租户归属（与读路径 mergeTenant 对称，闭环隔离）。
//
// 仅在 ctx 提供该维度值时注入；非多租户应用未注册维度则无操作。反射写入对实体对应
// 字段是**无条件覆写**（仅检查字段可导出且为 string 类型）——业务试图写入他租户值会被
// ctx 真实租户覆盖（`write_tenant_test.go` 锁定「越权值被覆盖」）；若实体无对应字段
// （如非租户实体）则静默跳过该维度。
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

// applyIsolation 返回注入了多租户/数据范围隔离条件的 Where 副本（不改动入参）。
//
// 与 ListWithPaging 的 translate 同源：租户与数据范围条件下沉 DAL 自动注入，
// 即使调用方未显式 where.T(ctx) 也生效——避免多租户应用用 Get/List/Count/Delete
// 直查时忘了隔离而读写全租户数据（安全边界，已知边界 1）。
//
// **平台级豁免（2026-09-24）**：若本 Store 经 WithPlatformLevel 显式声明为平台级
// （表无租户维度），则**跳过租户条件注入**，仅注入数据范围——否则对无 tenant_id
// 列的表会产生 `no such column: tenant_id`。豁免是显式的、fail-closed 的：未声明
// 即隔离生效。
//
// 触发条件：仅当 ctx 携带租户值（且维度已注册）或 AuthClaims 时才有条件注入；非多租户
// 应用（未注册维度、无 claims）零影响——注入是 no-op。where 为 nil 视为空条件。
//
// 隔离条件与业务条件按 AND 连接（租户/数据范围进 Filters 或与 Expr 组合），业务
// 已手写的同名租户条件会被去重（见 mergeTenant），不会产生恒假叠加。
// shouldIsolate 是租户隔离的**唯一闸门**——所有读路径（applyIsolation 与
// translate）必须经它判断是否注入租户条件。
//
// 存在意义（2026-09-24 暗雷修复）：此前 applyIsolation 有 platformLevel 守卫，
// 而 translate 直接调 mergeTenant 无守卫——同一不变量的两个违反点只修了一个，
// 导致「声明平台级的实体改用 ListWithPaging 仍被注入 tenant_id = ?」
// （对无该列的表即 `no such column: tenant_id`）。统一到本方法后，
// 新增读路径只需调用它，不会再出现不对称。
//
// 返回 true 表示**应注入**租户隔离（fail-closed 默认）。
func (s *Store[T]) shouldIsolate(ctx context.Context) bool {
	// 实体级豁免：表本身无租户维度（WithPlatformLevel 显式声明）。
	if s.opts.platformLevel {
		return false
	}
	// 身份级豁免：平台级身份（跨租户视图，如平台管理员）。
	// 与实体级正交——前者问「这张表有没有租户列」，后者问「这个请求要不要按租户切分」。
	if contextx.PlatformFromContext(ctx) {
		return false
	}
	return true
}

func (s *Store[T]) applyIsolation(ctx context.Context, where *Where) *Where {
	out := &Where{}
	if where != nil {
		out.Offset = where.Offset
		out.Limit = where.Limit
		out.Sorting = where.Sorting
		out.Expr = where.Expr
		// 复制 Filters：避免 mergeTenant/mergeDataScope 的 append 修改调用方底层数组。
		out.Filters = append([]*storev1.FilterCondition(nil), where.Filters...)
	}
	if s.shouldIsolate(ctx) {
		mergeTenant(out, ctx)
	}
	mergeDataScope(out, ctx)
	mergeDataScopeExpr(out, ctx)
	return out
}

// Delete 按条件删除，返回受影响行数。
//
// **幂等语义**：对 0 行匹配返回 `(0, nil)`，不报错——删除不存在的资源是合法
// 结果，集合删除本就为空亦然（REST DELETE 幂等标准）。这消除了旧语义下
// 「集合删除被误报为 not found」的缺陷（框架缺陷报告 D12）。
//
// 需要「必须存在才能删」的调用方，**由上层显式实现**——判 `rows == 0` 即可，
// 无需额外的 `Get` 往返：
//
//	rows, err := store.Delete(ctx, where)
//	if err != nil { return err }
//	if rows == 0 { return ErrNotFound } // 调用方自定义语义
//
// **多租户隔离**：删除条件自动注入租户/数据范围隔离（见 applyIsolation）——多租户
// 应用删不到他租户记录。ctx 无租户值时行为不变。
//
// 注意：`Get` 的未命中语义**不变**（仍返回 `ErrNotFound`），二者是独立契约。
func (s *Store[T]) Delete(ctx context.Context, where *Where) (int64, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return 0, err
	}
	return q.Delete(ctx, s.applyIsolation(ctx, where))
}

// Get 按条件取单条。
//
// **未命中返回 `ErrNotFound`**（两个 provider 一致：gorm `:149`、inmemory
// `:104`），**不是** `(nil, nil)`——调用方须判 error 而非只判 nil。
//
// **多租户隔离**：查询条件自动注入租户/数据范围隔离（见 applyIsolation）——多租户
// 应用取不到他租户记录（返回 `ErrNotFound`）。ctx 无租户值时行为不变。
func (s *Store[T]) Get(ctx context.Context, where *Where) (*T, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return nil, err
	}
	return q.Get(ctx, s.applyIsolation(ctx, where))
}

// List 按条件列出（含偏移/限制）。
//
// **多租户隔离**：查询条件自动注入租户/数据范围隔离（见 applyIsolation）——多租户
// 应用列不到他租户记录。ctx 无租户值时行为不变。
func (s *Store[T]) List(ctx context.Context, where *Where) ([]*T, int64, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return nil, 0, err
	}
	return q.List(ctx, s.applyIsolation(ctx, where))
}

// Count 统计符合条件记录数。
//
// **多租户隔离**：统计条件自动注入租户/数据范围隔离（见 applyIsolation）——多租户
// 应用只数到本租户记录。ctx 无租户值时行为不变。
func (s *Store[T]) Count(ctx context.Context, where *Where) (int64, error) {
	q, err := s.provider.DB(ctx)
	if err != nil {
		return 0, err
	}
	return q.Count(ctx, s.applyIsolation(ctx, where))
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
	fillTotal(meta, total, len(items), where, s.opts)
	return &PagingResult[T]{Items: items, Meta: meta}, nil
}

// translate 把 PagingRequest 翻译成 Where + 分页元数据骨架。
// 分页 offset/limit 的解析交由分页策略（pkg/store/paging.go）完成，
// 与具体策略解耦；本函数仅负责按所选策略填充对应元数据字段。
func (s *Store[T]) translate(ctx context.Context, req *storev1.PagingRequest) (*Where, *storev1.PaginationResponseMeta, error) {
	where := &Where{
		Sorting: req.GetSorting(),
		Expr:    req.GetFilterExpr(), // 布尔树整体下推（AND/OR 嵌套），由后端递归翻译
	}
	meta := &storev1.PaginationResponseMeta{}

	// 多租户隔离：租户条件下沉 DAL，自动注入（优先于业务条件，不可被覆盖）。
	// 即使 NoPaging 全量列出也必须隔离，防止跨租户数据泄漏。
	// 经 shouldIsolate 统一闸门——与 applyIsolation 同源（2026-09-24 暗雷修复：
	// 此前本处无 platformLevel 守卫，与 applyIsolation 不对称）。
	if s.shouldIsolate(ctx) {
		mergeTenant(where, ctx)
	}
	// 数据权限范围：在租户隔离基础上进一步收窄可见行（P9，Viewer 五级范围；
	// 布尔树版支持多范围 OR 组合，如「本人 OR 本部门」）。
	mergeDataScope(where, ctx)
	mergeDataScopeExpr(where, ctx)

	// 选择并解析分页策略（NoPaging > Token > Page > Offset > 默认页码，单一路径）。
	p := detectStrategy(req)
	off, limit, err := p.Resolve(req, s.opts.pageSize, s.opts.maxSize)
	if err != nil {
		return nil, nil, err
	}
	where.Offset, where.Limit = off, limit

	// 不分页：全量列出，不填页元数据（noPaginator 的 offset=limit=0）。
	if _, ok := p.(noPaginator); ok {
		return where, meta, nil
	}

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

func clampPageSize(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func fillTotal(meta *storev1.PaginationResponseMeta, total int64, currentSize int, where *Where, o options) {
	if meta.Total == nil {
		meta.Total = uint64Ptr(uint64(total))
	}
	// CurrentSize：当前页实际返回条数（与 len(items) 一致）。
	if meta.CurrentSize == nil {
		meta.CurrentSize = uint32p(uint32(currentSize))
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
