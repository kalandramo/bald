package contract

// contract_test.go —— RocketMQ 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. rocketmq 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. 段在而 name_server_addrs 为空、或含空串元素 → fail-fast（v2 broker 的
//     Address() 会兜底到内置默认 127.0.0.1:9876——落点不由配置决定）；
//  3. codec 已注册 → 落到 broker.Options；codec 未注册 → fail-fast
//     （底层 WithCodec 对未注册名静默置 nil，首包才炸 errNoCodec）；
//  4. driver_type 只收 ""/"v2"：v5/aliyun 无代码支撑，配了必须报错而不是
//     静默按 v2 跑（proto 注释「默认 v5」是文档漂移，代码实况在此收口）；
//  5. afterBuild 在 NewServer 之后被调用且拿到非 nil server；其 error 短路装配。
//
// 与 kafka 族的判法差异：rocketmq 的地址经 options.Context 传递、Init 才读出
// ——构造期 Address()/NameServer 没有公开可观测面，「契约地址活在服务端面」
// 只能在 Start 后判（归 mapping_offline_test.go）；构造期只判 codec 与端点形状。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/broker/rocketmq/option"
	rocketmq "github.com/kalandramo/bald/transport/rocketmq"
)

// testCodecName 只在测试进程注册——codec 映射腿用它，与 broker 的默认
// （json）区分开：契约值落没落，拿默认名判不出来。
const testCodecName = "rt-contract-test"

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

func rocketmqServerCfg(addrs []string, codec, driverType string) *bootstrapv1.Server {
	return &bootstrapv1.Server{Rocketmq: &bootstrapv1.Server_Rocketmq{
		NameServerAddrs: addrs, Codec: codec, DriverType: driverType,
	}}
}

// TestProvider_SectionMissingSkips rocketmq 段缺失 → nil server（合法跳过，非错误）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (rocketmq section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrsFailFast 配了段却不给地址属漏配：v2 的 Address() 兜底
// 到 127.0.0.1:9876，会让「写了配置」与「没写配置」落到同一个不由配置决定的
// 地址，必须显式报错。
func TestProvider_EmptyAddrsFailFast(t *testing.T) {
	for _, cfg := range []*bootstrapv1.Server{
		rocketmqServerCfg(nil, "", ""),
		rocketmqServerCfg([]string{}, "", ""),
	} {
		srv, cleanup, err := Provider()(context.Background(), cfg)
		if err == nil {
			t.Fatalf("Provider() error = nil, want fail-fast on empty addrs (srv=%v)", srv)
		}
		if !strings.Contains(err.Error(), "name_server_addrs is required") {
			t.Fatalf("err = %v, want attribution to server.rocketmq.name_server_addrs", err)
		}
		if cleanup != nil {
			t.Fatal("cleanup != nil on error path")
		}
	}
}

// TestProvider_AddrsEmptyElementFailFast 含空串元素的列表会把空串顶成首址——
// 端点/日志落空地址，逐元素判，不留静默形状。
func TestProvider_AddrsEmptyElementFailFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9876", ""}, "", ""))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on empty addr element")
	}
	if !strings.Contains(err.Error(), "is empty") {
		t.Fatalf("err = %v, want attribution to the empty element", err)
	}
}

// TestProvider_UnknownCodecFailsFast 未注册的 codec 名必须装配期报错——
// 底层静默置 nil、首条消息才炸（errNoCodec），那个失败点离配置太远。
func TestProvider_UnknownCodecFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9876"}, "no-such-codec-xyz", ""))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on unregistered codec")
	}
	if !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("err = %v, want registration hint", err)
	}
}

// TestProvider_DriverTypeFailFastOnUnimplemented v5/aliyun 没有代码
// （transport/rocketmq 的 init 恒构造 v2 broker），配了必须报错——静默按 v2 跑
// 就是「配置说谎」。
func TestProvider_DriverTypeFailFastOnUnimplemented(t *testing.T) {
	for _, v := range []string{"v5", "aliyun", "V2", "v3"} {
		_, _, err := Provider()(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9876"}, "", v))
		if err == nil {
			t.Fatalf("Provider() error = nil for driver_type %q, want fail-fast", v)
		}
		if !strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("err = %v, want not-implemented attribution for %q", err, v)
		}
	}
}

