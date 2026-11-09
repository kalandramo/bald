// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package option_test

import (
	"context"
	"testing"
	"time"

	"github.com/kalandramo/bald/broker"
	"github.com/kalandramo/bald/broker/rocketmq/option"
	"github.com/kalandramo/bald/log"
)

// TestOptionConstructors 逐一调用全部 broker.Option 构造器——每个体内仅一条
// 「委托 OptionContextWithValue」语句，调用即覆盖；再统一 Apply 到 Options 验证
// 闭包可正常写入 context。
func TestOptionConstructors(t *testing.T) {
	opts := []broker.Option{
		option.WithLoggerLevel(log.LevelInfo),
		option.WithEnableTrace(),
		option.WithNameServer([]string{"127.0.0.1:9876"}),
		option.WithNameServerDomain("http://onsaddr"),
		option.WithAccessKey("ak"),
		option.WithSecretKey("sk"),
		option.WithSecurityToken("st"),
		option.WithCredentials("ak", "sk", "st"),
		option.WithRetryCount(3),
		option.WithNamespace("ns"),
		option.WithInstanceName("inst"),
		option.WithGroupName("grp"),
		option.WithDefaultTopic("topic"),
	}
	o := &broker.Options{}
	o.Apply(opts...)
	if o.Context == nil {
		t.Fatal("Apply 后 Context 应已建立")
	}
}

// TestPublishOptionConstructors 覆盖全部 PublishOption 构造器并应用到 PublishOptions。
func TestPublishOptionConstructors(t *testing.T) {
	po := &broker.PublishOptions{Context: context.Background()}
	po.Apply(
		option.WithCompress(true),
		option.WithBatch(false),
		option.WithProperties(map[string]string{"k": "v"}),
		option.WithDelayTimeLevel(2),
		option.WithTag("tag"),
		option.WithKeys([]string{"k1"}),
		option.WithShardingKey("sk"),
		option.WithDeliveryTimestamp(time.Now()),
		option.WithMessageGroup("mg"),
		option.WithSendAsync(true),
		option.WithSendWithTransaction(false),
	)
	if po.Context == nil {
		t.Fatal("PublishOptions.Context 应已建立")
	}
}

// TestSubscribeOptionConstructors 覆盖 SubscribeOption 构造器并应用到 SubscribeOptions。
func TestSubscribeOptionConstructors(t *testing.T) {
	so := &broker.SubscribeOptions{Context: context.Background()}
	so.Apply(option.WithConsumerModel(option.MessageModelClustering))
	if got, _ := so.Context.Value(option.ConsumerModelKey{}).(option.MessageModel); got != option.MessageModelClustering {
		t.Fatalf("ConsumerModel 读回 %q", got)
	}
}
