package contract

import (
	"context"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/cache"
)

func TestType(t *testing.T) {
	if Type != "redis" {
		t.Errorf("Type = %q, want %q", Type, "redis")
	}
}

// 段缺失 / 必填字段缺失 fail-fast（不触真连）。

func TestProvider_MissingSection(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{})
	if err == nil {
		t.Fatal("Provider with missing redis section should fail")
	}
	want := `cache: type="redis" but redis section is missing`
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_MissingAddr(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{KeyPrefix: "app:"},
	})
	if err == nil {
		t.Fatal("Provider with empty redis.addr should fail")
	}
	want := "cache: redis.addr is required"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

// miniredis 起真实内存 Redis 验证全流程：Ping→读写→前缀隔离→cleanup 关闭。
// （§0 硬性原则：接真实 Redis，不用 fake client。）
func TestProvider_BuildRoundTripAndCleanup(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	cli, cleanup, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{Addr: mr.Addr(), KeyPrefix: "app:"},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	c, ok := cli.(cache.Cache)
	if !ok {
		t.Fatalf("client type = %T, want cache.Cache", cli)
	}

	ctx := context.Background()
	if err := c.Set(ctx, "k", []byte("v"), 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	// 前缀生效：裸 redis 里键为 app:k
	if !mr.Exists("app:k") {
		t.Fatal("key prefix not applied")
	}
	if v, err := c.Get(ctx, "k"); err != nil || string(v) != "v" {
		t.Fatalf("Get = (%q, %v), want (v, nil)", v, err)
	}

	cleanup()
	if _, err := c.Get(ctx, "k"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("after cleanup Get err = %v, want closed", err)
	}
}

// 连接不可达 fail-fast（Ping 失败即返回错误，不产半成品实例）。
func TestProvider_Unreachable(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{Addr: "127.0.0.1:1"}, // 不可达端口
	})
	if err == nil {
		t.Fatal("Provider with unreachable addr should fail")
	}
}

// --- 集群 / 哨兵模式（契约段选建）---

// 模式互斥与必填校验（纯参数，不触网络）。
func TestProvider_ClusterSentinelMutualExclusion(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{
			Cluster:  &bootstrapv1.Cache_Redis_Cluster{Addrs: []string{"127.0.0.1:6379"}},
			Sentinel: &bootstrapv1.Cache_Redis_Sentinel{MasterName: "m", Addrs: []string{"127.0.0.1:26379"}},
		},
	})
	if err == nil {
		t.Fatal("Provider with both cluster and sentinel sections should fail")
	}
	want := "cache: redis.cluster and redis.sentinel are mutually exclusive"
	if err.Error() != want {
		t.Errorf("err = %q, want %q", err.Error(), want)
	}
}

func TestProvider_ClusterValidation(t *testing.T) {
	cases := []struct {
		name string
		sec  *bootstrapv1.Cache_Redis
		want string
	}{
		{
			name: "addr set alongside cluster",
			sec: &bootstrapv1.Cache_Redis{
				Addr:    "localhost:6379",
				Cluster: &bootstrapv1.Cache_Redis_Cluster{Addrs: []string{"127.0.0.1:7001"}},
			},
			want: "cache: redis.addr is only valid in standalone mode (remove it when cluster section is present)",
		},
		{
			name: "empty cluster addrs",
			sec:  &bootstrapv1.Cache_Redis{Cluster: &bootstrapv1.Cache_Redis_Cluster{}},
			want: "cache: redis.cluster.addrs is required",
		},
		{
			name: "db set in cluster mode",
			sec: &bootstrapv1.Cache_Redis{
				Db:      1,
				Cluster: &bootstrapv1.Cache_Redis_Cluster{Addrs: []string{"127.0.0.1:7001"}},
			},
			want: "cache: redis.db is not supported in cluster mode",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Provider(context.Background(), &bootstrapv1.Cache{Redis: tc.sec})
			if err == nil {
				t.Fatal("Provider should fail")
			}
			if err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestProvider_SentinelValidation(t *testing.T) {
	cases := []struct {
		name string
		sec  *bootstrapv1.Cache_Redis
		want string
	}{
		{
			name: "addr set alongside sentinel",
			sec: &bootstrapv1.Cache_Redis{
				Addr:     "localhost:6379",
				Sentinel: &bootstrapv1.Cache_Redis_Sentinel{MasterName: "m", Addrs: []string{"127.0.0.1:26379"}},
			},
			want: "cache: redis.addr is only valid in standalone mode (remove it when sentinel section is present)",
		},
		{
			name: "empty master name",
			sec: &bootstrapv1.Cache_Redis{
				Sentinel: &bootstrapv1.Cache_Redis_Sentinel{Addrs: []string{"127.0.0.1:26379"}},
			},
			want: "cache: redis.sentinel.master_name is required",
		},
		{
			name: "empty sentinel addrs",
			sec: &bootstrapv1.Cache_Redis{
				Sentinel: &bootstrapv1.Cache_Redis_Sentinel{MasterName: "m"},
			},
			want: "cache: redis.sentinel.addrs is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Provider(context.Background(), &bootstrapv1.Cache{Redis: tc.sec})
			if err == nil {
				t.Fatal("Provider should fail")
			}
			if err.Error() != tc.want {
				t.Errorf("err = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

// 集群/哨兵连接不可达 fail-fast：Ping 失败经 Provider 包 build 前缀返回。
// （miniredis 不支持 cluster/sentinel 协议，真连接往返无本地方案——
// 全流程读写验证仅单节点模式覆盖，集群/哨兵覆盖到「校验 + 连接失败传播」。）
func TestProvider_ClusterUnreachable(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{
			Cluster: &bootstrapv1.Cache_Redis_Cluster{Addrs: []string{"127.0.0.1:1"}},
		},
	})
	if err == nil {
		t.Fatal("Provider with unreachable cluster addr should fail")
	}
	if !strings.Contains(err.Error(), "cache: build redis client") {
		t.Errorf("err = %q, want build prefix", err.Error())
	}
}

func TestProvider_SentinelUnreachable(t *testing.T) {
	_, _, err := Provider(context.Background(), &bootstrapv1.Cache{
		Redis: &bootstrapv1.Cache_Redis{
			Sentinel: &bootstrapv1.Cache_Redis_Sentinel{MasterName: "m", Addrs: []string{"127.0.0.1:1"}},
		},
	})
	if err == nil {
		t.Fatal("Provider with unreachable sentinel addr should fail")
	}
	if !strings.Contains(err.Error(), "cache: build redis client") {
		t.Errorf("err = %q, want build prefix", err.Error())
	}
}
