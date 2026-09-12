package validation

import (
	"strings"
	"testing"
)

// TestValidRequired：必填字段非空检查（R4 复审补课——校验设计验收标准 #2
// 自评审以来从未交付，零调用能力以测试钉住行为防静默腐烂）。
// isEmpty 语义：nil / 空串 / 空 slice / 空 map / nil 指针视为空；
// 零值标量（int 0 / bool false）不算空。
func TestValidRequired(t *testing.T) {
	tests := []struct {
		name    string
		fields  map[string]any
		wantErr string // 空串表示应通过
	}{
		{
			name:   "全部非空通过",
			fields: map[string]any{"name": "bob", "age": 18},
		},
		{
			name:   "空 map 通过",
			fields: map[string]any{},
		},
		{
			name:    "nil 值报错",
			fields:  map[string]any{"name": nil, "age": 18},
			wantErr: `field "name" is required`,
		},
		{
			name:    "空字符串报错",
			fields:  map[string]any{"name": "", "age": 18},
			wantErr: `field "name" is required`,
		},
		{
			name:    "空切片报错",
			fields:  map[string]any{"tags": []string{}, "age": 18},
			wantErr: `field "tags" is required`,
		},
		{
			name:    "空 map 值报错",
			fields:  map[string]any{"meta": map[string]any{}, "age": 18},
			wantErr: `field "meta" is required`,
		},
		{
			name:    "nil 指针报错",
			fields:  map[string]any{"req": (*struct{})(nil), "age": 18},
			wantErr: `field "req" is required`,
		},
		{
			name:   "零值 int 不算空",
			fields: map[string]any{"age": 0},
		},
		{
			name:   "false bool 不算空",
			fields: map[string]any{"ok": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidRequired(tt.fields)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidRequired() 不应报错, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidRequired() 应报错 %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("错误信息 = %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

// TestValidRange：数值/字符串长度区间检查（验收 #2：各类型边界）。
func TestValidRange(t *testing.T) {
	tests := []struct {
		name    string
		value   any
		min     any
		max     any
		wantErr bool
	}{
		{name: "int 区间内", value: 5, min: 1, max: 10},
		{name: "int 下边界含", value: 1, min: 1, max: 10},
		{name: "int 上边界含", value: 10, min: 1, max: 10},
		{name: "int 低于下界", value: 0, min: 1, max: 10, wantErr: true},
		{name: "int 超过上限", value: 11, min: 1, max: 10, wantErr: true},
		{name: "int64 区间内", value: int64(50), min: int64(10), max: int64(100)},
		{name: "int64 超限", value: int64(101), min: int64(10), max: int64(100), wantErr: true},
		{name: "float64 区间内", value: 1.5, min: 0.0, max: 2.0},
		{name: "float64 超限", value: 2.1, min: 0.0, max: 2.0, wantErr: true},
		{name: "string 长度区间内", value: "abcd", min: 2, max: 8},
		{name: "string 过短", value: "a", min: 2, max: 8, wantErr: true},
		{name: "string 过长", value: "abcdefghi", min: 2, max: 8, wantErr: true},
		{name: "边界值用字符串数字（宽松转换）", value: "abc", min: "1", max: "5"},
		{name: "未支持类型跳过（bool 放行）", value: true, min: 0, max: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidRange("f", tt.value, tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidRange(value=%v, min=%v, max=%v) error = %v, wantErr %v",
					tt.value, tt.min, tt.max, err, tt.wantErr)
			}
		})
	}
}

// TestValidPattern：prefix/suffix/contains/len 规则（验收 #2：规则组合）。
// 契约：返回首个不匹配错误；非法规则（无冒号/未知 op）静默跳过不报错。
func TestValidPattern(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		rules   []string
		wantErr string
	}{
		{name: "prefix 匹配", value: "svc-foo", rules: []string{"prefix:svc-"}},
		{
			name:    "prefix 不匹配",
			value:   "web-foo",
			rules:   []string{"prefix:svc-"},
			wantErr: `field "name" must start with "svc-"`,
		},
		{name: "suffix 匹配", value: "a.jpeg", rules: []string{"suffix:.jpeg"}},
		{
			name:    "suffix 不匹配",
			value:   "a.png",
			rules:   []string{"suffix:.jpeg"},
			wantErr: `field "name" must end with ".jpeg"`,
		},
		{name: "contains 匹配", value: "user@example.com", rules: []string{"contains:@"}},
		{
			name:    "contains 不匹配",
			value:   "user-at-example",
			rules:   []string{"contains:@"},
			wantErr: `field "name" must contain "@"`,
		},
		{name: "len 区间内", value: "abcd", rules: []string{"len:2,8"}},
		{
			name:    "len 过短",
			value:   "a",
			rules:   []string{"len:2,8"},
			wantErr: `field "name" length must be [2, 8]`,
		},
		{
			name:    "len 过长",
			value:   "abcdefghi",
			rules:   []string{"len:2,8"},
			wantErr: `field "name" length must be [2, 8]`,
		},
		{name: "多规则组合全过", value: "svc-prod-01", rules: []string{"prefix:svc-", "contains:prod", "len:4,16"}},
		{
			name:    "多规则组合返回首个错误",
			value:   "x",
			rules:   []string{"suffix:01", "len:4,16"},
			wantErr: `field "name" must end with "01"`,
		},
		{name: "无冒号规则静默跳过", value: "anything", rules: []string{"badrule"}},
		{name: "未知 op 静默跳过", value: "anything", rules: []string{"regex:^a$"}},
		{name: "无规则放行", value: "anything"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidPattern("name", tt.value, tt.rules...)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidPattern() 不应报错, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidPattern() 应报错 %q, got nil", tt.wantErr)
			}
			if !strings.Contains(err.Error(), strings.Trim(tt.wantErr, "`")) {
				t.Errorf("错误信息 = %q, want 包含 %q", err.Error(), tt.wantErr)
			}
		})
	}
}
