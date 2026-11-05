package contract

// contract_test.go —— Provider 工厂的构造期判据：段缺失跳过、addr 必填 fail-fast、
// buildOpts 的五行映射语义（addr 必写 / network+path 空不追加 / codec 未注册报错 /
// tls 按段存在与否）、after-build 钩子时序与错误传播、业务 Option 的覆盖序。
//
// 口径：webrtc 的 codec 只影响 DataChannel 上的消息编解码，离线信令面观测不到，
// 所以 codec 行判在「未注册即启动失败」+ buildOpts 计数上（mapping_offline 头部
// 记录了同一口径）。NewServer 的 nil 返回守卫是防御性的：当前 ServerOption 里
// 没有能置 init 出错的赋值路径，该守卫无对应变异行（记入验活报告）。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/kalandramo/bald/encoding"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	jsoncodec "github.com/kalandramo/bald/encoding/json"
	webrtc "github.com/kalandramo/bald/transport/webrtc"
)

// TestMain 显式注册 json codec（本仓库 codec 不做 init 自注册，先例见
// broker/redis/contract）。契约里 codec: json 的腿依赖这一行。
func TestMain(m *testing.M) {
	if encoding.GetCodec("json") == nil {
		encoding.MustRegister(jsoncodec.New())
	}
	os.Exit(m.Run())
}

func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("err = %v, want nil (skip)", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, isNil=%v), want (nil, nil)", srv, cleanup == nil)
	}
}

func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{},
	})
	if err == nil || !strings.Contains(err.Error(), "server.webrtc.addr is required") {
		t.Fatalf("err = %v, want addr required", err)
	}
}

func TestProvider_SectionPresentConstructs(t *testing.T) {
	addr := loopbackAddr(t)
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: addr},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil (webrtc factory yields no cleanup)")
	}
	wsrv, ok := srv.(*webrtc.Server)
	if !ok {
		t.Fatalf("srv = %T, want *webrtc.Server", srv)
	}
	if wsrv.Name() != Type {
		t.Fatalf("Name() = %q, want %q", wsrv.Name(), Type)
	}
	// Endpoint() 自带信令路径（缺省 /signal）。
	if ep := wsrv.Endpoint(); ep != "http://"+addr+"/signal" {
		t.Fatalf("Endpoint() = %q, want http://%s/signal", ep, addr)
	}
}

// --- buildOpts 五行 ---------------------------------------------------------

func TestBuildOpts_AddrOnly(t *testing.T) {
	opts, err := buildOpts(&bootstrapv1.Server_Webrtc{Addr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("buildOpts: %v", err)
	}
	// addr 是唯一无条件追加的行；其余字段全空 → 一条都不追加（默认值单源）。
	if len(opts) != 1 {
		t.Fatalf("len(opts) = %d, want 1 (addr only)", len(opts))
	}
}

func TestBuildOpts_AllFields(t *testing.T) {
	certPEM, keyPEM, _ := testPEMs(t)
	sec := &bootstrapv1.Server_Webrtc{
		Addr:    "127.0.0.1:0",
		Network: "tcp",
		Path:    "/wrtc-signal",
		Codec:   "json",
		Tls: &bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{
			CertPem: certPEM, KeyPem: keyPEM,
		}},
	}
	opts, err := buildOpts(sec)
	if err != nil {
		t.Fatalf("buildOpts: %v", err)
	}
	if len(opts) != 5 {
		t.Fatalf("len(opts) = %d, want 5 (addr/network/path/codec/tls)", len(opts))
	}
}

func TestBuildOpts_UnknownCodecFailsFast(t *testing.T) {
	// 底层形状：WithCodec 未注册即置 nil，init 再兜底 json——契约写 proto 实跑 json，
	// 一声不响。本包必须在装配期判红。
	_, err := buildOpts(&bootstrapv1.Server_Webrtc{Addr: "127.0.0.1:0", Codec: "nosuchcodec"})
	if err == nil || !strings.Contains(err.Error(), "not registered") {
		t.Fatalf("err = %v, want codec not registered", err)
	}
}

func TestBuildOpts_EmptyTlsSectionFailsFast(t *testing.T) {
	// 空 tls 段 = 「启用但没证书」，归 tlsconf 报错，不静默回落明文。
	_, err := buildOpts(&bootstrapv1.Server_Webrtc{
		Addr: "127.0.0.1:0",
		Tls:  &bootstrapv1.Server_TLS{},
	})
	if err == nil || !strings.Contains(err.Error(), "server.webrtc.tls") {
		t.Fatalf("err = %v, want tls attribution", err)
	}
}

// --- after-build 钩子 -------------------------------------------------------

func TestProvider_AfterBuildInvokedSameServer(t *testing.T) {
	var hookSrv *webrtc.Server
	srv, _, err := Provider(WithAfterBuild(func(_ context.Context, s *webrtc.Server) error {
		hookSrv = s
		return nil
	}))(context.Background(), &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: loopbackAddr(t)},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if hookSrv == nil {
		t.Fatal("after-build hook not invoked")
	}
	if any(hookSrv) != any(srv) { //nolint:staticcheck // 判「钩子拿到的就是产物」
		t.Fatal("hook received a different server than returned")
	}
}

func TestProvider_AfterBuildErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	_, _, err := Provider(WithAfterBuild(func(_ context.Context, _ *webrtc.Server) error {
		return boom
	}))(context.Background(), &bootstrapv1.Server{
		Webrtc: &bootstrapv1.Server_Webrtc{Addr: "127.0.0.1:0"},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "webrtc: after-build hook") {
		t.Fatalf("err = %v, want attribution", err)
	}
}

// --- 业务 Option 覆盖序 -----------------------------------------------------

func TestProvider_BusinessOptionsApplyAfterContract(t *testing.T) {
	contractAddr := loopbackAddr(t)
	businessAddr := loopbackAddr(t)
	srv, _, err := Provider(WithServerOptions(webrtc.WithAddress(businessAddr)))(
		context.Background(), &bootstrapv1.Server{
			Webrtc: &bootstrapv1.Server_Webrtc{Addr: contractAddr},
		})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if ep := srv.Endpoint(); ep != "http://"+businessAddr+"/signal" {
		t.Fatalf("Endpoint() = %q, want business addr %s (contract-first, business-overrides)", ep, businessAddr)
	}
}
