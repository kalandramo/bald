package validation

import (
	"context"
	"errors"
	"strings"
	"testing"

	validate "buf.build/gen/go/bufbuild/protovalidate/protocolbuffers/go/buf/validate"
	"buf.build/go/protovalidate"
	"github.com/kalandramo/bald/berrors"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
)

// TestValidateNonProtoPassThrough 验证非 proto 消息直接放行。
func TestValidateNonProtoPassThrough(t *testing.T) {
	v := MustNew()
	for _, tc := range []struct {
		name string
		msg  any
	}{
		{"string", "not-a-proto"},
		{"int", 42},
		{"struct", struct{ A int }{A: 1}},
		{"nil", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := v.Validate(context.Background(), tc.msg); err != nil {
				t.Fatalf("Validate(%T) 期望放行，得到 error: %v", tc.msg, err)
			}
		})
	}
}

// TestValidateProtoNoViolation 验证无注解的 proto 消息校验通过。
func TestValidateProtoNoViolation(t *testing.T) {
	v := MustNew()
	if err := v.Validate(context.Background(), &descriptorpb.FileDescriptorProto{}); err != nil {
		t.Fatalf("无注解 proto 消息期望放行，得到 error: %v", err)
	}
}

// TestMapValidationError 验证违规被映射为 berrors.BadRequest。
func TestMapValidationError(t *testing.T) {
	nameField := (&descriptorpb.FileDescriptorProto{}).ProtoReflect().
		Descriptor().Fields().ByName("name")
	if nameField == nil {
		t.Fatal("无法获取测试用 FieldDescriptor")
	}

	err := &protovalidate.ValidationError{
		Violations: []*protovalidate.Violation{
			{
				Proto:           &validate.Violation{Message: proto.String("名字不能为空")},
				FieldDescriptor: nameField,
			},
			{
				Proto: &validate.Violation{Message: proto.String("值不合法")},
				// FieldDescriptor 为 nil，走 <unknown> 分支
			},
		},
	}

	got := MapValidationError(err)
	var berr *berrors.Error
	if !errors.As(got, &berr) {
		t.Fatalf("期望 *berrors.Error，得到 %T: %v", got, got)
	}
	if berr.Code != berrors.CodeInvalidArgument {
		t.Errorf("Code = %d, 期望 %d", berr.Code, berrors.CodeInvalidArgument)
	}
	if berr.Reason != "INVALID_ARGUMENT" {
		t.Errorf("Reason = %q, 期望 %q", berr.Reason, "INVALID_ARGUMENT")
	}
	for _, want := range []string{
		"请求参数校验失败",
		"name: 名字不能为空",
		"<unknown>: 值不合法",
	} {
		if !strings.Contains(berr.Message, want) {
			t.Errorf("Message = %q, 期望包含 %q", berr.Message, want)
		}
	}
}

// TestMapValidationErrorPassThrough 验证非 ValidationError 原样透传（同一指针）。
func TestMapValidationErrorPassThrough(t *testing.T) {
	orig := errors.New("compilation failed")
	got := MapValidationError(orig)
	if got != orig {
		t.Fatalf("期望原样透传同一指针，得到 %v", got)
	}
}
