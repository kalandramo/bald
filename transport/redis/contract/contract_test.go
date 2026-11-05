package contract

// contract_test.go —— Redis 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. redis_server 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. 段在而 addr 为空 → fail-fast（pubsub broker 的 Init 会兜底到内置默认
//     redis://127.0.0.1:6379——「写了配置」与「没写配置」落同一个地址）；
//  3. codec 已注册 → 落到 broker.Options；codec 未注册 → fail-fast
//     （底层 WithCodec 对未注册名静默置 nil，首包才炸 errNoCodec）；
//  4. afterBuild 在 NewServer 之后被调用且拿到非 nil server；其 error 短路装配；
//  5. 业务 Option 追加在契约字段之后（addr 覆盖通道的构造期证据）。
//
// 与 kafka 族的判法差异：redis 不开监听，且 pubsub 的 Address() 在 Init 前是
// 空串（构造期没有地址面可断言）——构造判据落在 Options().Addrs/Codec 上；
// 「Start 后端点变描述性、Stop 后回空」归 mapping_offline_test.go 判（appkit 的
// waitForEndpoints 按它决定 readiness）。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	redis "github.com/kalandramo/bald/transport/redis"
)

// testCodecName 只在测试进程注册——codec 映射腿用它，与 broker 的默认
// （json）区分开：契约值落没落，拿默认名判不出来。
const testCodecName = "kt-contract-test"

type testCodec struct{}

func (testCodec) Name() string { return testCodecName }
func (testCodec) Marshal(v any) ([]byte, error) {
	return jsoncodec.New().Marshal(v)
}
func (testCodec) Unmarshal(data []byte, v any) error {
	return jsoncodec.New().Unmarshal(data, v)
}

func TestMain(m *testing.M) {
	// json 不随 import 自注册（encoding 注册表是显式的）；broker 默认 codec
	// 惰性查表，注册必须早于构造——TestMain 是最早的统一落点。
	encoding.MustRegister(jsoncodec.New())
	encoding.MustRegister(testCodec{})
	os.Exit(m.Run())
}

func redisServerCfg(addr, codec string) *bootstrapv1.Server {
	return &bootstrapv1.Server{RedisServer: &bootstrapv1.Server_RedisServer{Addr: addr, Codec: codec}}
}

// TestProvider_SectionMissingSkips redis_server 段缺失 → nil server（合法跳过，非错误）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (redis_server section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrFailFast 配了段却不给地址属漏配：pubsub 的 Init 兜底到
// redis://127.0.0.1:6379，会让「写了配置」与「没写配置」落到同一个不由配置决定
// 的地址，必须显式报错。
func TestProvider_EmptyAddrFailFast(t *testing.T) {
	for _, cfg := range []*bootstrapv1.Server{
		redisServerCfg("", ""),
		redisServerCfg("", testCodecName),
	} {
		srv, cleanup, err := Provider()(context.Background(), cfg)
		if err == nil {
			t.Fatalf("Provider() error = nil, want fail-fast on empty addr (srv=%v)", srv)
		}
		if !strings.Contains(err.Error(), "server.redis_server.addr is required") {
			t.Fatalf("err = %v, want attribution to server.redis_server.addr", err)
		}
		if cleanup != nil {
			t.Fatal("cleanup != nil on error path")
		}
	}
}

// TestProvider_UnknownCodecFailsFast 未注册的 codec 名必须装配期报错——
// 底层静默置 nil、首条消息才炸（errNoCodec），那个失败点离配置太远。
func TestProvider_UnknownCodecFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), redisServerCfg("127.0.0.1:16380", "no-such-codec-xyz"))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on unregistered codec")
	}
	if !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("err = %v, want registration hint", err)
	}
}

