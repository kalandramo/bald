// Package storeerr 提供存储层哨兵错误到传输层业务错误（berrors.Error）的桥接。
//
// store 包定义的是存储领域哨兵（ErrNotFound/ErrConflict/ErrInvalidToken），
// 它们可被 fmt.Errorf 的 %w 包装；berrors 定义的是传输级别类别码与领域 Reason。
// 本包只负责「类别映射」：把哨兵翻译成对应的 *berrors.Error，便于上层统一处理。
//
// 刻意不填 Message/Cause——那些是传输面的展示与诊断语义，应由调用方按场景决定，
// 桥接层不越俎代庖。
package storeerr

import (
	"errors"

	"github.com/kalandramo/bald/berrors"
	"github.com/kalandramo/bald/pkg/store"
)

// ToBError 把 store 哨兵错误映射为对应类别的 *berrors.Error：
//   - ErrNotFound      → berrors.NotFound(reason)
//   - ErrConflict      → berrors.AlreadyExists(reason)
//   - ErrInvalidToken  → berrors.BadRequest(reason)
//
// 使用 errors.Is 判定，故 fmt.Errorf("wrap: %w", ErrNotFound) 等包装错误仍可识别。
// 非哨兵错误（含已是 *berrors.Error 的错误）返回 (nil, false)，由调用方决定是否透传。
func ToBError(err error, reason string) (*berrors.Error, bool) {
	if err == nil {
		return nil, false
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		return berrors.NotFound(reason), true
	case errors.Is(err, store.ErrConflict):
		return berrors.AlreadyExists(reason), true
	case errors.Is(err, store.ErrInvalidToken):
		return berrors.BadRequest(reason), true
	}
	return nil, false
}
