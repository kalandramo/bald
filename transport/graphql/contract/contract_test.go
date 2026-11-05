package contract

// contract_test.go —— GraphQL 契约装配的字段映射与三态语义。
//
// 锁定的不变量：
//  1. 段缺失 → (nil, nil, nil)（引擎跳过构建）；
//  2. addr 为空 → fail-fast（空串会绑随机端口，落点不由配置决定）；
//  3. 无 tls 段 → 不追加任何 Option（明文 http，不塞空证书）；
//  4. 空 tls 段 → 交给 tlsconf 报错（不静默回落明文）；
//  5. afterBuild 在 NewServer 之后被调用且拿到非 nil server；其 error 短路装配；
//  6. 业务 Option 追加在契约字段之后（TLS 覆盖通道的构造期证据）。

import (
	"context"
	"crypto/tls"
	"errors"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	graphql "github.com/kalandramo/bald/transport/graphql"
)

func gqlServerCfg(addr string, tlsSec *bootstrapv1.Server_TLS) *bootstrapv1.Server {
	return &bootstrapv1.Server{Graphql: &bootstrapv1.Server_Graphql{Addr: addr, Tls: tlsSec}}
}

// TestProvider_SectionMissingSkips 段缺失 → nil server。
func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (graphql section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_EmptyAddrFailsFast 配了段却不给地址属漏配，必须显式报错。
func TestProvider_EmptyAddrFailsFast(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), gqlServerCfg("", nil))
	if err == nil {
		t.Fatalf("Provider() error = nil, want fail-fast on empty addr (srv=%v)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on error path")
	}
}

// TestProvider_SectionPresentConstructs addr 非空 → 构造出 *graphql.Server 且端点即契约地址。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	srv, _, err := Provider()(context.Background(), gqlServerCfg("127.0.0.1:19801", nil))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	got, ok := srv.(*graphql.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *graphql.Server", srv)
	}
	if ep := got.Endpoint(); ep != "http://127.0.0.1:19801" {
		t.Fatalf("Endpoint() = %q, want http://127.0.0.1:19801", ep)
	}
}

// TestBuildOpts_NoTlsSectionYieldsNoOptions 无 tls 段 → Option 序列为空。
//
// 反向陷阱防护：恒传 WithTLSConfig(nil) 会让「明文」变成「TLS 配置为 nil 的 TLS」，
// 而 graphql 的 Endpoint 判的是 tlsConfig != nil——塞 nil 恰好不会翻案，但会把
// 「未配置」和「配置成空」混为一谈。
func TestBuildOpts_NoTlsSectionYieldsNoOptions(t *testing.T) {
	opts, err := buildOpts(&bootstrapv1.Server_Graphql{Addr: "127.0.0.1:19802"})
	if err != nil {
		t.Fatalf("buildOpts: %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("len(buildOpts) = %d, want 0 (absent tls section must not append Option)", len(opts))
	}
}

// TestProvider_EmptyTlsSectionFailsFast 空 tls 段 = 「启用但没证书」，必须报错。
func TestProvider_EmptyTlsSectionFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), gqlServerCfg("127.0.0.1:19803", &bootstrapv1.Server_TLS{}))
	if err == nil {
		t.Fatal("error = nil, want fail-fast on empty tls section (no silent plaintext fallback)")
	}
}

// TestProvider_TlsSectionSetsHttpsEndpoint tls 段在 → 方案切 https（读的是装配完的
// 实例状态，不是 Option 计数）。
func TestProvider_TlsSectionSetsHttpsEndpoint(t *testing.T) {
	pemCert, pemKey, caPem := testPEMs(t)
	srv, _, err := Provider()(context.Background(), gqlServerCfg("127.0.0.1:19804",
		&bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{CertPem: pemCert, KeyPem: pemKey, CaPem: caPem}}))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	got, ok := srv.(*graphql.Server)
	if !ok {
		t.Fatalf("srv type = %T, want *graphql.Server", srv)
	}
	if ep := got.Endpoint(); ep != "https://127.0.0.1:19804" {
		t.Fatalf("Endpoint() = %q, want https://127.0.0.1:19804（tls 段没切方案）", ep)
	}
}

// TestProvider_AfterBuildHookInvoked 构造后回调被调用，且拿到返回的同一 server。
func TestProvider_AfterBuildHookInvoked(t *testing.T) {
	var got *graphql.Server
	p := Provider(WithAfterBuild(func(_ context.Context, s *graphql.Server) error {
		got = s
		return nil
	}))
	srv, _, err := p(context.Background(), gqlServerCfg("127.0.0.1:19805", nil))
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

// TestProvider_AfterBuildErrorPropagates 回调返回 error → 装配失败。
func TestProvider_AfterBuildErrorPropagates(t *testing.T) {
	sentinel := errors.New("wiring failed")
	p := Provider(WithAfterBuild(func(context.Context, *graphql.Server) error { return sentinel }))
	_, _, err := p(context.Background(), gqlServerCfg("127.0.0.1:19806", nil))
	if !errors.Is(err, sentinel) {
		t.Fatalf("Provider() error = %v, want wrapped %v", err, sentinel)
	}
}

// TestProvider_BusinessOptionsAppendAfterContract 业务 Option 追加在契约 Option 之后。
//
// 覆盖序的构造期证据；其线上形态（业务 WithTLSConfig 顶掉契约证书）由
// TestMappingOffline_BusinessOptionOverridesTls 判。
//
// 口径记录：graphql 的 addr 是 NewServer 的**位置参数**，本模块没有 WithAddress，
// 所以契约段是 addr 的唯一来源，没有覆盖通道（与 websocket 同形，与 tcp 不同）。
func TestProvider_BusinessOptionsAppendAfterContract(t *testing.T) {
	var seenContractTLS bool
	business := tls.Config{MinVersion: tls.VersionTLS12}
	p := Provider(
		WithServerOptions(graphql.WithTLSConfig(&business)),
		WithAfterBuild(func(context.Context, *graphql.Server) error { seenContractTLS = true; return nil }),
	)
	certPEM, keyPEM, _ := testPEMs(t)
	srv, _, err := p(context.Background(), gqlServerCfg("127.0.0.1:19807",
		&bootstrapv1.Server_TLS{Config: &bootstrapv1.Server_TLS_Config{CertPem: certPEM, KeyPem: keyPEM}}))
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv == nil || !seenContractTLS {
		t.Fatalf("srv=%v afterBuildRan=%v, want server built and hook run", srv, seenContractTLS)
	}
}
