// Package validation 提供基于反射的「按请求类型名」分发校验能力（移植自 onexstack）。
//
// 约定：业务为每个请求类型 XxxRequest 实现一个方法 ValidateXxxRequest(ctx, *XxxRequest) error，
// 并把它注册到 Validator。每次请求进入时调用 Validator.Validate(ctx, req)，自动按 req 的类型名
// 找到对应校验方法进行校验。这一约定避免了一层抽象（不用把校验注册成 map），且天然支持
// 「先 Bind 后一次性 Validate」的流水线（见 pkg/web 的多源绑定）。
//
// 注意：本包只负责「分发」，不负责「规则表达」。声明式的字段级规则应写在 proto 的
// buf.validate 注解里（由 protovalidate 在运行时读取），见 docs/devel/zh-CN/架构改进路线图.md P6。
// 本包适合承载注解表达不了的复杂校验（查库、权限比对、跨字段动态逻辑）。
package validation

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"k8s.io/klog/v2"
)

// Validator 是注册表 + 分发器：把「请求类型名」映射到「校验函数」。
type Validator struct {
	validators map[string]func(ctx context.Context, req any) error
}

// NewValidator 创建一个校验器；validators 是实现了 ValidateXxx 方法的任意对象（struct 或指针）。
//
// 传 nil 表示「没有校验器」，合法且不注册任何东西。
// 若 validators 上声明了形如 Validate* 的方法但无法满足约定（方法名与请求类型名不符、
// 签名不符等），返回 error —— 见 Register 的说明。
func NewValidator(validators any) (*Validator, error) {
	v := &Validator{validators: make(map[string]func(ctx context.Context, req any) error)}
	if err := v.Register(validators); err != nil {
		return nil, err
	}
	return v, nil
}

// Register 把 obj 上所有形如 Validate<RequestTypeName>(ctx, *Request) error 的方法注册进来。
// 方法名必须是 `Validate` + 请求类型名；第一个参数为 context.Context，第二个参数为请求指针。
//
// 关键行为（P2 修复）：只要方法名以 Validate 开头，就被视为「声明了校验意图」；
// 此时**任何**一项约定不满足都返回 error，而不是静默跳过。
// 这是为防止方法名拼写错误（如 ValidateUserRequst）导致校验完全不生效且无任何提示
// ——这类静默失效在生产环境等同于「没有校验」。
func (v *Validator) Register(validators any) error {
	if validators == nil {
		return nil
	}
	val := reflect.ValueOf(validators)
	typ := val.Type()

	const prefix = "Validate"
	const wantSig = "func(recv T, ctx context.Context, req *XxxRequest) error"

	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		name := method.Name
		if !strings.HasPrefix(name, prefix) {
			continue
		}

		// 到这里说明「有校验意图」，后续每一项都必须满足，否则报错。
		// 各项检查按「从粗到细」排列，使错误信息指向最具体的原因。
		mt := method.Type // 类型：(recv, ctx, *Req) error
		if mt.NumIn() != 3 {
			return fmt.Errorf("validation: method %q must have signature %s, got %s",
				name, wantSig, mt.String())
		}
		if mt.NumOut() != 1 || mt.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			return fmt.Errorf("validation: method %q must return error, got %s", name, mt.String())
		}
		if mt.In(1) != reflect.TypeOf((*context.Context)(nil)).Elem() {
			return fmt.Errorf("validation: method %q first arg must be context.Context, got %s",
				name, mt.In(1))
		}
		reqIn := mt.In(2)
		if reqIn.Kind() != reflect.Ptr {
			return fmt.Errorf("validation: method %q second arg must be a request pointer, got %s",
				name, reqIn)
		}

		reqName := reqIn.Elem().Name()
		if ruleName := name[len(prefix):]; ruleName != reqName {
			return fmt.Errorf("validation: method %q does not match request type %q, want %q",
				name, reqName, prefix+reqName)
		}

		mval := method.Func
		v.validators[reqName] = func(ctx context.Context, req any) error {
			out := mval.Call([]reflect.Value{val, reflect.ValueOf(ctx), reflect.ValueOf(req)})
			if err, ok := out[0].Interface().(error); ok && err != nil {
				return err
			}
			return nil
		}
		klog.V(4).InfoS("Registered validator", "request", reqName)
	}
	return nil
}

// Validate 按请求类型名分发到对应校验方法；未注册则返回 nil（放行）。
func (v *Validator) Validate(ctx context.Context, request any) error {
	if request == nil {
		return nil
	}
	fn, ok := v.validators[RequestTypeName(request)]
	if !ok {
		return nil
	}
	return fn(ctx, request)
}

// RequestTypeName 返回请求类型的名字（去掉指针）。
func RequestTypeName(request any) string {
	return reflect.TypeOf(request).Elem().Name()
}
