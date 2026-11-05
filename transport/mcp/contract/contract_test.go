package contract

// contract_test.go —— Provider 工厂的构造期判据：段缺失跳过、address 必填
// fail-fast、buildOpts 的映射语义（address+形态无条件写 / name+version 空不追加）、
// after-build 钩子时序与错误传播、形态强制 HTTP 的可观测落点（STDIO 不绑端口）。
//
// 口径：契约 mcp 段没有 type 字段，本包无条件写 ServerTypeHTTP——判据是
// 「Start 之后 Endpoint() 非空」：STDIO/in-process 都不产生 endpoint，
// 形态被摘掉时这条腿直接判红（mapping_offline 再打协议层）。

import (
	"context"
	"errors"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	mcp "github.com/kalandramo/bald/transport/mcp"
)

func TestProvider_SectionMissingSkips(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("err = %v, want nil (skip)", err)
	}
	if srv != nil || cleanup != nil {
		t.Fatalf("(srv, cleanup) = (%v, isNil=%v), want (nil, nil)", srv, cleanup == nil)
	}
}

func TestProvider_EmptyAddressFailsFast(t *testing.T) {
	_, _, err := Provider()(context.Background(), &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{},
	})
	if err == nil || !strings.Contains(err.Error(), "server.mcp.address is required") {
		t.Fatalf("err = %v, want address required", err)
	}
}

func TestProvider_SectionPresentConstructs(t *testing.T) {
	srv, cleanup, err := Provider()(context.Background(), &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil, want nil")
	}
	if _, ok := srv.(*mcp.Server); !ok {
		t.Fatalf("srv = %T, want *mcp.Server", srv)
	}
}

// --- buildOpts 映射行 -------------------------------------------------------

func TestBuildOpts_AddressOnly(t *testing.T) {
	opts := buildOpts(&bootstrapv1.Server_Mcp{Address: "127.0.0.1:0"})
	// address + 形态（HTTP）是两条无条件写；name/version 空 → 不追加，
	// 保留实现默认（"MCP Server"/"1.0.0"）——initialize 响应里 serverInfo 是必填面。
	if len(opts) != 2 {
		t.Fatalf("len(opts) = %d, want 2 (address + serve type)", len(opts))
	}
}

func TestBuildOpts_AllFields(t *testing.T) {
	opts := buildOpts(&bootstrapv1.Server_Mcp{
		Address: "127.0.0.1:0",
		Name:    "contract-mcp",
		Version: "v9.9.9",
	})
	if len(opts) != 4 {
		t.Fatalf("len(opts) = %d, want 4 (address + type + name + version)", len(opts))
	}
}

// TestProvider_ForcedHTTPBindsPort 形态强制 HTTP 的落点判据：Start 后 endpoint 非空。
// STDIO（实现默认）不绑端口、endpoint 恒空——若映射摘掉 WithMCPServeType，
// address 会被实现默认吞成 STDIO，这条腿判红。
func TestProvider_ForcedHTTPBindsPort(t *testing.T) {
	srv, _, err := Provider()(context.Background(), &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:0"},
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	msrv := srv.(*mcp.Server)
	if err := msrv.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = msrv.Stop(context.Background()) })
	if ep := msrv.Endpoint(); !strings.HasPrefix(ep, "http://127.0.0.1:") {
		t.Fatalf("Endpoint() = %q, want http://127.0.0.1:<port>（形态没被强制成 HTTP？）", ep)
	}
}

// --- after-build 钩子 -------------------------------------------------------

func TestProvider_AfterBuildInvokedSameServer(t *testing.T) {
	var hookSrv *mcp.Server
	srv, _, err := Provider(WithAfterBuild(func(_ context.Context, s *mcp.Server) error {
		hookSrv = s
		return nil
	}))(context.Background(), &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:0"},
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
	_, _, err := Provider(WithAfterBuild(func(_ context.Context, _ *mcp.Server) error {
		return boom
	}))(context.Background(), &bootstrapv1.Server{
		Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:0"},
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want wrapped boom", err)
	}
	if !strings.Contains(err.Error(), "mcp: after-build hook") {
		t.Fatalf("err = %v, want attribution", err)
	}
}

// --- 业务 Option 覆盖序 -----------------------------------------------------

// TestProvider_BusinessOptionOverridesServeType 业务可把形态改回 STDIO（自担语义：
// 那时 address 不再决定落点）。判据取「STDIO 不产生 endpoint」这一可实现差异。
func TestProvider_BusinessOptionOverridesServeType(t *testing.T) {
	srv, _, err := Provider(WithServerOptions(mcp.WithMCPServeType(mcp.ServerTypeStdio)))(
		context.Background(), &bootstrapv1.Server{
			Mcp: &bootstrapv1.Server_Mcp{Address: "127.0.0.1:0"},
		})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	msrv := srv.(*mcp.Server)
	if err := msrv.Start(context.Background()); err != nil {
		t.Fatalf("Start (stdio): %v", err)
	}
	t.Cleanup(func() { _ = msrv.Stop(context.Background()) })
	if ep := msrv.Endpoint(); ep != "" {
		t.Fatalf("Endpoint() = %q, want empty (业务 Option 没盖过契约形态)", ep)
	}
}
