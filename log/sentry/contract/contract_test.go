package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

const testDSN = "https://0123456789abcdef0123456789abcdef@o0.ingest.sentry.io/1234567"

// Provider 把契约 sentry 段逐字段映射为构造选项；段缺失/dsn 缺失 fail-fast。
// 构造（sentry.Init 解析 DSN）不联网；Flush 空队列立即返回。
func TestProvider_MapsContractFields(t *testing.T) {
	cfg := &bootstrapv1.Logger{
		Type: "sentry",
		Sentry: &bootstrapv1.Logger_Sentry{
			Dsn:         testDSN,
			Environment: "test",
			Release:     "demo@1.0.0",
			ServerName:  "localhost",
		},
	}
	l, cleanup, err := Provider(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if l == nil {
		t.Fatal("nil logger")
	}
	cleanup()
}

func TestProvider_NilSegment(t *testing.T) {
	cfg := &bootstrapv1.Logger{Type: "sentry"}
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on nil sentry segment")
	}
}

func TestProvider_MissingDSN(t *testing.T) {
	cfg := &bootstrapv1.Logger{Type: "sentry", Sentry: &bootstrapv1.Logger_Sentry{}}
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on empty dsn")
	}
}
