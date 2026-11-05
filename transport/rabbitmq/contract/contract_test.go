package contract

// contract_test.go —— RabbitMQ 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. rabbitmq 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. 段在而 addrs 为空、或含空串元素 → fail-fast（broker 的 Init 丢弃空元素
//     并整体兜底到 amqp://guest:guest@127.0.0.1:5672——落点不由配置决定，
//     默认值还自带凭证）；
//  3. codec 已注册 → 落到 broker.Options；codec 未注册 → fail-fast
//     （底层 WithCodec 对未注册名静默置 nil，首包才炸 errNoCodec）；
//  4. afterBuild 在 NewServer 之后被调用且拿到非 nil server；其 error 短路装配；
//  5. 业务 Option 追加在契约字段之后（addrs 覆盖通道的构造期证据）。
//
// 判法与 kafka 的差异：rabbitmq 的 Connect 是完整 AMQP 握手，进程内假后端
// 不可行——「Start 后端点变描述性」离线不可判（归真集群集成），本层能判的
// 是映射形状与 fail-fast；运行期失败形状见 mapping_offline_test.go。

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

	"github.com/kalandramo/bald/transport/rabbitmq"
)

// testCodecName 只在测试进程注册——codec 映射腿用它，与 broker 的默认
// （json）区分开：契约值落没落，拿默认名判不出来。
const testCodecName = "rmq-contract-test"

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

func rabbitmqServerCfg(addrs []string, codec string) *bootstrapv1.Server {
	return &bootstrapv1.Server{Rabbitmq: &bootstrapv1.Server_Rabbitmq{Addrs: addrs, Codec: codec}}
}

// TestProvider_SectionMissingSkips rabbitmq 段缺失 → nil server（合法跳过，非错误）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (rabbitmq section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrsFailFast 配了段却不给地址属漏配：Init 的默认兜底会让
// 「写了配置」与「没写配置」落到同一个不由配置决定的地址（还带 guest 凭证），
// 必须显式报错。
func TestProvider_EmptyAddrsFailFast(t *testing.T) {
	for _, cfg := range []*bootstrapv1.Server{
		rabbitmqServerCfg(nil, ""),
		rabbitmqServerCfg([]string{}, ""),
	} {
		srv, cleanup, err := Provider()(context.Background(), cfg)
		if err == nil {
			t.Fatalf("Provider() error = nil, want fail-fast on empty addrs (srv=%v)", srv)
		}
		if !strings.Contains(err.Error(), "server.rabbitmq.addrs is required") {
			t.Fatalf("err = %v, want attribution to server.rabbitmq.addrs", err)
		}
		if cleanup != nil {
			t.Fatal("cleanup != nil on error path")
		}
	}
}

// TestProvider_AddrsEmptyElementFailFast 含空串元素的列表会被 Init 静默丢弃——
// 全空时整体换成默认地址，落点不由配置决定；逐元素判，不留静默形状。
func TestProvider_AddrsEmptyElementFailFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), rabbitmqServerCfg([]string{"127.0.0.1:15672", ""}, ""))
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
	_, _, err := Provider()(context.Background(), rabbitmqServerCfg([]string{"127.0.0.1:15673"}, "no-such-codec-xyz"))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on unregistered codec")
	}
	if !strings.Contains(err.Error(), "is not registered") {
		t.Fatalf("err = %v, want registration hint", err)
	}
}

// TestProvider_SectionPresentConstructs 段在且合法 → 构造 *rabbitmq.Server，
// addrs/codec 落到 broker.Options（构造期读原始值；Init 才规范化，离线跑不到
// Init 之后）；且构造期端点必须为空——appkit 的 waitForEndpoints 把非空端点
// 判为「可服务」，构造完还没 Start 就非空是假就绪。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	addrs := []string{"127.0.0.1:15674", "127.0.0.1:15675"}
	srv, cleanup, err := Provider()(context.Background(), rabbitmqServerCfg(addrs, testCodecName))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (rabbitmq factory yields no cleanup)")
	}
	rsrv, ok := srv.(*rabbitmq.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *rabbitmq.Server", srv)
	}
	if ep := rsrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() before Start = %q, want \"\"（readiness 闸门看的是 Start 后的描述性端点）", ep)
	}
	if got := rsrv.Options().Addrs; !reflect.DeepEqual(got, addrs) {
		t.Fatalf("Options().Addrs = %v, want %v", got, addrs)
	}
	if codec := rsrv.Options().Codec; codec == nil || codec.Name() != testCodecName {
		t.Fatalf("Options().Codec = %v, want registered codec %q", codec, testCodecName)
	}
}

// TestProvider_EmptyCodecKeepsBrokerDefault codec 空串 = 「没配」，不追加
// WithCodec——底层 WithCodec("") 会把默认 codec 置 nil（GetCodec("") 即 nil），
// 首包才炸。三态语义：未配 ≠ 配了个把默认干掉的值。
func TestProvider_EmptyCodecKeepsBrokerDefault(t *testing.T) {
	srv, _, err := Provider()(context.Background(), rabbitmqServerCfg([]string{"127.0.0.1:15676"}, ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	rsrv, ok := srv.(*rabbitmq.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *rabbitmq.Server", srv)
	}
	if codec := rsrv.Options().Codec; codec == nil || codec.Name() != "json" {
		t.Fatalf("Options().Codec = %v, want broker default json（空 codec 字段被当成 WithCodec(\"\") 追加了？）", codec)
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *rabbitmq.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *rabbitmq.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), rabbitmqServerCfg([]string{"127.0.0.1:15677"}, ""))
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
	p := Provider(WithAfterBuild(func(context.Context, *rabbitmq.Server) error { return sentinel }))
	_, _, err := p(context.Background(), rabbitmqServerCfg([]string{"127.0.0.1:15678"}, ""))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "after-build hook") {
		t.Fatalf("err = %v, want after-build attribution", err)
	}
}

// TestProvider_BusinessOptionsAppendAfterContract 业务 Option 追加在契约 Option 之后。
//
// 覆盖序的构造期证据（Options().Addrs 读的是业务值）；拨号目标的线上形态由
// TestMappingOffline_BusinessAddressOverridesContract 判。
//
// 口径记录：rabbitmq 的地址没有 NewServer 位置参数通道（与 graphql 的 addr
// 不同），契约段与业务 WithAddress 走的是同一个 broker.Options 覆盖栈，末者胜。
func TestProvider_BusinessOptionsAppendAfterContract(t *testing.T) {
	businessAddrs := []string{"127.0.0.1:15680"}
	var hooked bool
	p := Provider(
		WithServerOptions(rabbitmq.WithAddress(businessAddrs)),
		WithAfterBuild(func(_ context.Context, s *rabbitmq.Server) error { hooked = true; return nil }),
	)
	srv, _, err := p(context.Background(), rabbitmqServerCfg([]string{"127.0.0.1:15679"}, ""))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if !hooked {
		t.Fatal("afterBuild hook not invoked with business options")
	}
	rsrv, ok := srv.(*rabbitmq.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *rabbitmq.Server", srv)
	}
	if got := rsrv.Options().Addrs; !reflect.DeepEqual(got, businessAddrs) {
		t.Fatalf("Options().Addrs = %v, want %v（业务 Option 没排在契约之后？）", got, businessAddrs)
	}
}
