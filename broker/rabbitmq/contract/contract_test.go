// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package contract

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestProviderMissingSection 覆盖 rabbitmq 段缺失分支。
func TestProviderMissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Broker{})
	if err == nil || !strings.Contains(err.Error(), "rabbitmq section is missing") {
		t.Fatalf("期望段缺失错误，实际 %v", err)
	}
}

// TestProviderEmptyURL 覆盖 url 必填校验分支。
func TestProviderEmptyURL(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Broker{
		Rabbitmq: &bootstrapv1.Broker_RabbitMQ{},
	})
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("期望 url required 错误，实际 %v", err)
	}
}

// TestProviderInvalidURL 覆盖 injectCredentials 报错经 Provider 透传的分支：
// "%" 触发 url.Parse 失败。
func TestProviderInvalidURL(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Broker{
		Rabbitmq: &bootstrapv1.Broker_RabbitMQ{Url: "%"},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid url") {
		t.Fatalf("期望 invalid url 错误，实际 %v", err)
	}
}

// TestProviderConnectFails 覆盖 Init 成功、exchange/queue 追加、Connect 拨号失败
// 的分支：指向本机已关闭端口（1），TCP 立即拒绝 → Provider 返回 connect 错误。
func TestProviderConnectFails(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Broker{
		Rabbitmq: &bootstrapv1.Broker_RabbitMQ{
			Url:      "amqp://127.0.0.1:1",
			Exchange: "ex",
			Queue:    "q",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "connect") {
		t.Fatalf("期望 connect 失败错误，实际 %v", err)
	}
}

// TestInjectCredentials 覆盖注入凭证纯函数的全部可达分支。
func TestInjectCredentials(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if _, err := injectCredentials("", "", ""); err == nil {
			t.Fatal("空 URL 应报错")
		}
	})
	t.Run("parse_error", func(t *testing.T) {
		if _, err := injectCredentials("%", "", ""); err == nil {
			t.Fatal("非法转义应触发 url.Parse 错误")
		}
	})
	t.Run("keeps_existing_user", func(t *testing.T) {
		got, err := injectCredentials("amqp://u:p@host", "x", "y")
		if err != nil || got != "amqp://u:p@host" {
			t.Fatalf("已有 userinfo 应原样返回，got=%q err=%v", got, err)
		}
	})
	t.Run("no_credentials", func(t *testing.T) {
		got, err := injectCredentials("amqp://host", "", "")
		if err != nil || got != "amqp://host" {
			t.Fatalf("无凭证应原样返回，got=%q err=%v", got, err)
		}
	})
	t.Run("inject", func(t *testing.T) {
		got, err := injectCredentials("amqp://host", "u", "p")
		if err != nil {
			t.Fatalf("注入出错: %v", err)
		}
		if !strings.Contains(got, "u:p@host") {
			t.Fatalf("期望注入 userinfo，got=%q", got)
		}
	})
}
