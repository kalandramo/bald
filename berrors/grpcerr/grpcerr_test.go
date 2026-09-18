package grpcerr

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
)

func TestToStatusAndFromStatusRoundTrip(t *testing.T) {
	wErr := berrors.NotFound("ORDER_NOT_FOUND").
		WithMessage("订单不存在").
		WithDetails(map[string]string{"id": "42"})

	st := ToStatus(wErr)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", st.Code())
	}

	back := FromStatus(st)
	got, ok := berrors.FromError(back)
	if !ok {
		t.Fatalf("FromStatus result should be a *berrors.Error, got %T", back)
	}
	if got.Reason != "ORDER_NOT_FOUND" || got.Details["id"] != "42" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if !berrors.Is(back, wErr) {
		t.Fatal("Is should match across gRPC boundary by Reason")
	}
}

func TestToStatusPlainError(t *testing.T) {
	st := ToStatus(status.Error(codes.Internal, "boom"))
	if st.Code() != codes.Internal {
		t.Fatalf("expected Internal, got %v", st.Code())
	}
}

func TestFromStatusWithKratosLikeErrorInfo(t *testing.T) {
	st, _ := status.New(codes.PermissionDenied, "no").
		WithDetails(&errdetails.ErrorInfo{Reason: "FORBIDDEN", Metadata: map[string]string{"k": "v"}})
	back := FromStatus(st)
	got, ok := berrors.FromError(back)
	if !ok || got.Reason != "FORBIDDEN" || got.Details["k"] != "v" {
		t.Fatalf("ErrorInfo not parsed: %+v", got)
	}
}

// 裂缝1 回归：roundtrip 必须保留用户 Message（决策⑧要求前端展示读 message）。
func TestRoundTripPreservesMessage(t *testing.T) {
	orig := berrors.NotFound("ORDER_NOT_FOUND").
		WithMessage("订单 %s 不存在", "42").
		WithDetails(map[string]string{"id": "42"})

	back, ok := berrors.FromError(FromStatus(ToStatus(orig)))
	if !ok {
		t.Fatal("roundtrip 应还原为 *berrors.Error")
	}
	if back.Message != "订单 42 不存在" {
		t.Errorf("roundtrip 丢失 Message：got=%q, want=%q", back.Message, "订单 42 不存在")
	}
	if back.Reason != "ORDER_NOT_FOUND" || back.Details["id"] != "42" {
		t.Errorf("roundtrip 丢失 Reason/Details：%+v", back)
	}
}

// 裂缝2 回归：无 ErrorInfo 的普通 status，其可变文本不应污染 Is 的匹配键 Reason。
// 两个同类 NotFound（文本不同）必须能互相 Is 匹配。
func TestPlainStatusKeepsReasonClean(t *testing.T) {
	e1, _ := berrors.FromError(FromStatus(status.New(codes.NotFound, "order 42 not found")))
	e2, _ := berrors.FromError(FromStatus(status.New(codes.NotFound, "order 99 not found")))

	if e1.Reason == "order 42 not found" {
		t.Errorf("普通 status 的文本不应落在 Reason：Reason=%q", e1.Reason)
	}
	if !berrors.Is(e1, e2) {
		t.Errorf("两个同类 NotFound 应能 Is 匹配：e1.Reason=%q e2.Reason=%q", e1.Reason, e2.Reason)
	}
	// 文本不应丢失，应落在 Message
	if e1.Message != "order 42 not found" {
		t.Errorf("普通 status 的文本应落在 Message：Message=%q", e1.Message)
	}
}

// 空 Reason 的 Error 经 roundtrip 后语义稳定（不因缺 ErrorInfo 而错位）。
func TestRoundTripEmptyReason(t *testing.T) {
	bare := berrors.New(berrors.CodeInternal, "")
	back, _ := berrors.FromError(FromStatus(ToStatus(bare)))
	if back.Code != berrors.CodeInternal {
		t.Errorf("Code 应保留：got=%d want=%d", back.Code, berrors.CodeInternal)
	}
}
