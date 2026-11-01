package gateway

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
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

// startFakeGRPCBackend 起一个带 health 服务的真实 gRPC 后端（用于 gateway 拨号）。
func startFakeGRPCBackend(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, health.NewServer())
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(s.Stop)
	return ln.Addr().String()
}

// TestGatewayServer_StartStop：真实 gRPC 后端 + 空转码注册，验证完整启停不 panic、
// Endpoint scheme 正确、Stop 幂等（appkit 五阶段会调两次）。
func TestGatewayServer_StartStop(t *testing.T) {
	backend := startFakeGRPCBackend(t)

	register := func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	}
	gw, err := NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: ":0"},
		&bootstrapv1.Server_Grpc{Addr: backend},
		register,
	)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	stop := startAndWait(t, gw)

	if ep := gw.Endpoint(); !strings.HasPrefix(ep, "http://") {
		t.Fatalf("scheme = %q, want http://", ep)
	}

	// 协议层不挂探针：转码 handler 里没有的路径一律 404（探针归装配层/业务）。
	addr := "http://" + strings.TrimPrefix(gw.Endpoint(), "http://")
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("/healthz = %d, want 404 (no framework probes in transport)", resp.StatusCode)
	}

	stop()
	// 幂等：二次 Stop 不 panic。
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := gw.Stop(stopCtx); err != nil {
		t.Fatalf("second Stop should be idempotent, got %v", err)
	}
}

// TestGatewayServer_HTTPS：网关 HTTP 面按契约 tls 段起 HTTPS——真实证书材料 +
// 真实请求（信任 CA 的客户端拿到响应，明文请求拿不到业务响应）。
//
// 「Tls 非 nil 就报 https scheme」是假绿：证书配不出来时服务根本没起来，
// scheme 照样是 https。故这里必须打一次真实请求。
func TestGatewayServer_HTTPS(t *testing.T) {
	backend := startFakeGRPCBackend(t)
	c := certs(t)
	register := func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
		return http.NewServeMux(), nil
	}
	gw, err := NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: "127.0.0.1:0", Tls: c.ServerTLS()},
		&bootstrapv1.Server_Grpc{Addr: backend},
		register,
	)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}
	stop := startAndWait(t, gw)
	defer stop()

	ep := gw.Endpoint()
	if !strings.HasPrefix(ep, "https://") {
		t.Fatalf("with TLS, scheme = %q, want https://", ep)
	}

	tlsCfg, err := tlsclient(c)
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: tlsCfg}, Timeout: 3 * time.Second}
	resp, err := client.Get(ep + "/anything")
	if err != nil {
		t.Fatalf("HTTPS 请求失败: %v", err)
	}
	_ = resp.Body.Close()

	plain := &http.Client{Timeout: 3 * time.Second}
	if resp, err := plain.Get("http://" + strings.TrimPrefix(ep, "https://") + "/anything"); err == nil {
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Error("HTTPS 端口把明文请求当正常流量处理")
		}
	}
}
