package store

import (
	"errors"

	wrapperspb "google.golang.org/protobuf/types/known/wrapperspb"
)

// 存储层哨兵错误。
var (
	// ErrNotFound 表示按条件未命中记录。
	ErrNotFound = errors.New("store: record not found")
	// ErrInvalidToken 表示令牌分页的游标非法。
	ErrInvalidToken = errors.New("store: invalid paging token")
	// ErrConflict 表示唯一键冲突（插入/更新时）。
	ErrConflict = errors.New("store: unique constraint conflict")
)

// 小工具：构造 proto wrapper 指针，供填写 PaginationResponseMeta 的 optional 字段。
// 注意生成代码中 Total/TotalPages/CurrentPage/CurrentOffset 为 wrapperspb 类型，
// 而 NextToken/PageSize/CurrentSize 为原生指针（proto3 optional scalar），故分别提供。
func uint32Ptr(v uint32) *wrapperspb.UInt32Value { return wrapperspb.UInt32(v) }
func uint64Ptr(v uint64) *wrapperspb.UInt64Value { return wrapperspb.UInt64(v) }
func uint32p(v uint32) *uint32                  { return &v }
func strp(v string) *string                     { return &v }
