package circuitbreaker

import (
	"context"
	"errors"
	"testing"

	"github.com/kalandramo/bald/berrors"
)

// TestIsFailureCode 表驱动钉住失败口径：只有服务侧的错算失败；客户端侧
// （4xx 族）与限流自答（ResourceExhausted）不算——健康节点不得被业务 4xx
// 硬排除，熔断不得消费限流的拒流（AR-18 单一来源）。
func TestIsFailureCode(t *testing.T) {
	for _, c := range []struct {
		code uint32
		want bool
	}{
		{berrors.CodeInternal, true},
		{berrors.CodeUnknown, true},
		{berrors.CodeUnavailable, true},
		{berrors.CodeDeadlineExceeded, true},
		{berrors.CodeDataLoss, true},
		{berrors.CodeNotFound, false},
		{berrors.CodeInvalidArgument, false},
		{berrors.CodeResourceExhausted, false},
	} {
		if got := IsFailureCode(c.code); got != c.want {
			t.Errorf("IsFailureCode(%d) = %v, want %v", c.code, got, c.want)
		}
	}
}

// TestIsFailureError 完整分类链：berrors 主形态、context 两哨兵、原生兜底。
func TestIsFailureError(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"原生未知错误", errors.New("boom"), true},
		{"context.Canceled（客户端撤单）", context.Canceled, false},
		{"context.DeadlineExceeded（服务超预算）", context.DeadlineExceeded, true},
		{"berrors Unavailable", berrors.New(berrors.CodeUnavailable, "R"), true},
		{"berrors NotFound（4xx 不算失败）", berrors.New(berrors.CodeNotFound, "R"), false},
		{"包一层仍按 Code 分类", errors.Join(berrors.New(berrors.CodeNotFound, "R")), false},
	} {
		if got := IsFailureError(c.err); got != c.want {
			t.Errorf("IsFailureError(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}
