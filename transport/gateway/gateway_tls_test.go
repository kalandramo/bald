package gateway

// gateway 出站 TLS 用例：被代理的 gRPC 段声明了 tls，gateway 必须用 TLS 拨它。
//
// 这一项与被代理端读同一段配置（Server_Grpc.tls）：服务端开 TLS 后 gateway
// 若仍硬编码明文，转码面会立刻连不上后端——所以两侧必须同源。

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"
	"github.com/kalandramo/bald/bconf/tlsconf/tlstest"
)

// serveBackend 起一个 gRPC 后端（标准健康服务），plain=true 为明文、false 为
// TLS（契约服务端段同源材料 + h2 ALPN，grpc-go 强制校验）。
func serveBackend(t *testing.T, plain bool, c *tlstest.Certs) string {
	t.Helper()
	var opts []grpc.ServerOption
	if !plain {
		srvCfg, err := tlsconf.Server(c.ServerTLS())
		if err != nil {
			t.Fatalf("server tls: %v", err)
		}
		srvCfg.NextProtos = []string{"h2"}
		opts = append(opts, grpc.Creds(credentials.NewTLS(srvCfg)))
	}
	srv := grpc.NewServer(opts...)
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		_ = srv.Serve(ln)
	}()
	t.Cleanup(srv.Stop)
	return ln.Addr().String()
}

// assertHealth 用给定拨号选项连后端并调用健康服务，返回 RPC 错误。
func assertHealth(t *testing.T, addr string, opts ...grpc.DialOption) error {
	t.Helper()
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

func clientOpts(t *testing.T, c *tlstest.Certs) []grpc.DialOption {
	t.Helper()
	opts, err := dialOpts(c.ClientTLS())
	if err != nil {
		t.Fatalf("dialOpts: %v", err)
	}
	return opts
}

// TestDialOptsMatchesBackend 断言凭据选择与后端一致：明文段拨明文后端、TLS 段
// 拨 TLS 后端都通；反过来（TLS 段拨明文后端）必须失败。
func TestDialOptsMatchesBackend(t *testing.T) {
	c := certs(t)

	plainAddr := serveBackend(t, true, nil)
	tlsAddr := serveBackend(t, false, c)

	if err := assertHealth(t, plainAddr, grpc.WithTransportCredentials(insecure.NewCredentials())); err != nil {
		t.Fatalf("明文后端不可达: %v", err)
	}
	if err := assertHealth(t, tlsAddr, clientOpts(t, c)...); err != nil {
		t.Fatalf("TLS 后端经契约凭据不可达: %v", err)
	}
	if err := assertHealth(t, plainAddr, clientOpts(t, c)...); err == nil {
		t.Error("TLS 凭据连上了明文后端")
	}
}

// TestDialOptsNilMeansInsecure 契约无 tls 段 → 明文凭据（保持既有默认行为）。
func TestDialOptsNilMeansInsecure(t *testing.T) {
	opts, err := dialOpts(nil)
	if err != nil {
		t.Fatalf("dialOpts(nil): %v", err)
	}
	addr := serveBackend(t, true, nil)
	if err := assertHealth(t, addr, opts...); err != nil {
		t.Fatalf("nil tls 段应拨通明文后端: %v", err)
	}
}

// TestStartFailsOnBadBackendTLS 后端 tls 段配不出可用凭据时，Start 必须报错并
// 在建立监听器之前失败——不能起一个「转码面在线、后端永不可达」的网关。
func TestStartFailsOnBadBackendTLS(t *testing.T) {
	gw, err := NewGatewayServer(
		&bootstrapv1.Server_Http{Addr: "127.0.0.1:0", Driver: "grpc-gateway"},
		&bootstrapv1.Server_Grpc{Addr: "127.0.0.1:1", Tls: &bootstrapv1.Server_TLS{
			Config: &bootstrapv1.Server_TLS_Config{CertPem: []byte("not a pem"), KeyPem: []byte("not a pem")},
		}},
		nil,
	)
	if err != nil {
		t.Fatalf("NewGatewayServer: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err = gw.Start(ctx)
	if err == nil {
		_ = gw.Stop(context.Background())
		t.Fatal("坏的后端 tls 段应让 Start 失败")
	}
	if !strings.Contains(err.Error(), "gateway backend tls") {
		t.Errorf("err = %v, want 指向后端 tls", err)
	}
	if ep := gw.Endpoint(); ep != "" {
		t.Errorf("启动失败后 Endpoint = %q, want 空（不得报告未监听的地址）", ep)
	}
}

// tlsclient 用 tlsconf 把契约客户端段翻译成 tls.Config（与被测路径同源）。
func tlsclient(c *tlstest.Certs) (*tls.Config, error) {
	return tlsconf.Client(c.ClientTLS())
}

func certs(t *testing.T) *tlstest.Certs {
	t.Helper()
	c, err := tlstest.Generate()
	if err != nil {
		t.Fatalf("tlstest.Generate: %v", err)
	}
	return c
}
