package httperr

import (
	"testing"

	berrors "github.com/kalandramo/bald/pkg/berrors"
)

func TestCodeToHTTP(t *testing.T) {
	cases := map[uint32]int{
		berrors.CodeNotFound:          404,
		berrors.CodeInvalidArgument:   400,
		berrors.CodeUnauthenticated:   401,
		berrors.CodeResourceExhausted: 429,
		berrors.CodeUnavailable:       503,
		999:                           500, // 未知兜底
	}
	for code, want := range cases {
		if got := CodeToHTTP(code); got != want {
			t.Fatalf("CodeToHTTP(%d) = %d, want %d", code, got, want)
		}
	}
}

func TestHTTPToCode(t *testing.T) {
	if HTTPToCode(418) != berrors.CodeUnknown {
		t.Fatal("unmapped HTTP status should fall back to Unknown")
	}
	if HTTPToCode(400) != berrors.CodeInvalidArgument {
		t.Fatal("400 should map to InvalidArgument")
	}
}

func TestStatusCode(t *testing.T) {
	if got := StatusCode(berrors.NotFound("ORDER_NOT_FOUND")); got != 404 {
		t.Fatalf("StatusCode = %d, want 404", got)
	}
}
