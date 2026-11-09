package grpcerr

import (
	"testing"

	"google.golang.org/grpc/codes"
)

// ToStatus(nil) → OK 半边。
func TestToStatus_Nil(t *testing.T) {
	if st := ToStatus(nil); st.Code() != codes.OK {
		t.Fatalf("nil 错误应映射 OK，got %v", st.Code())
	}
}

// FromStatus(nil) → nil 半边。
func TestFromStatus_Nil(t *testing.T) {
	if err := FromStatus(nil); err != nil {
		t.Fatalf("nil status 应返回 nil，got %v", err)
	}
}
