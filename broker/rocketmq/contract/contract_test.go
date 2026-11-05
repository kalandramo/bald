package contract

// contract_test.go —— rocketmq 映射层的形状与 fail-fast（不触线）。
//
// 八行映射的**取值**判据在 mapping_offline_test.go；这里只判 Provider 的入口条件：
// 段缺失、两个地址字段都缺、只给 name_server_url 时守卫要放行。这几条不需要假线，
// 因为它们在任何一个请求发出之前就返回了。

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker"
)

func TestType(t *testing.T) {
	if Type != "rocketmq" {
		t.Errorf("Type = %q, want %q", Type, "rocketmq")
	}
}

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Broker{})
	if err == nil {
		t.Fatal("缺 rocketmq 段时 Provider 应该失败")
	}
	want := `broker: type="rocketmq" but rocketmq section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestProvider_MissingBothAddressFields 判「两个地址字段都得有」这一半守卫：
// name_servers 为空**且** name_server_url 为空时必须当场拒绝，而不是让 SDK 拿
// `jmenv.tbsite.net:8080` 这个内置域名去撞外网——那会把一个配置错误变成一条
// 需要联网才能复现的超时。
func TestProvider_MissingBothAddressFields(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Broker{
		Rocketmq: &bootstrapv1.Broker_Rocketmq{GroupName: "GID-guard"},
	})
	if err == nil {
		t.Fatal("两个地址字段都为空时 Provider 应该失败")
	}
	want := "broker.rocketmq: name_servers or name_server_url is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// TestProvider_NameServerUrlAlonePassesGuard 判守卫的另一半（或的关系）：只给
// name_server_url 也要放行。放行不等于连接——v2 的 Connect 只翻标志位，生产者是
// 首次 Publish 时才建的，所以这里给一个不存在的域名也不会失败。
// 「构造成功但一个字节都没发」这一条由 mapping_offline_test.go 的
// TestMappingOffline_ProviderAloneEmitsNoTraffic 判，本条只判守卫。
func TestProvider_NameServerUrlAlonePassesGuard(t *testing.T) {
	out, cleanup, err := Provider(context.Background(), &bootstrapv1.Broker{
		Rocketmq: &bootstrapv1.Broker_Rocketmq{
			NameServerUrl: "http://127.0.0.1:1/nsaddr",
			InstanceName:  uniqInstance("guard-url"),
		},
	})
	if err != nil {
		t.Fatalf("只给 name_server_url 时 Provider 被守卫挡下了: %v", err)
	}
	if _, ok := out.(broker.Broker); !ok {
		t.Fatalf("Provider 交出 %T，不是 broker.Broker", out)
	}
	cleanup()
	// cleanup 走 Disconnect；再清一次必须无害（Disconnect 对未连接直接返回 nil）。
	cleanup()
}

// 类型守卫：Provider 保持构造 func 形状（供 Provider.Build 复用）。
var _ func(context.Context, *bootstrapv1.Broker) (any, func(), error) = Provider
