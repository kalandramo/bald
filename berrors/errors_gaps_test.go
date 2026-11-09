package berrors

import (
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
)

// Error() 携带 cause 时追加 "; cause: ..." 半边。
func TestError_WithCauseText(t *testing.T) {
	e := New(CodeNotFound, "R").WithCause(stderrors.New("boom"))
	if !strings.Contains(e.Error(), "; cause: boom") {
		t.Fatalf("Error() 应含 cause 文案：%q", e.Error())
	}
}

// WithCode 逃生舱覆盖传输类别。
func TestWithCode(t *testing.T) {
	e := New(CodeNotFound, "R").WithCode(CodeInternal)
	if e.Code != CodeInternal {
		t.Fatalf("WithCode 后 Code = %d, want %d", e.Code, CodeInternal)
	}
}

// 零值 Error（未捕获栈）StackTrace 返回空串。
func TestStackTrace_NilStack(t *testing.T) {
	if got := (&Error{}).StackTrace(); got != "" {
		t.Fatalf("nil stack 应返回空串，got %q", got)
	}
}

// 便捷工厂各自构造对应传输类别 + Reason。
func TestConvenienceFactories(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string) *Error
		code uint32
	}{
		{"BadRequest", BadRequest, CodeInvalidArgument},
		{"NotFound", NotFound, CodeNotFound},
		{"AlreadyExists", AlreadyExists, CodeAlreadyExists},
		{"PermissionDenied", PermissionDenied, CodePermissionDenied},
		{"ResourceExhausted", ResourceExhausted, CodeResourceExhausted},
		{"FailedPrecondition", FailedPrecondition, CodeFailedPrecondition},
		{"Unauthenticated", Unauthenticated, CodeUnauthenticated},
		{"Internal", Internal, CodeInternal},
		{"Unimplemented", Unimplemented, CodeUnimplemented},
		{"Unavailable", Unavailable, CodeUnavailable},
		{"DeadlineExceeded", DeadlineExceeded, CodeDeadlineExceeded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.fn("DOMAIN_REASON")
			if e.Code != tc.code || e.Reason != "DOMAIN_REASON" {
				t.Fatalf("%s = code %d reason %q, want code %d reason DOMAIN_REASON", tc.name, e.Code, e.Reason, tc.code)
			}
		})
	}
}

// 标准库桥接 Is/As/Unwrap/Join 四个重导出函数。
func TestStdBridges(t *testing.T) {
	base := New(CodeNotFound, "R")
	if !Is(base, New(CodeInternal, "R")) {
		t.Fatal("Is 应按 Reason 匹配（忽略 Code）")
	}
	if Is(base, stderrors.New("x")) {
		t.Fatal("Is 非 *Error target 应返回 false")
	}

	wrapped := fmt.Errorf("wrap: %w", base)
	var e *Error
	if !As(wrapped, &e) || e != base {
		t.Fatal("As 应解出链中的 *Error")
	}

	cause := stderrors.New("c")
	if Unwrap(base.WithCause(cause)) != cause {
		t.Fatal("Unwrap 应返回直接 cause")
	}

	j := Join(base, stderrors.New("other"))
	if j == nil || !Is(j, base) {
		t.Fatal("Join 应合并错误并保留 Is 匹配")
	}
}
