package validation

import (
	"context"
	"errors"
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
	v := NewValidator(sampleValidator{})

	if err := v.Validate(context.Background(), &SampleRequest{Name: "ok"}); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if err := v.Validate(context.Background(), &SampleRequest{}); err == nil {
		t.Fatalf("expected error for empty name")
	}
}

func TestValidatorUnregistered(t *testing.T) {
	v := NewValidator(nil)
	// 未注册类型应直接放行（返回 nil）。
	if err := v.Validate(context.Background(), &struct{ X int }{X: 1}); err != nil {
		t.Fatalf("expected nil for unregistered, got %v", err)
	}
}

func TestRequestTypeName(t *testing.T) {
	if got := RequestTypeName(&SampleRequest{}); got != "SampleRequest" {
		t.Fatalf("expected SampleRequest, got %q", got)
	}
}
