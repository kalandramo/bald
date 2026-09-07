package auditlog

// Package auditlog 审计日志查询业务层（T6）。只读：数据由写路径（拦截器链/
// 登录动作）经 StoreAuditor 自动落库，本层仅提供过滤+分页检索。审计表刻意
// 全量记录（不走 TenantID 读隔离——审计是跨租户留痕，查询侧租户过滤由
// 管理面策略决定；当前 admin 可见全量，与源项目审计查询一致）。
//
// store 经请求期包级引用（bootstrap.AuditStore，InitBridges 装配后可用）。

import (
	"context"
	"strconv"

	berrors "github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/store"

	authmodel "github.com/kalandramo/bald/examples/go-bald-admin/internal/apiserver/model"
	bootstrappkg "github.com/kalandramo/bald/examples/go-bald-admin/internal/bootstrap"
)

// 分页边界：默认页大小 50，上限 200（防大结果集拖垮查询）。
const (
	defaultPageSize = 50
	maxPageSize     = 200
)

// ListFilter 审计查询过滤条件（全可选；非空即过滤）。
type ListFilter struct {
	Category string // operation / login
	Subject  string
	Object   string
	Action   string
	Result   string // allow / deny / error
	IP       string // 客户端 IP
}

// Page 分页参数：token 是上页响应的 next_page_token（offset 十进制编码）。
type Page struct {
	Size  uint32
	Token string
}

// ListResult 查询结果：当前页条目 + 总数 + 下一页令牌（空 = 末页）。
type ListResult struct {
	Items         []*authmodel.AuditRecord
	Total         int64
	NextPageToken string
}

// Biz 审计查询业务。
type Biz struct{}

// New 构造审计查询 biz。
func New() *Biz { return &Biz{} }

// auditStore 请求期读取（InitBridges 装配后可用）。
func (b *Biz) auditStore() *store.Store[authmodel.AuditRecord] {
	return bootstrappkg.AuditStore
}

// List 分页查询审计日志：全量记录不走 TenantID 读隔离（Where 不注入 T），
// 过滤条件精确匹配，按时间降序（最新在前，审计查询惯例）。
func (b *Biz) List(ctx context.Context, f ListFilter, p Page) (*ListResult, error) {
	w := &store.Where{}
	if f.Category != "" {
		w.Filters = append(w.Filters, store.Eq("category", f.Category))
	}
	if f.Subject != "" {
		w.Filters = append(w.Filters, store.Eq("subject", f.Subject))
	}
	if f.Object != "" {
		w.Filters = append(w.Filters, store.Eq("object", f.Object))
	}
	if f.Action != "" {
		w.Filters = append(w.Filters, store.Eq("action", f.Action))
	}
	if f.Result != "" {
		w.Filters = append(w.Filters, store.Eq("result", f.Result))
	}
	if f.IP != "" {
		w.Filters = append(w.Filters, store.Eq("ip_address", f.IP))
	}
	w.Sorting = append(w.Sorting, store.SortDesc("time"))

	size := int(p.Size)
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}
	offset, err := decodePageToken(p.Token)
	if err != nil {
		return nil, berrors.BadRequest("audit/invalid_page_token")
	}
	w.Offset = offset
	w.Limit = size

	items, total, err := b.auditStore().List(ctx, w)
	if err != nil {
		return nil, err
	}

	next := ""
	if offset+size < int(total) {
		next = strconv.Itoa(offset + size)
	}
	return &ListResult{Items: items, Total: total, NextPageToken: next}, nil
}

// Get 取单条审计日志。
func (b *Biz) Get(ctx context.Context, id string) (*authmodel.AuditRecord, error) {
	w := &store.Where{}
	w.Filters = append(w.Filters, store.Eq("id", id))
	m, err := b.auditStore().Get(ctx, w)
	if err != nil || m == nil {
		return nil, berrors.NotFound("audit/not_found")
	}
	return m, nil
}

// decodePageToken 解析翻页令牌（offset 十进制编码；空串 = 首页 0）。
func decodePageToken(token string) (int, error) {
	if token == "" {
		return 0, nil
	}
	off, err := strconv.Atoi(token)
	if err != nil || off < 0 {
		return 0, strconv.ErrSyntax
	}
	return off, nil
}
