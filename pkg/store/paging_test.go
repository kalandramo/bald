package store

import (
	"context"
	"testing"

	storev1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/store/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func boolp(b bool) *bool { return &b }

const testDefSize = 10
const testMaxSize = 100

func TestPaging_PageStrategy(t *testing.T) {
	p := pagePaginator{}
	// page=2, size=10 → offset 10, limit 10
	off, limit, err := p.Resolve(&storev1.PagingRequest{
		Page:     u32p(2),
		PageSize: u32p(10),
	}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 10, off)
	assert.Equal(t, 10, limit)

	// page<1 归正为 1
	off, _, _ = p.Resolve(&storev1.PagingRequest{Page: u32p(0), PageSize: u32p(5)}, testDefSize, testMaxSize)
	assert.Equal(t, 0, off)

	// 超过 maxSize 被截断
	_, limit, _ = p.Resolve(&storev1.PagingRequest{PageSize: u32p(999)}, testDefSize, testMaxSize)
	assert.Equal(t, testMaxSize, limit)
}

func TestPaging_OffsetStrategy(t *testing.T) {
	p := offsetPaginator{}
	off, limit, err := p.Resolve(&storev1.PagingRequest{
		Offset: u64p(20),
		Limit:  u32p(15),
	}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 20, off)
	assert.Equal(t, 15, limit)
}

func TestPaging_TokenStrategy(t *testing.T) {
	p := tokenPaginator{}
	// 空 token → 从头
	off, _, err := p.Resolve(&storev1.PagingRequest{}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 0, off)

	// 十进制偏移 token（向后兼容）
	off, limit, err := p.Resolve(&storev1.PagingRequest{Token: strp("30")}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 30, off)
	assert.Equal(t, testDefSize, limit)

	// base64 偏移 token
	off, _, _ = p.Resolve(&storev1.PagingRequest{Token: strp(encodeToken(40))}, testDefSize, testMaxSize)
	assert.Equal(t, 40, off)

	// 非法 token → ErrInvalidToken
	_, _, err = p.Resolve(&storev1.PagingRequest{Token: strp("not-a-number")}, testDefSize, testMaxSize)
	assert.ErrorIs(t, err, ErrInvalidToken)
}

func TestPaging_NoStrategy(t *testing.T) {
	_, limit, err := noPaginator{}.Resolve(&storev1.PagingRequest{}, testDefSize, testMaxSize)
	require.NoError(t, err)
	assert.Equal(t, 0, limit) // limit<=0 表示全量
}

func TestPaging_DetectStrategy(t *testing.T) {
	assert.Equal(t, "none", detectStrategy(&storev1.PagingRequest{NoPaging: boolp(true)}).Name())
	assert.Equal(t, "token", detectStrategy(&storev1.PagingRequest{Token: strp("5")}).Name())
	assert.Equal(t, "page", detectStrategy(&storev1.PagingRequest{Page: u32p(1)}).Name())
	assert.Equal(t, "offset", detectStrategy(&storev1.PagingRequest{Offset: u64p(1)}).Name())
	assert.Equal(t, "page", detectStrategy(&storev1.PagingRequest{}).Name()) // 默认页码
}

func TestFlatten_RejectsOR(t *testing.T) {
	// 顶层 OR：扁平 translate 不支持，应报错而非静默降级为 AND。
	_, err := flatten(&storev1.FilterExpr{
		Type:       storev1.FilterExpr_OR,
		Conditions: []*storev1.FilterCondition{{Field: "name", Op: storev1.Operator_EQ, Value: "a"}},
	})
	assert.Error(t, err)

	// 嵌套 OR 同样应报错。
	_, err = flatten(&storev1.FilterExpr{
		Type: storev1.FilterExpr_AND,
		Groups: []*storev1.FilterExpr{
			{Type: storev1.FilterExpr_OR, Conditions: []*storev1.FilterCondition{{Field: "x", Op: storev1.Operator_EQ, Value: "1"}}},
		},
	})
	assert.Error(t, err)

	// 纯 AND 可正常展平。
	out, err := flatten(&storev1.FilterExpr{
		Type:       storev1.FilterExpr_AND,
		Conditions: []*storev1.FilterCondition{{Field: "a", Op: storev1.Operator_EQ, Value: "1"}},
		Groups: []*storev1.FilterExpr{
			{Type: storev1.FilterExpr_AND, Conditions: []*storev1.FilterCondition{{Field: "b", Op: storev1.Operator_EQ, Value: "2"}}},
		},
	})
	require.NoError(t, err)
	assert.Len(t, out, 2)
}

func TestPaging_TranslateMetadata(t *testing.T) {
	s := NewStore[int](nil) // provider 不参与分页元数据计算，传 nil 不影响 translate
	// 页码分页元数据
	where, meta, err := s.translate(context.Background(), &storev1.PagingRequest{Page: u32p(2), PageSize: u32p(10)})
	require.NoError(t, err)
	assert.Equal(t, uint32(2), meta.GetCurrentPage().GetValue())
	assert.Equal(t, uint32(10), meta.GetPageSize())
	// translate 阶段不知道总数，NextToken 不预填（避免末页误发空翻页）。
	assert.Empty(t, meta.GetNextToken())

	// 偏移分页元数据
	where, meta, err = s.translate(context.Background(), &storev1.PagingRequest{Offset: u64p(20), Limit: u32p(5)})
	require.NoError(t, err)
	assert.Equal(t, uint64(20), meta.GetCurrentOffset().GetValue())
	assert.Equal(t, uint32(5), meta.GetPageSize())
	assert.Empty(t, meta.GetNextToken())

	// fillTotal：还有下一页时下发 NextToken，末页不下发。
	opts := options{pageSize: testDefSize, maxSize: testMaxSize}
	fillTotal(meta, 100, where, opts)
	assert.Equal(t, uint64(100), meta.GetTotal().GetValue())
	assert.NotEmpty(t, meta.GetNextToken()) // 20+5 < 100 → 有下一页

	lastWhere, lastMeta, err := s.translate(context.Background(), &storev1.PagingRequest{Offset: u64p(95), Limit: u32p(10)})
	require.NoError(t, err)
	fillTotal(lastMeta, 100, lastWhere, opts)
	assert.Empty(t, lastMeta.GetNextToken()) // 95+10 >= 100 → 末页，不下发

	// 不分页无元数据
	_, meta, err = s.translate(context.Background(), &storev1.PagingRequest{NoPaging: boolp(true)})
	require.NoError(t, err)
	assert.Nil(t, meta.GetCurrentPage())
}

// 小工具
func u32p(v uint32) *uint32 { return &v }
func u64p(v uint64) *uint64 { return &v }
