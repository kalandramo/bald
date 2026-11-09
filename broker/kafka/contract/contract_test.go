// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package contract_test

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/broker/kafka/contract"
)

// TestProviderMissingSection 覆盖 kafka 段缺失分支：返回错误。
func TestProviderMissingSection(t *testing.T) {
	_, cleanup, err := contract.Provider(context.Background(), &bootstrapv1.Broker{})
	if err == nil || !strings.Contains(err.Error(), "kafka section is missing") {
		t.Fatalf("期望段缺失错误，实际 err=%v", err)
	}
	if cleanup != nil {
		t.Fatal("错误路径 cleanup 应为 nil")
	}
}

// TestProviderAuthTypes 覆盖 auth_type 的五个分支（none/空/plain/scram256/scram512）
// 与 brokers 有/无两个分支。kafka 的 Init/Connect 是惰性的（建 writer、置 connected，
// 不真正拨号），故这些合法配置都返回成功产物 + 可调用 cleanup。
func TestProviderAuthTypes(t *testing.T) {
	rows := []struct {
		name string
		sec  *bootstrapv1.Broker_Kafka
	}{
		{"none", &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}, AuthType: "none"}},
		{"empty", &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}}},
		{"plain", &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}, AuthType: "plain", Username: "u", Password: "p"}},
		{"sha256", &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}, AuthType: "scram-sha256", Username: "u", Password: "p"}},
		{"sha512", &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}, AuthType: "scram-sha512", Username: "u", Password: "p"}},
		// brokers 为空 → 跳过 WithAddress 分支；Init 会补默认地址使惰性 Connect 仍成功。
		{"no_brokers", &bootstrapv1.Broker_Kafka{AuthType: "none"}},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			b, cleanup, err := contract.Provider(context.Background(), &bootstrapv1.Broker{Kafka: r.sec})
			if err != nil {
				t.Fatalf("Provider 期望成功，实际错误: %v", err)
			}
			if b == nil {
				t.Fatal("Provider 返回 nil broker")
			}
			if cleanup == nil {
				t.Fatal("成功路径 cleanup 应非 nil")
			}
			cleanup() // 执行 Disconnect 闭包
		})
	}
}

// TestProviderUnsupportedAuth 覆盖 switch default 分支：不支持的 auth_type 报错。
func TestProviderUnsupportedAuth(t *testing.T) {
	_, _, err := contract.Provider(context.Background(), &bootstrapv1.Broker{
		Kafka: &bootstrapv1.Broker_Kafka{Brokers: []string{"127.0.0.1:9092"}, AuthType: "bogus"},
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported auth_type") {
		t.Fatalf("期望 unsupported auth_type 错误，实际 %v", err)
	}
}
