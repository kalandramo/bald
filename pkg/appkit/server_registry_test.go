// server_registry_test.go —— server 段 Provider（用户经段驱动 Provider 接入
// 第三方协议服务器，co-provider 与内置 http/grpc 并存）。
//
// 本文件锁三条不变量：
//  1. co-provider 的 Build 在 **Run 期**（迟后阶段）被调用（构造期零调用）；
//  2. co-provider 与内置 std server Provider 并存（http 主面不因
//     co-provider 而丢失）；
//  3. 仅声明 co-provider（不声明 http/grpc 能力）也能构造服务器面。
//
// 用真实构建链路（不 mock 中间层）：契约段 → Provider → server 启动。
package appkit

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/transport"
)

// asynqCoProvider 构造消费 server.asynq 段的 co-provider（第三方协议
// 后端的 Provider 形态：自建 server、产物 []transport.Server 交引擎入列）。
func asynqCoProvider(called, started *atomic.Bool) segProvider {
	return segProvider{segs: []string{"server.asynq"}, build: func(context.Context, BuildScope) (any, Cleanup, error) {
		called.Store(true)
		return []transport.Server{&probeServer{name: "asynq", started: started, mark: func(string) {}}}, nil, nil
	}}
}

// TestAssemble_ServerCoProviderDeferred co-provider 在 Run 期被调用，
// 其 server 进入启动序列（原 TestAssemble_ServerRegistryCustomProviderWired）。
func TestAssemble_ServerCoProviderDeferred(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	// 契约里配一个非内置子段（asynq）——框架不内置，靠 co-provider。
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	// 同时配 http 主面，验证内置 server Provider 不因 co-provider 而丢失。
	cfg.Server.Http = &bootstrapv1.Server_Http{Addr: ":0"}
	cfg.Server.Grpc = nil

	var called, started atomic.Bool
	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		StandardServerProvider(), asynqCoProvider(&called, &started))
	// 构造期：co-provider 不应被调用（服务器构造延后到 Run 期迟后阶段）。
	if called.Load() {
		t.Fatal("co-provider called during construction, want deferred to Run")
	}

	runBriefly(t, a, 50*time.Millisecond)

	if !called.Load() {
		t.Fatal("co-provider not called at Run")
	}
	if len(a.servers) != 2 {
		t.Fatalf("servers = %d, want 2 (http builtin + asynq co-provider)", len(a.servers))
	}
}

// TestAssemble_ServerCoProviderOnlyNoMainProtocol 只声明 co-provider
// （不声明 http/grpc 能力）也能构造服务器面（原
// TestAssemble_ServerRegistryOnlyNoMainProtocol）。
func TestAssemble_ServerCoProviderOnlyNoMainProtocol(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	cfg.Server.Http = nil
	cfg.Server.Grpc = nil

	var called, started atomic.Bool
	a := assembleForTest(t, cfg,
		asynqCoProvider(&called, &started))
	if a == nil {
		t.Fatal("a = nil")
	}

	runBriefly(t, a, 50*time.Millisecond)

	if !started.Load() {
		t.Fatal("仅声明 co-provider（无 http/grpc 主面）时自定义 server 未被启动")
	}
	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil", a.Err())
	}
}

// TestAssemble_BuiltinStdServerProvider 内置 std server Provider 按能力
// 声明构建 http 主面（原 TestAssemble_BuiltinProvidersBackfilled：
// 「框架补注册 http/grpc」语义在 Provider 形态下即 std 的构建行为）。
func TestAssemble_BuiltinStdServerProvider(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	if cfg.GetServer().GetHttp() == nil {
		t.Skip("contract has no http section")
	}

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		StandardServerProvider())

	runBriefly(t, a, 50*time.Millisecond)

	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil（内置 http provider 应按能力声明构建）", a.Err())
	}
	if len(a.servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(a.servers))
	}
}
