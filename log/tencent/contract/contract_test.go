package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Provider 把契约 tencent 段逐字段映射为构造选项；段缺失 fail-fast。
// 这里的 endpoint 指向本机一个没人听的端口：构造期不建连接、本用例也不写日志，
// 所以不会有任何线上动作——四行真落到线上的判据在 mapping_offline_test.go。
func TestProvider_MapsContractFields(t *testing.T) {
	cfg := &bootstrapv1.Logger_Backend{
		Type: "tencent",
		Tencent: &bootstrapv1.Logger_Tencent{
			Endpoint:     "http://127.0.0.1:1",
			TopicId:      "topic-1234",
			AccessKey:    "ak",
			AccessSecret: "sk",
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
	cfg := &bootstrapv1.Logger_Backend{Type: "tencent"}
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on nil tencent segment")
	}
}
