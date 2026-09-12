package fieldmaskutil

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	storev1 "github.com/kalandramo/bald/bconf/gen/go/bald/store/v1"
)

func Test_NestedMaskFromPaths(t *testing.T) {
	type args struct {
		paths []string
	}
	tests := []struct {
		name string
		args args
		want NestedMask
	}{
		{
			name: "no nested fields",
			args: args{paths: []string{"a", "b", "c"}},
			want: NestedMask{"a": nil, "b": nil, "c": nil},
		},
		{
			name: "with nested fields",
			args: args{paths: []string{"aaa.bb.c", "dd.e", "f"}},
			want: NestedMask{
				"aaa": NestedMask{"bb": NestedMask{"c": nil}},
				"dd":  NestedMask{"e": nil},
				"f":   nil},
		},
		{
			name: "single field",
			args: args{paths: []string{"a"}},
			want: NestedMask{"a": nil},
		},
		{
			name: "empty fields",
			args: args{paths: []string{}},
			want: NestedMask{},
		},
		{
			name: "invalid input",
			args: args{paths: []string{".", "..", "...", ".a.", ""}},
			want: NestedMask{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NestedMaskFromPaths(tt.args.paths); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("NestedMaskFromPaths() = %v, want %v", got, tt.want)
			}
		})
	}
}

// Test_FilterByFieldMask 用真实 proto 消息（bconf storev1.PagingRequest）验证：
// mask 之外的字段被清空，mask 内字段保留。
func Test_FilterByFieldMask(t *testing.T) {
	var msg proto.Message = &storev1.PagingRequest{Token: strp("cursor-1"), OrderBy: strp("id,-created_at")}

	fm, err := fieldmaskpb.New(&storev1.PagingRequest{}, "token")
	assert.NoError(t, err)
	assert.NoError(t, FilterByFieldMask(&msg, fm))

	got := msg.(*storev1.PagingRequest)
	assert.Equal(t, "cursor-1", got.GetToken())
	assert.Empty(t, got.GetOrderBy(), "mask 外字段应被清空")
}

// Test_NormalizeFieldMaskPaths 验证 camelCase 路径归一化为 snake_case（含 id_ 特例；
// fieldmaskpb.Normalize 官方行为会排序去重，故断言按字母序）。
func Test_NormalizeFieldMaskPaths(t *testing.T) {
	fm := &fieldmaskpb.FieldMask{Paths: []string{"pageSize", "id_"}}
	NormalizeFieldMaskPaths(fm)
	assert.Equal(t, []string{"id", "page_size"}, fm.GetPaths())
}

// Test_ValidateFieldMask 验证非法路径报错、nil mask 放行。
func Test_ValidateFieldMask(t *testing.T) {
	msg := &storev1.PagingRequest{}
	assert.NoError(t, ValidateFieldMask(msg, nil))
	valid, err := fieldmaskpb.New(msg, "token")
	assert.NoError(t, err)
	assert.NoError(t, ValidateFieldMask(msg, valid))
	assert.Error(t, ValidateFieldMask(msg, &fieldmaskpb.FieldMask{Paths: []string{"not_exist"}}))
}

// Test_PathsFromFieldNumbers 验证字段号转字段名（未知号跳过）。
func Test_PathsFromFieldNumbers(t *testing.T) {
	msg := &storev1.PagingRequest{}
	assert.Equal(t, []string{"page", "page_size"}, PathsFromFieldNumbers(msg, 1, 2))
	assert.Nil(t, PathsFromFieldNumbers(msg))
	assert.Empty(t, PathsFromFieldNumbers(msg, 999))
}

// Test_NilValuePaths 验证 paths 中「Has 为 false」子集的拣出（消费语义：
// bald-crud/entgo update 链据此区分「mask 覆盖但未设值 → 显式置 NULL」与
// 「未出现在 mask → 不动」；bald-crud 消费的是 bald-utils 孪生实现，本测试
// 钉住主模块副本同契约）。
func Test_NilValuePaths(t *testing.T) {
	msg := &storev1.PagingRequest{Token: strp("cursor-1")}

	// 空 paths 直接 nil
	assert.Nil(t, NilValuePaths(msg, nil))

	// 已设置（token）不返回；未设置（order_by/page）返回，顺序保持输入序
	assert.Equal(t, []string{"order_by", "page"}, NilValuePaths(msg, []string{"token", "order_by", "page"}))

	// 全部已设置 → nil
	assert.Nil(t, NilValuePaths(msg, []string{"token"}))

	// 未知字段名跳过（不 panic、不出现在结果）
	assert.Nil(t, NilValuePaths(msg, []string{"not_exist"}))
}

func strp(s string) *string { return &s }
