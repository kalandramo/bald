package berrors

import (
	stderrors "errors"
	"testing"
)

func TestNewAndError(t *testing.T) {
	e := New(CodeNotFound, "ORDER_NOT_FOUND")
	if e.Error() != "code: 5, reason: ORDER_NOT_FOUND" {
		t.Fatalf("unexpected Error(): %q", e.Error())
	}
	// HTTP 状态码映射已外置到 httperr 子包（见 httperr/httperr_test.go）。
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

// 裂缝3 回归：Details 是 Error 上唯一的引用类型字段——clone 必须深拷贝，
// 否则派生实例就地改 Details 会污染源实例（sentinel），击穿「不可变 builder」承诺。
func TestImmutableBuilder_DetailsNotShared(t *testing.T) {
	sentinel := NotFound("ORDER_NOT_FOUND").WithDetails(map[string]string{"id": "1"})

	// 派生实例就地改自己的 Details
	derived := sentinel.WithMessage("订单不存在")
	derived.Details["id"] = "HACKED"

	if sentinel.Details["id"] != "1" {
		t.Errorf("派生实例改 Details 污染了源实例：sentinel.Details[id]=%q, want 1", sentinel.Details["id"])
	}
}

// 裂缝3 回归（另一半）：WithDetails 必须深拷贝入参——调用方之后改自己的 map
// 不应污染已构造的 error。
func TestWithDetails_CopiesInputMap(t *testing.T) {
	input := map[string]string{"id": "1"}
	e := NotFound("ORDER_NOT_FOUND").WithDetails(input)

	input["id"] = "HACKED" // 调用方改自己的 map

	if e.Details["id"] != "1" {
		t.Errorf("WithDetails 未深拷贝入参：e.Details[id]=%q, want 1", e.Details["id"])
	}
}

// nil Details 深拷贝应保持 nil（不引入空 map，保持零分配语义）。
func TestClone_NilDetailsStaysNil(t *testing.T) {
	bare := NotFound("ORDER_NOT_FOUND")
	derived := bare.WithMessage("x")
	if derived.Details != nil {
		t.Errorf("nil Details 派生后应仍为 nil，got %v", derived.Details)
	}
}
