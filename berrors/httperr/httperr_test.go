package httperr

import (
	"testing"

	"github.com/kalandramo/bald/berrors"
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

// 裂缝4 回归：正向表里每个 HTTP 码都应在反向表中有对应（对称性）。
// 曾漏 499（Canceled→499 有正向，HTTPToCode(499) 却兜底 Unknown）。
func TestHTTPMappingSymmetry(t *testing.T) {
	codes := []uint32{
		berrors.CodeOK, berrors.CodeCanceled, berrors.CodeUnknown,
		berrors.CodeInvalidArgument, berrors.CodeDeadlineExceeded, berrors.CodeNotFound,
		berrors.CodeAlreadyExists, berrors.CodePermissionDenied, berrors.CodeResourceExhausted,
		berrors.CodeFailedPrecondition, berrors.CodeAborted, berrors.CodeOutOfRange,
		berrors.CodeUnimplemented, berrors.CodeInternal, berrors.CodeUnavailable,
		berrors.CodeDataLoss, berrors.CodeUnauthenticated,
	}
	for _, code := range codes {
		h := CodeToHTTP(code)
		if back := HTTPToCode(h); back != code {
			// 多对一（多个 Code → 同一 HTTP 码）时反向只能取其一，属预期。
			// 但 499/200 等一对一码必须精确往返。
			if h == 499 || h == 200 || h == 401 || h == 403 || h == 404 || h == 429 || h == 501 || h == 503 || h == 504 {
				t.Errorf("对称性破坏：CodeToHTTP(%d)=%d 但 HTTPToCode(%d)=%d", code, h, h, back)
			}
		}
	}
}

// 499 专项：Canceled 必须能经 HTTP 往返。
func TestCanceledRoundTrip(t *testing.T) {
	if got := HTTPToCode(499); got != berrors.CodeCanceled {
		t.Errorf("HTTPToCode(499)=%d, want CodeCanceled(%d)", got, berrors.CodeCanceled)
	}
}
