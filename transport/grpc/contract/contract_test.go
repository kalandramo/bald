package contract

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/health"

	"github.com/kalandramo/bald/transport"
)

// TestProvider_Unconfigured 契约无 Grpc 段 → nil server。
func TestProvider_Unconfigured(t *testing.T) {
	srv, closer, err := Provider()(context.Background(), &bootstrapv1.Server{})
	if err != nil || srv != nil || closer != nil {
		t.Fatalf("unconfigured = (srv=%v, closer!=nil: %t, err=%v), want all nil", srv, closer != nil, err)
	}
}

// TestProvider_ReflectionFlag 契约 reflection 由契约装配层消费：
// false 不注册、true 注册（transport 已不看这个字段）。
func TestProvider_ReflectionFlag(t *testing.T) {
	for _, want := range []bool{false, true} {
		cfg := &bootstrapv1.Server{
			Grpc: &bootstrapv1.Server_Grpc{Addr: ":0", Reflection: want},
		}
		srv, closer, err := Provider(WithRegister(func(s *grpc.Server) {}))(context.Background(), cfg)
		if err != nil || srv == nil {
			t.Fatalf("provider(reflection=%t) = (%v, %v), want non-nil", want, srv, err)
		}
		if closer != nil {
			closer()
		}
		// 有无 health 包装都满足该接口（healthServer 内嵌 *grpcserver.GRPCServer）。
		infoer, ok := srv.(interface {
			GetServiceInfo() map[string]grpc.ServiceInfo
		})
		if !ok {
			t.Fatalf("server %T does not expose GetServiceInfo", srv)
		}
		got := false
		for name := range infoer.GetServiceInfo() {
			if strings.Contains(name, "reflection") {
				got = true
			}
		}
		if got != want {
			t.Fatalf("reflection registered = %t, want %t", got, want)
		}
	}
}

// waitEndpoint 等待 server 解析出真实监听地址（:0 动态端口）。
func waitEndpoint(t *testing.T, srv transport.Server) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for srv.Endpoint() == "" || strings.HasSuffix(srv.Endpoint(), ":0") {
		select {
		case <-deadline:
			t.Fatal("endpoint not ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// grpcHealthStatus 查询 gRPC 标准健康服务的整体状态（"" 服务）。
func grpcHealthStatus(t *testing.T, ep string) healthpb.HealthCheckResponse_ServingStatus {
	t.Helper()
	conn, err := grpc.NewClient(strings.TrimPrefix(ep, "grpc://"),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("grpc client: %v", err)
	}
	defer conn.Close()
	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check: %v", err)
	}
	return resp.GetStatus()
}

// TestProvider_HealthSync 契约装配层推送：health.Health 的聚合结果经后台
// 轮询同步到 gRPC 标准健康服务（NOT_SERVING ⇄ SERVING）。
func TestProvider_HealthSync(t *testing.T) {
	var ready atomic.Bool
	h := health.New()
	h.Register("dep", health.PingFunc(func(context.Context) error {
		if !ready.Load() {
			return errors.New("dep not ready")
		}
		return nil
	}))

	cfg := &bootstrapv1.Server{Grpc: &bootstrapv1.Server_Grpc{Addr: ":0"}}
	srv, closer, err := Provider(WithHealth(h, 10*time.Millisecond))(context.Background(), cfg)
	if err != nil || srv == nil {
		t.Fatalf("provider = (%v, %v), want non-nil", srv, err)
	}
	if closer != nil {
		defer closer()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = srv.Stop(stopCtx)
		stopCancel()
		cancel()
		<-done
	}()

	waitEndpoint(t, srv)
	if got := grpcHealthStatus(t, srv.Endpoint()); got != healthpb.HealthCheckResponse_NOT_SERVING {
		t.Fatalf("status = %v, want NOT_SERVING (dependency down)", got)
	}

	ready.Store(true)
	deadline := time.Now().Add(2 * time.Second)
	for grpcHealthStatus(t, srv.Endpoint()) != healthpb.HealthCheckResponse_SERVING {
		if time.Now().After(deadline) {
			t.Fatal("status should recover to SERVING")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
