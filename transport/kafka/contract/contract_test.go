package contract

// contract_test.go —— Kafka 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. kafka 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. 段在而 addrs 为空、或含空串元素 → fail-fast（broker/kafka 的 Init 会把
//     两种形状都归一成内置默认 127.0.0.1:9092——落点不由配置决定）；
//  3. codec 已注册 → 落到 broker.Options；codec 未注册 → fail-fast
//     （底层 WithCodec 对未注册名静默置 nil，首包才炸 errNoCodec）；
//  4. afterBuild 在 NewServer 之后被调用且拿到非 nil server；其 error 短路装配；
//  5. 业务 Option 追加在契约字段之后（addrs 覆盖通道的构造期证据）。
//
// 与 HTTP 族的判法差异：kafka 不开监听，构造成功的判据落在
// broker.Options / Address() 这些构造期可观测面上；「Start 后端点变描述性、
// Stop 后回空」的就绪语义归 mapping_offline_test.go 判（appkit 的
// waitForEndpoints 按它决定 readiness）。

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/kalandramo/bald/encoding"
	jsoncodec "github.com/kalandramo/bald/encoding/json"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/kafka"
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

func kafkaServerCfg(addrs []string, codec string) *bootstrapv1.Server {
	return &bootstrapv1.Server{Kafka: &bootstrapv1.Server_Kafka{Addrs: addrs, Codec: codec}}
}

// TestProvider_SectionMissingSkips kafka 段缺失 → nil server（合法跳过，非错误）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (kafka section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrsFailFast 配了段却不给地址属漏配：默认 127.0.0.1:9092
// 兜底会让「写了配置」与「没写配置」落到同一个不由配置决定的地址，必须显式报错。
func TestProvider_EmptyAddrsFailFast(t *testing.T) {
	for _, cfg := range []*bootstrapv1.Server{
		kafkaServerCfg(nil, ""),
		kafkaServerCfg([]string{}, ""),
	} {
		srv, cleanup, err := Provider()(context.Background(), cfg)
		if err == nil {
			t.Fatalf("Provider() error = nil, want fail-fast on empty addrs (srv=%v)", srv)
		}
		if !strings.Contains(err.Error(), "server.kafka.addrs is required") {
			t.Fatalf("err = %v, want attribution to server.kafka.addrs", err)
		}
		if cleanup != nil {
			t.Fatal("cleanup != nil on error path")
		}
	}
}

// TestProvider_AddrsEmptyElementFailFast 含空串元素的列表同样被 Init 归一
// （空元素被丢弃，全空则整体换默认地址）——逐元素判，不留静默形状。
func TestProvider_AddrsEmptyElementFailFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), kafkaServerCfg([]string{"127.0.0.1:19092", ""}, ""))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on empty addr element")
	}
	if !strings.Contains(err.Error(), "must not contain empty entries") {
		t.Fatalf("err = %v, want attribution to the empty element", err)
	}
}

// TestProvider_UnknownCodecFailsFast 未注册的 codec 名必须装配期报错——
// 底层静默置 nil、首条消息才炸（errNoCodec），那个失败点离配置太远。
func TestProvider_UnknownCodecFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), kafkaServerCfg([]string{"127.0.0.1:19093"}, "no-such-codec-xyz"))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on unregistered codec")
	}
	if !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("err = %v, want registration hint", err)
	}
}

// TestProvider_SectionPresentConstructs 段在且合法 → 构造 *kafka.Server，
// addrs/codec 落到 broker.Options；且构造期端点必须为空——appkit 的
// waitForEndpoints 把非空端点判为「可服务」，构造完还没 Start 就非空是假就绪。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	addrs := []string{"127.0.0.1:19094", "127.0.0.1:19095"}
	srv, cleanup, err := Provider()(context.Background(), kafkaServerCfg(addrs, testCodecName))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (kafka factory yields no cleanup)")
	}
	ksrv, ok := srv.(*kafka.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *kafka.Server", srv)
	}
	if ep := ksrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() before Start = %q, want \"\"（readiness 闸门看的是 Start 后的描述性端点）", ep)
	}
	if got := ksrv.Options().Addrs; !reflect.DeepEqual(got, addrs) {
		t.Fatalf("Options().Addrs = %v, want %v", got, addrs)
	}
	if got := ksrv.Address(); got != addrs[0] {
		t.Fatalf("Address() = %q, want %s", got, addrs[0])
	}
	if codec := ksrv.Options().Codec; codec == nil || codec.Name() != testCodecName {
		t.Fatalf("Options().Codec = %v, want registered codec %q", codec, testCodecName)
	}
}

// TestProvider_EmptyCodecKeepsBrokerDefault codec 空串 = 「没配」，不追加
// WithCodec——底层 WithCodec("") 会把默认 codec 置 nil（GetCodec("") 即 nil），
// 首包才炸。三态语义：未配 ≠ 配了个把默认干掉的值。
func TestProvider_EmptyCodecKeepsBrokerDefault(t *testing.T) {
	srv, _, err := Provider()(context.Background(), kafkaServerCfg([]string{"127.0.0.1:19101"}, ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	ksrv, ok := srv.(*kafka.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *kafka.Server", srv)
	}
	if codec := ksrv.Options().Codec; codec == nil || codec.Name() != "json" {
		t.Fatalf("Options().Codec = %v, want broker default json（空 codec 字段被当成 WithCodec(\"\") 追加了？）", codec)
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *kafka.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *kafka.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), kafkaServerCfg([]string{"127.0.0.1:19096"}, ""))
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
	p := Provider(WithAfterBuild(func(context.Context, *kafka.Server) error { return sentinel }))
	_, _, err := p(context.Background(), kafkaServerCfg([]string{"127.0.0.1:19097"}, ""))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "after-build hook") {
		t.Fatalf("err = %v, want after-build attribution", err)
	}
}

// TestProvider_BusinessOptionsAppendAfterContract 业务 Option 追加在契约 Option 之后。
//
// 覆盖序的构造期证据；线上形态（Address/Endpoint 读的是被顶掉的值）由
// TestMappingOffline_BusinessAddressOverridesContract 判。
//
// 口径记录：kafka 的地址没有 NewServer 位置参数通道（与 graphql 的 addr 不同），
// 契约段与业务 WithAddress 走的是同一个 broker.Options 覆盖栈，末者胜。
func TestProvider_BusinessOptionsAppendAfterContract(t *testing.T) {
	businessAddr := "127.0.0.1:19099"
	var hooked bool
	p := Provider(
		WithServerOptions(kafka.WithAddress([]string{businessAddr})),
		WithAfterBuild(func(_ context.Context, s *kafka.Server) error { hooked = true; return nil }),
	)
	srv, _, err := p(context.Background(), kafkaServerCfg([]string{"127.0.0.1:19098"}, ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if !hooked {
		t.Fatal("afterBuild hook not invoked with business options")
	}
	ksrv, ok := srv.(*kafka.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *kafka.Server", srv)
	}
	if got := ksrv.Address(); got != businessAddr {
		t.Fatalf("Address() = %q, want business %s（业务 Option 没排在契约之后？）", got, businessAddr)
	}
}