// TestProvider_DriverTypeV2Accepted ""/"v2" 放行且落 v2 驱动。
func TestProvider_DriverTypeV2Accepted(t *testing.T) {
	for _, v := range []string{"", "v2"} {
		srv, _, err := Provider()(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9877"}, "", v))
		if err != nil {
			t.Fatalf("Provider() error = %v for driver_type %q, want nil", err, v)
		}
		if _, ok := srv.(*rocketmq.Server); !ok {
			t.Fatalf("srv type = %T, want *rocketmq.Server", srv)
		}
	}
	// 驱动常量本身的后端守卫：v2 名字变了就说明 broker 换向，本收口要重审。
	if got := string(option.DriverTypeV2); got != "v2" {
		t.Fatalf("option.DriverTypeV2 = %q, want \"v2\"", got)
	}
}

// TestProvider_SectionPresentConstructs 段在且合法 → 构造 *rocketmq.Server，
// codec 落到 broker.Options；且构造期端点必须为空——appkit 的 waitForEndpoints
// 把非空端点判为「可服务」，构造完还没 Start 就非空是假就绪。
//
// 构造期不断言 NameServer 落点：地址经 options.Context 传递、Init 才读出，
// 构造后 Address() 读到的是兜底默认——「契约地址活在端点」由 Start 后的腿判。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(),
		rocketmqServerCfg([]string{"127.0.0.1:9878", "127.0.0.1:9879"}, testCodecName, "v2"))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (rocketmq factory yields no cleanup)")
	}
	rsrv, ok := srv.(*rocketmq.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *rocketmq.Server", srv)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() before Start = %q, want \"\"（readiness 闸门看的是 Start 后的描述性端点）", ep)
	}
	if codec := rsrv.Options().Codec; codec == nil || codec.Name() != testCodecName {
		t.Fatalf("Options().Codec = %v, want registered codec %q", codec, testCodecName)
	}
}

// TestProvider_EmptyCodecKeepsBrokerDefault codec 空串 = 「没配」，不追加
// WithCodec——底层 WithCodec("") 会把默认 codec 置 nil（GetCodec("") 即 nil），
// 首包才炸。三态语义：未配 ≠ 配了个把默认干掉的值。
func TestProvider_EmptyCodecKeepsBrokerDefault(t *testing.T) {
	srv, _, err := Provider()(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9880"}, "", ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	rsrv, ok := srv.(*rocketmq.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *rocketmq.Server", srv)
	}
	if codec := rsrv.Options().Codec; codec == nil || codec.Name() != "json" {
		t.Fatalf("Options().Codec = %v, want broker default json（空 codec 字段被当成 WithCodec(\"\") 追加了？）", codec)
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *rocketmq.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *rocketmq.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9881"}, "", ""))
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
	p := Provider(WithAfterBuild(func(context.Context, *rocketmq.Server) error { return sentinel }))
	_, _, err := p(context.Background(), rocketmqServerCfg([]string{"127.0.0.1:9882"}, "", ""))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "after-build hook") {
		t.Fatalf("err = %v, want after-build attribution", err)
	}
}

// TestProvider_UnusedDriverTypeArgumentIsHarmless 位置参数 driverType 被
// transport 存而不用（init 恒 v2 broker）——本工厂仍传收口后的驱动值，
// 两处真相若分叉（有人补了 v5 分支），这条腿先红。
func TestProvider_UnusedDriverTypeArgumentIsHarmless(t *testing.T) {
	srv := rocketmq.NewServer(option.DriverTypeV2, rocketmq.WithNameServer([]string{"127.0.0.1:9883"}))
	if got := srv.Name(); got != "rocketmq" {
		t.Fatalf("Name() = %q, want rocketmq", got)
	}
	if brokerName := srv.Broker.Name(); brokerName != "rocketmqV2" {
		t.Fatalf("Broker.Name() = %q, want rocketmqV2（transport 的 init 换后端了——driver_type 收口口径要重审）", brokerName)
	}
}
