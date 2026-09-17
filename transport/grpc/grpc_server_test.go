package grpcserver

import (
	"context"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// startAndWait 启动 server 并等待 Endpoint 就绪（:0 场景），返回 stop 函数。
func startAndWait(t *testing.T, srv interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Endpoint() string
}) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	deadline := time.After(2 * time.Second)
	for {
		if ep := srv.Endpoint(); ep != "" && !strings.HasSuffix(ep, ":0") {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("server Endpoint not ready in time")
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = srv.Stop(stopCtx)
		cancel()
		<-done
	}
}

// healthCheck 对运行中 server 的整体健康状态（"" 服务）发起 Check。
func healthCheck(t *testing.T, ep string) (healthpb.HealthCheckResponse_ServingStatus, error) {
	t.Helper()
	conn, err := grpc.NewClient(strings.TrimPrefix(ep, "grpc://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		return 0, err
	}
	return resp.GetStatus(), nil
}

// TestGRPCServer_DynamicPort：绑定 ":0" 时 Endpoint 应解析为真实随机端口且可达。
func TestGRPCServer_DynamicPort(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil)
	stop := startAndWait(t, srv)
	defer stop()

	ep := srv.Endpoint()
	if strings.HasSuffix(ep, ":0") {
		t.Fatalf("Endpoint still :0, dynamic port not resolved: %q", ep)
	}
	if !strings.HasPrefix(ep, "grpc://") {
		t.Fatalf("scheme = %q, want grpc://", ep)
	}
	if _, err := healthCheck(t, ep); err != nil {
		t.Fatalf("endpoint should be reachable: %v", err)
	}
}

// TestGRPCServer_HealthRegistered：gRPC 标准健康服务默认注册且初始 SERVING
// （协议标配在位；就绪判断由装配层推，见《Bald 健康检查装配设计》）。
func TestGRPCServer_HealthRegistered(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil)
	stop := startAndWait(t, srv)
	defer stop()

	status, err := healthCheck(t, srv.Endpoint())
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", status)
	}
	if srv.HealthServer() == nil {
		t.Fatal("HealthServer() must expose the registered health server")
	}
}

// TestGRPCServer_HealthServerDrivesStatus：外部经 HealthServer() 推状态应生效
// （这正是装配层同步就绪状态的路径）。
func TestGRPCServer_HealthServerDrivesStatus(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil)
	stop := startAndWait(t, srv)
	defer stop()

	srv.HealthServer().SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	status, err := healthCheck(t, srv.Endpoint())
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if status != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING", status)
	}
}

// TestGRPCServer_NoReflection：协议实现不再按契约注册 reflection
// （外移到装配层，见设计文档）——默认 Server 上不应有 reflection 服务。
func TestGRPCServer_NoReflection(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0", Reflection: true}, nil, nil)
	for name := range srv.GetServiceInfo() {
		if strings.Contains(name, "reflection") {
			t.Fatalf("reflection service %q should not be registered by transport", name)
		}
	}
}

// TestGRPCServer_StopConcurrentWithStart：Stop 与 Start 并发不应 panic/race。
func TestGRPCServer_StopConcurrentWithStart(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil)
	go func() { _ = srv.Start(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
