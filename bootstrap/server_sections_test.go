package bootstrap

// D2 回归测试（2026-09-19）：契约段「配了但本仓无实现」必须 fail-fast。
//
// 背景：BuildServers 按**已注册 provider** 枚举，契约段若没有任何 provider 对应，
// 该段根本不进入遍历——修复前既不报错也不打日志，进程照常启动却不监听该协议
// （「配了没效果」的静默失效）。此测试锁定修复后的行为，并锁定「不误伤能力声明
// 机制」（implemented=true 的段未注册 provider 仍合法跳过）。

import (
	"context"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/transport"
)

// TestBuildServers_ConfiguredUnimplementedSectionFailsFast 配了本仓无实现的段
// （如 server.sse）→ 必须报错，且错误信息给出可操作指引。
func TestBuildServers_ConfiguredUnimplementedSectionFailsFast(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":8080"},
		Sse:  &bootstrapv1.Server_Sse{Path: "/events"}, // 本仓无 sse 服务端装配
	}

	servers, cleanup, err := r.BuildServers(context.Background(), cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil {
		t.Fatalf("BuildServers() = nil error, want fail-fast (servers=%d)", len(servers))
	}
	if !strings.Contains(err.Error(), "server.sse") {
		t.Fatalf("error %q must name the offending section server.sse", err)
	}
	if !strings.Contains(err.Error(), "WithExtraServers") {
		t.Fatalf("error %q must point to the escape hatch (WithExtraServers)", err)
	}
	if servers != nil {
		t.Fatalf("servers = %v, want nil on failure", servers)
	}
}

// TestBuildServers_MultipleUnimplementedSectionsAggregated 多个非法段一次列全。
func TestBuildServers_MultipleUnimplementedSectionsAggregated(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Sse:       &bootstrapv1.Server_Sse{Path: "/events"},
		Asynq:     &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"},
		Websocket: &bootstrapv1.Server_Websocket{Path: "/ws"},
	}

	_, _, err := r.BuildServers(context.Background(), cfg)
	if err == nil {
		t.Fatal("BuildServers() = nil error, want fail-fast")
	}
	for _, want := range []string{"server.sse", "server.asynq", "server.websocket"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q must list %s (aggregate all problems)", err, want)
		}
	}
}

// TestBuildServers_ImplementedSectionWithoutProviderStillSkips 锁定不误伤：
// implemented=true 的段（如 grpc）配了但未注册 provider → 仍合法跳过（能力声明
// 在代码，是刻意的设计，见 pkg/appkit/bootstrap.go 装配注释）。
func TestBuildServers_ImplementedSectionWithoutProviderStillSkips(t *testing.T) {
	r := NewServerRegistry()
	r.MustRegister("http", func(context.Context, *bootstrapv1.Server) (transport.Server, func(), error) {
		return newStubSrvServer("http://x"), nil, nil
	})

	cfg := &bootstrapv1.Server{
		Http: &bootstrapv1.Server_Http{Addr: ":8080"},
		Grpc: &bootstrapv1.Server_Grpc{Addr: ":9090"}, // 有实现但本 registry 未注册 provider
	}

	servers, cleanup, err := r.BuildServers(context.Background(), cfg)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		t.Fatalf("BuildServers() = %v, want nil (unregistered provider is a legitimate "+
			"capability declaration, not an error)", err)
	}
	if len(servers) != 1 {
		t.Fatalf("len(servers) = %d, want 1 (only http; grpc has no provider here)", len(servers))
	}
}
