package contract

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// Provider 把契约 tencent 段逐字段映射为构造选项；段缺失 fail-fast。
// 构造本地 producer 不联网（连接在写入时才发生）；cleanup 冲刷空队列即返回。
func TestProvider_MapsContractFields(t *testing.T) {
	cfg := &bootstrapv1.Logger{
		Type: "tencent",
		Tencent: &bootstrapv1.Logger_Tencent{
			Endpoint:     "ap-guangzhou.cls.tencentcs.com",
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
	cfg := &bootstrapv1.Logger{Type: "tencent"}
	if _, _, err := Provider(context.Background(), cfg); err == nil {
		t.Fatal("expected fail-fast on nil tencent segment")
	}
}
