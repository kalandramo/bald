// Package errors 提供 bald 的传输中立错误模型。
//
// 设计要点（详见 docs/devel/zh-CN/错误模型设计.md）：
//   - 核心包零依赖：不 import gRPC、net/http、kratos，任意场景（纯 HTTP、
//     gRPC、CLI、测试）都可 import 而不拖进一个传输框架。
//   - Code 是 uint32 而非 gRPC codes.Code，HTTP 映射由 CodeToHTTP 提供，
//     gRPC 转换由子包 pkg/errors/grpcerr 在边界提供。
//   - 所有 With* 构建器不可变：返回新实例，接收者绝不被修改，sentinel 安全。
//   - Is 按 Reason 匹配（忽略易变的 Code/Message/Details/cause）。
//
// 标准库桥接：本包名为 errors，遮蔽标准库 errors，故重导出最常用的
// Is/As/Unwrap/Join。注意 New 不重导出（项目 New(code, reason) 与标准库
// New(string) 签名冲突）；需要标准库 New 时别名 import。
package errors

import (
	"bytes"
	stderrors "errors"
	"fmt"
	"runtime"
	"strconv"
)

// Error 描述一个业务失败。所有字段均可为空。
type Error struct {
	Code    uint32            `json:"-"`       // 传输类别，与 gRPC codes.Code 1:1，见 Code* 常量
	Reason  string            `json:"reason"`  // 领域稳定标识，如 "ORDER_NOT_FOUND"，Is 按它匹配
	Message string            `json:"message"` // 可展示给最终用户的简短文案
	Details map[string]string `json:"details"` // i18n 动态变量，如 {"id": "42"}
	cause   error             `json:"-"`       // 包装的底层错误（error 链）
	stack   []uintptr         `json:"-"`       // 失败点调用栈，日志收集用
}

// New 用给定的传输类别与 Reason 构造一个 *Error。
// 需要用户文案时链上 WithMessage；需要动态变量时链上 WithDetails。
func New(code uint32, reason string) *Error {
	return &Error{
		Code:   code,
		Reason: reason,
	}
}

// Error 实现原生 error 接口；为日志可读性调成 code/reason/cause 形态，
// 不混入 Message（避免日志被多语言文案污染，且无法结构化消费）。
func (e *Error) Error() string {
	var buf bytes.Buffer
	buf.WriteString("code: ")
	buf.WriteString(strconv.FormatUint(uint64(e.Code), 10))
	buf.WriteString(", reason: ")
	buf.WriteString(e.Reason)
	if e.cause != nil {
		buf.WriteString("; cause: ")
		buf.WriteString(e.cause.Error())
	}
	return buf.String()
}

// Is 支持标准库 errors.Is：两个 Error 按 Reason 相等匹配；
// Code/Message/Details/cause 故意忽略，业务代码只在稳定业务标识上匹配。
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	return e.Reason == t.Reason
}

// Unwrap 支持标准库 errors.Unwrap。
func (e *Error) Unwrap() error {
	return e.cause
}

// StatusCode 返回该错误对应的 HTTP 状态码（CodeToHTTP(Code)），
// 满足 web 层 StatusCoder 接口：实现了 StatusCode() 的错误自己决定状态码。
func (e *Error) StatusCode() int {
	return CodeToHTTP(e.Code)
}

// WithMessage 返回带用户文案的新实例。文案用 fmt.Sprintf 格式化。
func (e *Error) WithMessage(format string, args ...any) *Error {
	err := e.clone()
	err.Message = fmt.Sprintf(format, args...)
	err.captureStack()
	return err
}

// WithDetails 返回注入 i18n 变量的新实例。
func (e *Error) WithDetails(details map[string]string) *Error {
	err := e.clone()
	err.Details = details
	err.captureStack()
	return err
}

// WithCause 返回包装底层原生错误的新实例。
func (e *Error) WithCause(cause error) *Error {
	err := e.clone()
	err.cause = cause
	err.captureStack()
	return err
}

// WithCode 是逃生舱：允许在特殊业务场景覆盖传输类别。
func (e *Error) WithCode(code uint32) *Error {
	err := e.clone()
	err.Code = code
	return err
}

func (e *Error) clone() *Error {
	// Details 浅拷贝（共享底层 map）：WithDetails 整体替换 map 而非就地改键，
	// 因此原实例不会被 clone 污染，浅拷贝安全且零分配。若未来新增就地修改
	// Details 的方法，请改为深拷贝以避免共享 map 的数据竞争。
	return &Error{
		Code:    e.Code,
		Reason:  e.Reason,
		Message: e.Message,
		Details: e.Details,
		cause:   e.cause,
		stack:   e.stack,
	}
}

func (e *Error) captureStack() {
	if e.stack != nil {
		return
	}
	var pcs [32]uintptr
	// 跳过 3 帧：runtime.Callers → captureStack → With*（业务失败现场）。
	n := runtime.Callers(3, pcs[:])
	e.stack = pcs[:n]
}

// StackTrace 把捕获的调用栈格式化为字符串，供日志收集器消费。
func (e *Error) StackTrace() string {
	if e.stack == nil {
		return ""
	}
	var buf bytes.Buffer
	frames := runtime.CallersFrames(e.stack)
	for {
		frame, more := frames.Next()
		buf.WriteString(fmt.Sprintf("\n\t%s:%d %s", frame.File, frame.Line, frame.Function))
		if !more {
			break
		}
	}
	return buf.String()
}

// 标准库桥接：重导出 Is/As/Unwrap/Join（New 因签名冲突不重导出）。
// 包级函数与 Error 的方法同处不同命名空间，无歧义；标准库 errors.Is/As
// 内部回调 Error 的方法，链式匹配与解包均正确。

// Is 桥接标准库 errors.Is：沿 error 链（Unwrap）报告 err 是否匹配 target。
func Is(err, target error) bool { return stderrors.Is(err, target) }

// As 桥接标准库 errors.As：在 err 链中找到第一个可赋给 *target 的错误。
func As(err error, target any) bool { return stderrors.As(err, target) }

// Unwrap 桥接标准库 errors.Unwrap：返回 err 的直接 cause。
func Unwrap(err error) error { return stderrors.Unwrap(err) }

// FromError 从 err 链中提取 *Error，找不到返回 (nil, false)。
// 它是 As 的类型化便捷版本，调用方省去 "var e *Error; errors.As(...)" 样板。
func FromError(err error) (*Error, bool) {
	if err == nil {
		return nil, false
	}
	var e *Error
	if stderrors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Join 桥接标准库 errors.Join：合并多个错误为一个。
func Join(errs ...error) error { return stderrors.Join(errs...) }
