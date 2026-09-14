package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Provider 把契约 loki 段逐字段映射为构造选项；段缺失/必填字段缺失 fail-fast。
func TestProvider_MapsContractFields(t *testing.T) {
	cfg := &bootstrapv1.Logger_Backend{
		Type: "loki",
		Loki: &bootstrapv1.Logger_Loki{
			Endpoint:      "http://127.0.0.1:3100/loki/api/v1/push",
			Labels:        map[string]string{"app": "demo", "env": "test"},
			BatchSize:     10,
			FlushInterval: 100,
		},
	}
	l, cleanup, err := Provider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if l == nil {
		t.Fatal("nil logger")
	}
	// NewLogger 只校验 endpoint 不发请求；cleanup 停后台推送 goroutine。
	cleanup()
}

func TestProvider_NilSegment(t *testing.T) {
	cfg := &bootstrapv1.Logger_Backend{Type: "loki"} // loki 段缺失
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on nil loki segment")
	}
}

func TestProvider_MissingEndpoint(t *testing.T) {
	cfg := &bootstrapv1.Logger_Backend{Type: "loki", Loki: &bootstrapv1.Logger_Loki{}}
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on empty endpoint")
	}
}
