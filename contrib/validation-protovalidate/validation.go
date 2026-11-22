// Package validation 提供 protovalidate 到 berrors 的官方桥接。
//
// 把 protovalidate 的 *protovalidate.ValidationError 违规映射为框架统一的
// *berrors.Error（CodeInvalidArgument），使参数校验失败能被 gRPC
// ErrorInterceptor / HTTP writeBizErr 统一收口为 400；非校验类错误原样透传，不吞。
package validation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"buf.build/go/protovalidate"
	"google.golang.org/protobuf/proto"

	"github.com/kalandramo/bald/berrors"
)

// Validator 包装 protovalidate.Validator，提供框架风格的错误出口。
type Validator struct {
	pv protovalidate.Validator
}

// New 构造并预热校验器。
//
// 用 protovalidate.New() 预编译注解中的 CEL 表达式，之后每次校验复用编译结果；
// 构造期即暴露注解编译错误（fail fast）。options 原样透传给 protovalidate.New。
func New(options ...protovalidate.ValidatorOption) (*Validator, error) {
	pv, err := protovalidate.New(options...)
	if err != nil {
		return nil, fmt.Errorf("validation: build protovalidate: %w", err)
	}
	return &Validator{pv: pv}, nil
}

// MustNew 同 New，但构造失败时 panic（供 main 装配期使用——注解写错应当启动即失败）。
func MustNew(options ...protovalidate.ValidatorOption) *Validator {
	v, err := New(options...)
	if err != nil {
		panic(err)
	}
	return v
}

// Validate 校验 msg 的 buf.validate 注解规则。
//
// 非 proto.Message 入参直接放行：gRPC 拦截器会对所有请求调用本方法，需容错。
// 返回 *berrors.Error（CodeInvalidArgument）表示有违规；其他错误原样透传。
func (v *Validator) Validate(_ context.Context, msg any) error {
	m, ok := msg.(proto.Message)
	if !ok {
		return nil
	}
	err := v.pv.Validate(m)
	if err == nil {
		return nil
	}
	return MapValidationError(err)
}

// MapValidationError 把 protovalidate 违规映射为框架错误模型。
//
// protovalidate 默认累积全部违规（非 fail-fast），这里把所有 violation 聚合
// 成一条可读消息，便于调用方一次看全。非 *protovalidate.ValidationError
// （如注解编译错误 CompilationError）原样透传，不吞——避免把框架级故障
// 伪装成参数问题。
func MapValidationError(err error) error {
	var valErr *protovalidate.ValidationError
	if !errors.As(err, &valErr) {
		return err
	}
	details := make([]string, 0, len(valErr.Violations))
	for _, v := range valErr.Violations {
		field := "<unknown>"
		if v.FieldDescriptor != nil {
			field = string(v.FieldDescriptor.Name())
		}
		details = append(details, fmt.Sprintf("%s: %s", field, v.Proto.GetMessage()))
	}
	return berrors.BadRequest("INVALID_ARGUMENT").
		WithMessage("请求参数校验失败: %s", strings.Join(details, "; "))
}
