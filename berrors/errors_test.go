package errors

import (
	stderrors "errors"
	"testing"
)

func TestNewAndError(t *testing.T) {
	e := New(CodeNotFound, "ORDER_NOT_FOUND")
	if e.Error() != "code: 5, reason: ORDER_NOT_FOUND" {
		t.Fatalf("unexpected Error(): %q", e.Error())
	}
	// HTTP 状态码映射已外置到 httperr 子包（见 pkg/berrors/httperr/httperr_test.go）。
}

func TestImmutableBuilder(t *testing.T) {
	sentinel := NotFound("ORDER_NOT_FOUND")
	derived := sentinel.WithMessage("订单不存在").WithDetails(map[string]string{"id": "42"})

	if sentinel.Message != "" || sentinel.Details != nil {
		t.Fatal("sentinel must not be mutated by With*")
	}
	if derived.Message != "订单不存在" || derived.Details["id"] != "42" {
		t.Fatalf("derived not set: %+v", derived)
	}
	// sentinel 派生后的 Reason 不变，Is 仍命中。
	if !stderrors.Is(derived, sentinel) {
		t.Fatal("errors.Is should match on Reason")
	}
}

func TestIsMatchesByReasonOnly(t *testing.T) {
	a := NotFound("ORDER_NOT_FOUND")
	b := New(CodeInternal, "ORDER_NOT_FOUND") // 不同 Code，相同 Reason
	if !stderrors.Is(a, b) {
		t.Fatal("Is should match regardless of Code")
	}

	c := NotFound("OTHER")
	if stderrors.Is(a, c) {
		t.Fatal("Is should not match different Reason")
	}
}

func TestWithCauseUnwrap(t *testing.T) {
	root := stderrors.New("db down")
	e := Internal("QUERY_FAILED").WithCause(root)
	if !stderrors.Is(e, root) {
		t.Fatal("cause should be unwrappable")
	}
}

func TestFromError(t *testing.T) {
	e := NotFound("MISSING")
	got, ok := FromError(e)
	if !ok || got != e {
		t.Fatalf("FromError failed: %v %v", got, ok)
	}
	if _, ok := FromError(nil); ok {
		t.Fatal("nil should return false")
	}
	if _, ok := FromError(stderrors.New("plain")); ok {
		t.Fatal("plain error should not match *Error")
	}
}

// P3：构造即捕获调用栈，StackTrace() 返回非空字符串且含调用方帧。
func TestStackTraceCaptured(t *testing.T) {
	e := NotFound("WITH_STACK")
	stack := e.StackTrace()
	if stack == "" {
		t.Fatal("expected non-empty stack trace string")
	}
	// 栈应包含本测试函数名，证明从调用点捕获。
	if !containsString(stack, "TestStackTraceCaptured") {
		t.Fatalf("stack trace should include caller frame:\n%s", stack)
	}
}

func containsString(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
