package grpc

// errors.go gRPC service 共享的错误归一化 helper。
//
// biz 层错误经 fmt.Errorf %w 包装 store 哨兵错误（ErrNotFound/ErrConflict）或
// berrors（校验/前置条件），此处按语义归一化后交 ErrorInterceptor 统一转 gRPC
// status——避免各 service 把内部错误一刀切折叠成 NotFound（掩盖故障、误导排障）。

import (
	"errors"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/store"
)

// notFoundOr 把 store.ErrNotFound 归一化为 berrors.NotFound(reason)——
// reason 用「域/稳定标识」风格（如 "user/not_found"），message 与 gin 面
// 同形（"%s not found"，readable 为人读资源名），三面一份契约。
// 其余错误透传（berrors 保持原 code；未知错误由 ErrorInterceptor 兜底 Unknown）。
func notFoundOr(err error, reason, readable string) error {
	if errors.Is(err, store.ErrNotFound) {
		return berrors.NotFound(reason).WithMessage("%s not found", readable)
	}
	return err
}
