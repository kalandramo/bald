package validation

import (
	"context"
	"strings"
	"testing"
)

// --- Register 约定校验的剩余错误形态 ---

type wrongArgCountValidator struct{}

// 只有 (recv, ctx) 两个入参，缺请求指针 → NumIn()!=3 必须报错。
func (wrongArgCountValidator) ValidateSampleRequest(_ context.Context) error { return nil }

type wrongFirstArgValidator struct{}

// 入参数正确但首参不是 context.Context → 必须报错。
func (wrongFirstArgValidator) ValidateSampleRequest(_ string, _ *SampleRequest) error { return nil }

func TestRegister_WrongSignature(t *testing.T) {
	cases := []struct {
		name    string
		val     any
		wantMsg string
	}{
		{"入参数不符", wrongArgCountValidator{}, "must have signature"},
		{"首参非 context", wrongFirstArgValidator{}, "first arg must be context.Context"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewValidator(tc.val)
			if err == nil {
				t.Fatal("NewValidator succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// --- Validate / RequestTypeName 的 nil 与值类型边界 ---

func TestValidate_NilRequest(t *testing.T) {
	v, err := NewValidator(sampleValidator{})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}
	if err := v.Validate(context.Background(), nil); err != nil {
		t.Errorf("Validate(nil) = %v, want nil", err)
	}
}

func TestRequestTypeName_NilAndValue(t *testing.T) {
	if got := RequestTypeName(nil); got != "" {
		t.Errorf("RequestTypeName(nil) = %q, want empty", got)
	}
	// 值类型（非指针）也应返回类型名。
	if got := RequestTypeName(SampleRequest{}); got != "SampleRequest" {
		t.Errorf("RequestTypeName(value) = %q, want SampleRequest", got)
	}
}

// --- 数值转换助手的类型分支 ---

func TestToInt(t *testing.T) {
	tests := []struct {
		in   any
		want int
	}{
		{7, 7},
		{int64(8), 8},
		{float64(9.9), 9},
		{"10", 10},
		{"not-a-number", 0},
		{true, 0},
	}
	for _, tt := range tests {
		if got := toInt(tt.in); got != tt.want {
			t.Errorf("toInt(%v/%T) = %d, want %d", tt.in, tt.in, got, tt.want)
		}
	}
}

func TestToInt64(t *testing.T) {
	tests := []struct {
		in   any
		want int64
	}{
		{int64(8), 8},
		{7, 7},
		{float64(9.9), 9},
		{true, 0},
	}
	for _, tt := range tests {
		if got := toInt64(tt.in); got != tt.want {
			t.Errorf("toInt64(%v/%T) = %d, want %d", tt.in, tt.in, got, tt.want)
		}
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		in   any
		want float64
	}{
		{1.5, 1.5},
		{7, 7.0},
		{int64(8), 8.0},
		{"3.14", 3.14},
		{"bad", 0.0},
		{true, 0.0},
	}
	for _, tt := range tests {
		if got := toFloat64(tt.in); got != tt.want {
			t.Errorf("toFloat64(%v/%T) = %v, want %v", tt.in, tt.in, got, tt.want)
		}
	}
}
