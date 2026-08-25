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
	if e.StatusCode() != 404 {
		t.Fatalf("expected 404, got %d", e.StatusCode())
	}
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

func TestCodeToHTTP(t *testing.T) {
	cases := map[uint32]int{
		CodeNotFound:         404,
		CodeInvalidArgument:  400,
		CodeUnauthenticated:  401,
		CodeResourceExhausted: 429,
		CodeUnavailable:      503,
		999:                  500, // 未知兜底
	}
	for code, want := range cases {
		if got := CodeToHTTP(code); got != want {
			t.Fatalf("CodeToHTTP(%d) = %d, want %d", code, got, want)
		}
	}
}

func TestHTTPToCode(t *testing.T) {
	if HTTPToCode(418) != CodeUnknown {
		t.Fatal("unmapped HTTP status should fall back to Unknown")
	}
	if HTTPToCode(400) != CodeInvalidArgument {
		t.Fatal("400 should map to InvalidArgument")
	}
}
