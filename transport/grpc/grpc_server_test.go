package grpcserver

import (
	"context"
	"errors"
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
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil, nil)
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

// TestGRPCServer_HealthRegistered：健康检查服务默认注册且初始 SERVING。
func TestGRPCServer_HealthRegistered(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil, nil)
	stop := startAndWait(t, srv)
	defer stop()

	status, err := healthCheck(t, srv.Endpoint())
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	if status != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v, want SERVING", status)
	}
}

// TestGRPCServer_ReadinessDrivesHealth：readiness 状态变化应驱动 health 联动。
func TestGRPCServer_ReadinessDrivesHealth(t *testing.T) {
	serving := false
	readiness := func(context.Context) error {
		if !serving {
			return errors.New("dep not ready")
		}
		return nil
	}
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil, readiness,
		WithReadinessPollInterval(10*time.Millisecond))
	stop := startAndWait(t, srv)
	defer stop()

	// 未就绪 → NOT_SERVING
	deadline := time.Now().Add(2 * time.Second)
	for {
		status, err := healthCheck(t, srv.Endpoint())
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		if status == healthpb.HealthCheckResponse_NOT_SERVING {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health should be NOT_SERVING before readiness, got %v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 恢复就绪 → SERVING
	serving = true
	deadline = time.Now().Add(2 * time.Second)
	for {
		status, err := healthCheck(t, srv.Endpoint())
		if err != nil {
			t.Fatalf("health check: %v", err)
		}
		if status == healthpb.HealthCheckResponse_SERVING {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("health should recover to SERVING, got %v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestGRPCServer_StartErrorLeavesNoProbe：Start 失败（端口占用）后
// 不应启动 readiness 轮询 goroutine（readinessCancel 保持 nil）。
func TestGRPCServer_StartErrorLeavesNoProbe(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: "127.0.0.1:1"}, nil, nil,
		func(context.Context) error { return nil })
	if err := srv.Start(context.Background()); err == nil {
		t.Fatal("Start on bad addr should fail")
	}
	srv.mu.RLock()
	cancel := srv.readinessCancel
	srv.mu.RUnlock()
	if cancel != nil {
		t.Fatal("readiness probe should not start when listen fails")
	}
}

// TestGRPCServer_StopConcurrentWithStart：Stop 与 Start 并发不应 panic/race。
func TestGRPCServer_StopConcurrentWithStart(t *testing.T) {
	srv := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil, nil)
	go func() { _ = srv.Start(context.Background()) }()
	time.Sleep(20 * time.Millisecond)
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := srv.Stop(stopCtx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
