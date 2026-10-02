package bootstrap

// Wave 1 回归测试（2026-10-03）：server 域注入口——外部注册 provider 使
// implemented=false 的契约段合法。
//
// 背景：`serverSections` 把 cron/asynq 等标记为 implemented=false（框架不内置
// 其服务端装配），配了即 fail-fast。但[后端子模块提供实现]是合法路径——
// transport/asynq/contract 注册 "asynq" provider 后，`server.asynq` 段就应当
// 被承认。本条通道由 validateServerSections 的 registered 参数承载。
//
// 反面：**未**注册 provider 的 implemented=false 段仍必须 fail-fast（不能因
// 引入 registered 通道而放松既有的静默失效防护）。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
)

// TestServerRegistry_Has 存在性判断语义。
func TestServerRegistry_Has(t *testing.T) {
	r := NewServerRegistry()
	if r.Has("http") {
		t.Fatal("Has(http) = true on empty registry, want false")
	}
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return nil, nil, nil
	})
	if !r.Has("http") {
		t.Fatal("Has(http) = false after MustRegister, want true")
	}
	if r.Has("grpc") {
		t.Fatal("Has(grpc) = true, want false (never registered)")
	}
}

// TestBuildServers_RegisteredProviderAdmitsUnimplementedSection 注册了 provider
// 的 implemented=false 段（asynq）→ 合法，不 fail-fast，且 provider 被调用。
func TestBuildServers_RegisteredProviderAdmitsUnimplementedSection(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})
	asynqCalled := false
	r.MustRegister("asynq", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		asynqCalled = true
		return newStubSrvServer("asynq://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Http:  &bootstrapv1.Server_Http{Addr: ":8080"},
		Asynq: &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"},
	}

	servers, cleanup, err := r.BuildServers(context.Background(), cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("BuildServers() = %v, want nil (asynq provider registered → section admitted)", err)
	}
	if !asynqCalled {
		t.Fatal("asynq provider was not invoked despite being registered")
	}
	if len(servers) != 2 {
		t.Fatalf("len(servers) = %d, want 2 (http + asynq)", len(servers))
	}
}

// TestBuildServers_UnregisteredUnimplementedSectionStillFailsFast 锁定反面：
// implemented=false 且**无**注册 provider 的段仍 fail-fast——registered 通道
// 不得放松既有的静默失效防护。
func TestBuildServers_UnregisteredUnimplementedSectionStillFailsFast(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Http:  &bootstrapv1.Server_Http{Addr: ":8080"},
		Asynq: &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}, // 无 asynq provider
	}

	_, _, err := r.BuildServers(context.Background(), cfg)
	if err == nil {
		t.Fatal("BuildServers() = nil error, want fail-fast (asynq has no registered provider)")
	}
	if !strings.Contains(err.Error(), "server.asynq") {
		t.Fatalf("error %q must name the offending section server.asynq", err)
	}
}

// TestBuildServers_CronSectionAdmittedAfterProviderRegistration 计划验收项：
// 外部注册 cron 段后不再 fail-fast。
func TestBuildServers_CronSectionAdmittedAfterProviderRegistration(t *testing.T) {	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})
	r.MustRegister("cron", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("cron://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":8080"},
		Cron: &bootstrapv1.Server_Cron{}, // 段存在即可（字段值为三态 optional，与本测试无关）
	}

	servers, cleanup, err := r.BuildServers(context.Background(), cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("BuildServers() = %v, want nil (cron provider registered)", err)
	}
	if len(servers) != 2 {
		t.Fatalf("len(servers) = %d, want 2 (http + cron)", len(servers))
	}
}

// TestBuildServers_GatewaySectionFailFastWithoutProvider gateway 段（Wave 2 新增）
// 配了但无 provider → fail-fast。锁定：新段必须进 serverSections，否则配了既不
// 校验也不装配（静默失效——正是 D2 防护要拦的情形）。
func TestBuildServers_GatewaySectionFailFastWithoutProvider(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Http:    &bootstrapv1.Server_Http{Addr: ":8080"},
		Gateway: &bootstrapv1.Server_Gateway{Addr: ":8081"},
	}

	_, _, err := r.BuildServers(context.Background(), cfg)
	if err == nil {
		t.Fatal("BuildServers() = nil error, want fail-fast (gateway has no registered provider)")
	}
	if !strings.Contains(err.Error(), "server.gateway") {
		t.Fatalf("error %q must name the offending section server.gateway", err)
	}
}

// TestBuildServers_GatewaySectionAdmittedAfterProviderRegistration 注册 gateway
// provider 后该段放行——这是 bald-admin「独立转码面」走契约装配（而非逃生舱）的前提。
func TestBuildServers_GatewaySectionAdmittedAfterProviderRegistration(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("gateway", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("gateway://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Gateway: &bootstrapv1.Server_Gateway{Addr: ":8081"},
	}

	servers, cleanup, err := r.BuildServers(context.Background(), cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("BuildServers() = %v, want nil (gateway provider registered)", err)
	}
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want 1 (gateway)", len(servers))
	}
}