// TestProvider_SectionPresentConstructs 段在且合法 → 构造 *redis.Server，
// addr/codec 落到 broker.Options；且构造期端点必须为空——appkit 的
// waitForEndpoints 把非空端点判为「可服务」，构造完还没 Start 就非空是假就绪。
//
// 构造期不断言 Address()：pubsub 的地址面在 Init 才落（未 Init 恒空），
// 「契约地址活在服务端面」由 Start 后的端点腿判。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	addr := "127.0.0.1:16381"
	srv, cleanup, err := Provider()(context.Background(), redisServerCfg(addr, testCodecName))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (redis factory yields no cleanup)")
	}
	rsrv, ok := srv.(*redis.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *redis.Server", srv)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() before Start = %q, want \"\"（readiness 闸门看的是 Start 后的描述性端点）", ep)
	}
	if got := rsrv.Options().Addrs; len(got) != 1 || got[0] != addr {
		t.Fatalf("Options().Addrs = %v, want [%s]", got, addr)
	}
	if codec := rsrv.Options().Codec; codec == nil || codec.Name() != testCodecName {
		t.Fatalf("Options().Codec = %v, want registered codec %q", codec, testCodecName)
	}
}

// TestProvider_EmptyCodecKeepsBrokerDefault codec 空串 = 「没配」，不追加
// WithCodec——底层 WithCodec("") 会把默认 codec 置 nil（GetCodec("") 即 nil），
// 首包才炸。三态语义：未配 ≠ 配了个把默认干掉的值。
func TestProvider_EmptyCodecKeepsBrokerDefault(t *testing.T) {
	srv, _, err := Provider()(context.Background(), redisServerCfg("127.0.0.1:16382", ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	rsrv, ok := srv.(*redis.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *redis.Server", srv)
	}
	if codec := rsrv.Options().Codec; codec == nil || codec.Name() != "json" {
		t.Fatalf("Options().Codec = %v, want broker default json（空 codec 字段被当成 WithCodec(\"\") 追加了？）", codec)
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *redis.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *redis.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), redisServerCfg("127.0.0.1:16383", ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if got == nil {
		t.Fatal("afterBuild hook not invoked")
	}
	if any(got) != any(srv) {
		t.Fatal("hook received a different server than the one returned")
	}
}

// TestProvider_AfterBuildErrorPropagates 回调返回 error → 装配失败并带归因。
func TestProvider_AfterBuildErrorPropagates(t *testing.T) {
	sentinel := errors.New("wiring failed")
	p := Provider(WithAfterBuild(func(context.Context, *redis.Server) error { return sentinel }))
	_, _, err := p(context.Background(), redisServerCfg("127.0.0.1:16384", ""))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "after-build hook") {
		t.Fatalf("err = %v, want after-build attribution", err)
	}
}

// TestProvider_BusinessOptionsAppendAfterContract 业务 Option 追加在契约 Option 之后。
//
// 覆盖序的构造期证据（Options().Addrs 读的是业务值）；线上形态（Start 后端点
// 读业务地址）由 TestMappingOffline_BusinessAddressOverridesContract 判。
//
// 口径记录：redis 的地址没有 NewServer 位置参数通道（与 graphql 的 addr 不同），
// 契约段与业务 WithAddress 走的是同一个 broker.Options 覆盖栈，末者胜。
func TestProvider_BusinessOptionsAppendAfterContract(t *testing.T) {
	businessAddr := "127.0.0.1:16386"
	var hooked bool
	p := Provider(
		WithServerOptions(redis.WithAddress(businessAddr)),
		WithAfterBuild(func(_ context.Context, s *redis.Server) error { hooked = true; return nil }),
	)
	srv, _, err := p(context.Background(), redisServerCfg("127.0.0.1:16385", ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if !hooked {
		t.Fatal("afterBuild hook not invoked with business options")
	}
	rsrv, ok := srv.(*redis.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *redis.Server", srv)
	}
	if got := rsrv.Options().Addrs; len(got) != 1 || got[0] != businessAddr {
		t.Fatalf("Options().Addrs = %v, want [%s]（业务 Option 没排在契约之后？）", got, businessAddr)
	}
}
