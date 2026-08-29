package validation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// SampleRequest 是导出类型，便于与 ValidateSampleRequest 精确匹配（约定：方法名 =
// "Validate" + 请求类型名）。
type SampleRequest struct {
	Name string
}

type sampleValidator struct{}

func (sampleValidator) ValidateSampleRequest(_ context.Context, req *SampleRequest) error {
	if req.Name == "" {
		return errors.New("name required")
	}
	return nil
}

func TestValidatorDispatch(t *testing.T) {
	v, err := NewValidator(sampleValidator{})
	if err != nil {
		t.Fatalf("NewValidator: %v", err)
	}

	if err := v.Validate(context.Background(), &SampleRequest{Name: "ok"}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if err := v.Validate(context.Background(), &SampleRequest{}); err == nil {
		t.Fatalf("expected error for empty name")
	}
}

func TestValidatorUnregistered(t *testing.T) {
	v, err := NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator(nil) should be legal: %v", err)
	}
	// 未注册类型应直接放行（返回 nil）。
	if err := v.Validate(context.Background(), &struct{ X int }{X: 1}); err != nil {
		t.Fatalf("expected nil for unregistered, got %v", err)
	}
}

// TestRegister_TypoMustError 回归防护（P2 修复）：
// 方法名拼写错误必须返回 error，而不是静默跳过。
//
// 修复前：Register 只打一行 klog.V(4) 日志就 continue，调用方完全无感，
// 结果是「以为加了校验，其实一个都没跑」。这是隐蔽的正确性 bug。
func TestRegister_TypoMustError(t *testing.T) {
	cases := []struct {
		name    string
		val     any
		wantMsg string
	}{
		{
			// 经典拼写错误：Requst 少一个 e。
			name:    "方法名拼写错误",
			val:     typoValidator{},
			wantMsg: `method "ValidateSampleRequst" does not match request type "SampleRequest", want "ValidateSampleRequest"`,
		},
		{
			name:    "第二个参数不是指针",
			val:     nonPointerValidator{},
			wantMsg: "second arg must be a request pointer",
		},
		{
			name:    "返回值不是 error",
			val:     noErrorReturnValidator{},
			wantMsg: "must return error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewValidator(tc.val)
			if err == nil {
				t.Fatalf("NewValidator succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error = %q, want containing %q", err.Error(), tc.wantMsg)
			}
		})
	}
}

// TestRegister_NonValidateMethodIgnored 非 Validate 前缀的方法不应触发校验（也不应报错）。
func TestRegister_NonValidateMethodIgnored(t *testing.T) {
	v, err := NewValidator(otherMethodValidator{})
	if err != nil {
		t.Fatalf("non-Validate methods must be ignored, got error: %v", err)
	}
	if err := v.Validate(context.Background(), &SampleRequest{}); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

// --- 用于 P2 回归测试的错误形态 ---

type typoValidator struct{}

// 拼写错误：SampleRequst（少一个 e）→ 必须与请求类型名不一致，所以必须报错。
func (typoValidator) ValidateSampleRequst(_ context.Context, req *SampleRequest) error {
	return nil
}

type nonPointerValidator struct{}

func (nonPointerValidator) ValidateSampleRequest(_ context.Context, req SampleRequest) error {
	return nil
}

type noErrorReturnValidator struct{}

func (noErrorReturnValidator) ValidateSampleRequest(_ context.Context, req *SampleRequest) {
}

type otherMethodValidator struct{}

func (otherMethodValidator) SomeHelper() {}

func TestRequestTypeName(t *testing.T) {
	if got := RequestTypeName(&SampleRequest{}); got != "SampleRequest" {
		t.Fatalf("expected SampleRequest, got %q", got)
	}
}
