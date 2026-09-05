package grpcerr

import (
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/kalandramo/bald/berrors"
)

func TestToStatusAndFromStatusRoundTrip(t *testing.T) {
	wErr := errors.NotFound("ORDER_NOT_FOUND").
		WithMessage("订单不存在").
		WithDetails(map[string]string{"id": "42"})

	st := ToStatus(wErr)
	if st.Code() != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", st.Code())
	}

	back := FromStatus(st)
	got, ok := errors.FromError(back)
	if !ok {
		t.Fatalf("FromStatus result should be a *errors.Error, got %T", back)
	}
	if got.Reason != "ORDER_NOT_FOUND" || got.Details["id"] != "42" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if !errors.Is(back, wErr) {
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
	got, ok := errors.FromError(back)
	if !ok || got.Reason != "FORBIDDEN" || got.Details["k"] != "v" {
		t.Fatalf("ErrorInfo not parsed: %+v", got)
	}
}
