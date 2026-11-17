package store

import (
	"encoding/base64"
	"math"
	"testing"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// W-10 / J4：total 首次越过 uint32 边界时，TotalPages 不得回绕成 0。
// fillTotal 可直接单测（total 由 Queryable 返回，无需真的造 2^32 条记录）。
func TestFillTotal_TotalPagesDoesNotWrapWhenTotalExceedsUint32(t *testing.T) {
	meta := &storev1.PaginationResponseMeta{PageSize: u32p(10)}
	total := int64(math.MaxUint32) + 1 // 2^32

	fillTotal(meta, total, 0, &Where{}, options{pageSize: testDefSize, maxSize: testMaxSize})

	require.NotNil(t, meta.GetTotalPages())
	// (2^32 + 9) / 10 = 429496729；旧代码 uint32(total)==0 会得到 0 页。
	assert.Equal(t, uint32((math.MaxUint32+1+9)/10), meta.GetTotalPages().GetValue())
	assert.Equal(t, uint64(math.MaxUint32)+1, meta.GetTotal().GetValue())
}

// W-10 / J5：算出的页数超出 TotalPages 字段（uint32）值域时 clamp 到 MaxUint32，
// 而不是回绕成小值。用 PageSize=1 让页数等于 total，直接触发 clamp 分支。
func TestFillTotal_TotalPagesClampsToUint32Max(t *testing.T) {
	meta := &storev1.PaginationResponseMeta{PageSize: u32p(1)}
	total := int64(math.MaxUint32) + 100

	fillTotal(meta, total, 0, &Where{}, options{pageSize: testDefSize, maxSize: testMaxSize})

	require.NotNil(t, meta.GetTotalPages())
	assert.Equal(t, uint32(math.MaxUint32), meta.GetTotalPages().GetValue())
}

// 正常分页行为不回归：向上取整规则保持不变。
func TestFillTotal_TotalPagesNormalPagingUnchanged(t *testing.T) {
	tests := []struct {
		total int64
		want  uint32
	}{
		{0, 0},
		{1, 1},
		{10, 1},
		{11, 2},
		{95, 10},
		{100, 10},
		{101, 11},
	}
	for _, tt := range tests {
		meta := &storev1.PaginationResponseMeta{PageSize: u32p(10)}
		fillTotal(meta, tt.total, 0, &Where{}, options{pageSize: testDefSize, maxSize: testMaxSize})
		require.NotNil(t, meta.GetTotalPages())
		assert.Equal(t, tt.want, meta.GetTotalPages().GetValue(), "total=%d", tt.total)
	}
}

// W-10：proto Offset 为 uint64，超出平台 int 值域必须报错，不能静默回绕成负数。
func TestPaging_OffsetStrategy_RejectsOffsetOverflow(t *testing.T) {
	p := offsetPaginator{}
	for _, off := range []uint64{uint64(math.MaxInt) + 1, math.MaxUint64} {
		_, _, err := p.Resolve(&storev1.PagingRequest{Offset: u64p(off)}, testDefSize, testMaxSize)
		assert.ErrorIs(t, err, ErrInvalidToken, "offset=%d", off)
	}

	// 恰好等于平台 int 上限仍可用（边界值放行，与 bconf coerceInt 一致）。
	off, limit, err := p.Resolve(&storev1.PagingRequest{
		Offset: u64p(uint64(math.MaxInt)),
		Limit:  u32p(15),
	}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, math.MaxInt, off)
	assert.Equal(t, 15, limit)
}

// W-10：token 解析出的偏移超过 int64 值域时报错，而非静默截断。
func TestPaging_TokenStrategy_RejectsOverflowingOffset(t *testing.T) {
	p := tokenPaginator{}
	const overflowDec = "9223372036854775808" // MaxInt64 + 1

	// 直接十进制。
	_, _, err := p.Resolve(&storev1.PagingRequest{Token: strp(overflowDec)}, testDefSize, testMaxSize)
	assert.ErrorIs(t, err, ErrInvalidToken)

	// base64 编码的十进制走同一 ParseInt 分支，同样拒绝。
	b64 := base64.StdEncoding.EncodeToString([]byte(overflowDec))
	_, _, err = p.Resolve(&storev1.PagingRequest{Token: strp(b64)}, testDefSize, testMaxSize)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

// 正常 token 与既有 off<0 归零守卫不回归。
func TestPaging_TokenStrategy_NormalAndNegativeOffsetUnchanged(t *testing.T) {
	p := tokenPaginator{}

	off, limit, err := p.Resolve(&storev1.PagingRequest{Token: strp("30")}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 30, off)
	assert.Equal(t, testDefSize, limit)

	// 负偏移 token 仍归零：既有 off < 0 守卫保持不变。
	off, _, err = p.Resolve(&storev1.PagingRequest{Token: strp("-5")}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 0, off)
}
