package contract

// contract_byte_order_test.go —— byte_order 枚举分支与非空 cleanup 适配器。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

func TestProvider_ByteOrderMapping(t *testing.T) {
	cases := []struct {
		name    string
		order   bootstrapv1.Server_Tcp_ByteOrder
		wantErr bool
	}{
		{"unspecified", bootstrapv1.Server_Tcp_BYTE_ORDER_UNSPECIFIED, false},
		{"little", bootstrapv1.Server_Tcp_LITTLE, false},
		{"big", bootstrapv1.Server_Tcp_BIG, false},
		{"unknown", bootstrapv1.Server_Tcp_ByteOrder(99), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := &bootstrapv1.Server{Tcp: &bootstrapv1.Server_Tcp{
				Addr:      "127.0.0.1:0",
				ByteOrder: c.order,
			}}
			srv, cleanup, err := Provider()(context.Background(), cfg)
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), "unknown") {
					t.Fatalf("err = %v, want unknown byte_order error", err)
				}
				if srv != nil || cleanup != nil {
					t.Fatalf("(srv, cleanup) = (%v, isNil=%v), want nil on error", srv, cleanup == nil)
				}
				return
			}
			if err != nil {
				t.Fatalf("Provider() error = %v", err)
			}
			if srv == nil {
				t.Fatal("server should be constructed")
			}
		})
	}
}

func TestAdaptCleanup_NonNil(t *testing.T) {
	called := false
	c := adaptCleanup(func() { called = true })
	if c == nil {
		t.Fatal("non-nil cleanup must be adapted")
	}
	if err := c(context.Background()); err != nil {
		t.Fatalf("cleanup error = %v", err)
	}
	if !called {
		t.Fatal("adapted cleanup did not invoke the original")
	}
}
