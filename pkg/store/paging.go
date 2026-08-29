// Package store 的分页策略（对照 go-crud pagination/paginator.go）。
//
// PagingRequest 同时携带三类分页参数：Page/PageSize（页码）、Offset/Limit（偏移）、
// Token（游标）。本文件把"如何从中算出 offset/limit"抽成可替换的 Paginator 策略，
// 由 Store.ListWithPaging 经 detectStrategy 自动选择，核心不与具体策略耦合。
//
// 当前 Token 策略把 token 当作「偏移游标」（base64 或直接十进制偏移量），与既有
// 行为一致；未来可平滑升级为「基于主键/排序键的游标」（需后端配合 last-key 过滤），
// 业务调用方无感知。
package store

import (
	"encoding/base64"
	"strconv"

	storev1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/store/v1"
)

// Paginator 把 PagingRequest 解析为后端可用的 offset/limit。
// 实现应返回非负 offset 与 limit；limit<=0 表示「不限制」（全量）。
type Paginator interface {
	// Name 返回策略名（用于日志/调试）。
	Name() string
	// Resolve 计算 offset/limit；defaultSize/maxSize 用于归一化页大小。
	Resolve(req *storev1.PagingRequest, defaultSize, maxSize int) (offset, limit int, err error)
}

// detectStrategy 依据请求字段自动选择分页策略（优先级：NoPaging > Token > Page > Offset > 默认页码）。
func detectStrategy(req *storev1.PagingRequest) Paginator {
	if req.GetNoPaging() {
		return noPaginator{}
	}
	if tok := req.GetToken(); tok != "" {
		return tokenPaginator{}
	}
	if req.Page != nil {
		return pagePaginator{}
	}
	if req.Offset != nil || req.Limit != nil {
		return offsetPaginator{}
	}
	return pagePaginator{}
}

// pagePaginator 页码分页：Page/PageSize → (Page-1)*Size, Size。
type pagePaginator struct{}

func (pagePaginator) Name() string { return "page" }

func (pagePaginator) Resolve(req *storev1.PagingRequest, defaultSize, maxSize int) (int, int, error) {
	ps := clampPageSize(int(req.GetPageSize()), defaultSize, maxSize)
	page := int(req.GetPage())
	if page < 1 {
		page = 1
	}
	return (page - 1) * ps, ps, nil
}

// offsetPaginator 偏移分页：直接用 Offset/Limit。
type offsetPaginator struct{}

func (offsetPaginator) Name() string { return "offset" }

func (offsetPaginator) Resolve(req *storev1.PagingRequest, defaultSize, maxSize int) (int, int, error) {
	limit := clampPageSize(int(req.GetLimit()), defaultSize, maxSize)
	if req.Limit == nil {
		limit = defaultSize // 仅给 Offset 时按默认页大小限制
	}
	return int(req.GetOffset()), limit, nil
}

// tokenPaginator 游标分页：Token 为偏移游标（base64 或直接十进制）。
// 未带 token 视为从头（offset=0）；limit 取 PageSize 或默认。
type tokenPaginator struct{}

func (tokenPaginator) Name() string { return "token" }

func (tokenPaginator) Resolve(req *storev1.PagingRequest, defaultSize, maxSize int) (int, int, error) {
	limit := clampPageSize(int(req.GetPageSize()), defaultSize, maxSize)
	raw := req.GetToken()
	if raw == "" {
		return 0, limit, nil
	}
	// 兼容两种编码：尝试 base64 解码，失败则按十进制偏移解析（向后兼容）。
	decoded := raw
	if b, err := base64.StdEncoding.DecodeString(raw); err == nil && len(b) > 0 {
		decoded = string(b)
	}
	off, err := strconv.ParseInt(decoded, 10, 64)
	if err != nil {
		return 0, 0, ErrInvalidToken
	}
	if off < 0 {
		off = 0
	}
	return int(off), limit, nil
}

// noPaginator 不分页：返回 offset=0, limit=0（limit<=0 由后端解释为全量）。
type noPaginator struct{}

func (noPaginator) Name() string { return "none" }

func (noPaginator) Resolve(_ *storev1.PagingRequest, _, _ int) (int, int, error) {
	return 0, 0, nil
}

// encodeToken 把下一页偏移量编码为 token（base64）。供 ListWithPaging 填充 NextToken。
func encodeToken(offset int) string {
	if offset <= 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}
