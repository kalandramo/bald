package appkit

// server_registry_test.go —— WithServerRegistry：server 域注入口。
//
// 背景（G-1）：其余 12 个资源域都有 With*Registry 注入口，唯独 server 没有——
// 框架把 ServerRegistry 当函数内局部变量、硬编码只注册 http/grpc。第三方
// 想加协议服务器只能走 WithExtraServers 逃生舱，而逃生舱收已构造实例，逼使
// 装配滑回构造期（时序倒置的机制来源）。
//
// 本文件锁三条不变量：
//  1. 注入的自定义 provider 在 **Run 期**被 BuildServers 调用（构造期零调用）；
//  2. 注入 Registry 后 http/grpc 内置 provider 仍被补注册（不因注入而丢失）；
//  3. 仅注入 Registry（不声明 http/grpc）也能构造服务器面（门禁含 serverRegistry）。
//
// 用真实 BuildServers 链路（不 mock 中间层）：契约段 → provider → server 启动。

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldbootstrap "github.com/kalandramo/bald/bootstrap"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/transport"
)

// TestFromBootstrap_ServerRegistryCustomProviderWired 注入带自定义 provider 的
// ServerRegistry：provider 在 Run 期被调用，其 server 进入启动序列。
func TestFromBootstrap_ServerRegistryCustomProviderWired(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	// 契约里配一个 implemented=false 的段（asynq）——框架不内置，靠注入的 provider。
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	// 同时配 http 主面，验证内置 provider 补注册不被注入破坏。
	cfg.Server.Http = &bootstrapv1.Server_Http{Addr: ":0"}
	cfg.Server.Grpc = nil

	var asynqCalled atomic.Int32
	sr := baldbootstrap.NewServerRegistry()
	sr.MustRegister("asynq", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		asynqCalled.Add(1)
		return &probeServer{name: "asynq", started: &atomic.Bool{}, mark: func(string) {}}, nil, nil
	})

	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithServerRegistry(sr),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	// 构造期：provider 不应被调用（BuildServers 延后到 Run 期）。
	if n := asynqCalled.Load(); n != 0 {
		t.Fatalf("custom provider called %d times during FromBootstrap, want 0 (deferred to Run)", n)
	}

	runBriefly(t, a, 50*time.Millisecond)

	if n := asynqCalled.Load(); n != 1 {
		t.Fatalf("custom provider called %d times at Run, want 1", n)
	}
}

// TestFromBootstrap_ServerRegistryOnlyNoMainProtocol 只注入 Registry（不声明
// http/grpc）也能构造服务器面——门禁条件须包含 spec.serverRegistry != nil。
func TestFromBootstrap_ServerRegistryOnlyNoMainProtocol(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	cfg.Server.Asynq = &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	cfg.Server.Http = nil
	cfg.Server.Grpc = nil

	started := &atomic.Bool{}
	sr := baldbootstrap.NewServerRegistry()
	sr.MustRegister("asynq", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return &probeServer{name: "asynq", started: started, mark: func(string) {}}, nil, nil
	})

	a, err := FromBootstrap(cfg, WithServerRegistry(sr))
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}
	if a == nil {
		t.Fatal("a = nil")
	}

	runBriefly(t, a, 50*time.Millisecond)

	if !started.Load() {
		t.Fatal("仅注入 Registry（无 http/grpc 主面）时自定义 server 未被启动")
	}
	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil", a.Err())
	}
}

// TestFromBootstrap_BuiltinProvidersBackfilled 注入 Registry 后，框架仍补注册
// http/grpc 内置 provider——声明了主协议面就必须能用。
func TestFromBootstrap_BuiltinProvidersBackfilled(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	if cfg.GetServer().GetHttp() == nil {
		t.Skip("contract has no http section")
	}

	// 空的自定义 Registry——框架应补注册 http/grpc。
	sr := baldbootstrap.NewServerRegistry()

	a, err := FromBootstrap(cfg,
		WithHTTP(new(http.ServeMux)),
		WithServerRegistry(sr),
	)
	if err != nil {
		t.Fatalf("FromBootstrap: %v", err)
	}

	runBriefly(t, a, 50*time.Millisecond)

	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil（内置 http provider 应被补注册）", a.Err())
	}
}
