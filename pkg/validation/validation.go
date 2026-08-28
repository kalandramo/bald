// Package validation 提供基于反射的「按请求类型名」分发校验能力（移植自 onexstack）。
//
// 约定：业务为每个请求类型 XxxRequest 实现一个方法 ValidateXxxRequest(ctx, *XxxRequest) error，
// 并把它注册到 Validator。每次请求进入时调用 Validator.Validate(ctx, req)，自动按 req 的类型名
// 找到对应校验方法进行校验。这一约定避免了一层抽象（不用把校验注册成 map），且天然支持
// 「先 Bind 后一次性 Validate」的流水线（见 pkg/web 的多源绑定）。
package validation

import (
	"context"
	"reflect"

	"k8s.io/klog/v2"
)

// Validator 是注册表 + 分发器：把「请求类型名」映射到「校验函数」。
type Validator struct {
	validators map[string]func(ctx context.Context, req any) error
}

// NewValidator 创建一个校验器；validators 是实现了 ValidateXxx 方法的任意对象（struct 或指针）。
func NewValidator(validators any) *Validator {
	v := &Validator{validators: make(map[string]func(ctx context.Context, req any) error)}
	v.Register(validators)
	return v
}

// Register 把 obj 上所有形如 Validate<RequestTypeName>(ctx, *Request) error 的方法注册进来。
// 方法名必须是 `Validate` + 请求类型名；第一个参数为 context.Context，第二个参数为请求指针。
func (v *Validator) Register(validators any) {
	if validators == nil {
		return
	}
	val := reflect.ValueOf(validators)
	typ := val.Type()
	for i := 0; i < typ.NumMethod(); i++ {
		method := typ.Method(i)
		name := method.Name
		const prefix = "Validate"
		if len(name) < len(prefix) || name[:len(prefix)] != prefix {
			continue
		}
		mt := method.Type // 类型：(recv, ctx, *Req) error
		if mt.NumIn() != 3 || mt.NumOut() != 1 {
			continue
		}
		reqIn := mt.In(2)
		if reqIn.Kind() != reflect.Ptr {
			continue
		}
		reqName := reqIn.Elem().Name()
		ruleName := name[len(prefix):]
		if ruleName != reqName {
			klog.V(4).InfoS("Skipping validator", "method", name, "expected", "Validate"+reqName)
			continue
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
