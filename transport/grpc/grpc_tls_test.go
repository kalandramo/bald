package grpcserver

// gRPC TLS 用例：证明契约 server.grpc.tls 真的生效。
//
// 这一项原本是「契约声明了 tls，装配与协议层都不读它」——服务在非 loopback 上
// 静默明文。这里断言三件事：TLS 握手成功、明文被拒、证书配不出来时启动即失败。

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/bconf/tlsconf"
	"github.com/kalandramo/bald/bconf/tlsconf/tlstest"
)

// startVisible 启动 server 并等待 Endpoint；与 startAndWait 的差别是 Start 的错误
// 必须能让测试看到——启动失败却报一个已关闭的地址，比启动失败本身更糟。
func startVisible(t *testing.T, srv *GRPCServer) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()

	deadline := time.After(3 * time.Second)
	for srv.Endpoint() == "" || strings.HasSuffix(srv.Endpoint(), ":0") {
		select {
		case err := <-done:
			cancel()
			if err != nil {
				t.Fatalf("Start failed before binding: %v", err)
			}
			t.Fatal("Start returned nil while endpoint is still unresolved")
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

// healthRPC 用给定凭据拨号并调用标准健康服务，返回 RPC 错误。
func healthRPC(t *testing.T, ep string, creds grpc.DialOption) error {
	t.Helper()
	conn, err := grpc.NewClient(strings.TrimPrefix(ep, "grpc://"), creds)
	if err != nil {
		return err
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

// TestGRPCServer_TLSRoundTrip 断言契约 tls 段生效后：持 CA 与客户端证书的连接
// 得到 SERVING，无客户端证书与明文连接都被拒。
func TestGRPCServer_TLSRoundTrip(t *testing.T) {
	c := certs(t)
	srv := NewGRPCServerWithRegister(
		&bootstrapv1.Server_Grpc{Addr: "127.0.0.1:0", Tls: c.ServerTLS()}, nil, nil)
	stop := startVisible(t, srv)
	defer stop()

	ep := srv.Endpoint()

	t.Run("mtls client", func(t *testing.T) {
		tlsCfg, err := clientTLS(c)
		if err != nil {
			t.Fatalf("client tls: %v", err)
		}
		if err := healthRPC(t, ep, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))); err != nil {
			t.Fatalf("健康服务经 TLS 不可达: %v", err)
		}
	})

	t.Run("no client cert", func(t *testing.T) {
		// 服务端配了 ca → 双向校验；只信任 CA、不给客户端证书必须被拒。
		tlsCfg, err := tlsconf.Client(&bootstrapv1.Server_TLS{
			Config: &bootstrapv1.Server_TLS_Config{CaPem: c.CA},
		})
		if err != nil {
			t.Fatalf("client tls: %v", err)
		}
		if err := healthRPC(t, ep, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))); err == nil {
			t.Error("gRPC 服务端接受了无客户端证书的连接")
		}
	})

	t.Run("plaintext client", func(t *testing.T) {
		if err := healthRPC(t, ep, grpc.WithTransportCredentials(insecure.NewCredentials())); err == nil {
			t.Error("TLS 端口接受了明文 gRPC 连接")
		}
	})
}

// TestGRPCServer_UntrustedClientRejected 断言 ca 池不是装饰：另一套 CA 的服务端
// 证书链不被信任时握手失败。
func TestGRPCServer_UntrustedClientRejected(t *testing.T) {
	c := certs(t)
	srv := NewGRPCServerWithRegister(
		&bootstrapv1.Server_Grpc{Addr: "127.0.0.1:0", Tls: c.ServerTLS()}, nil, nil)
	stop := startVisible(t, srv)
	defer stop()

	tlsCfg, err := clientTLS(certs(t)) // 客户端信任的是另一套 CA
	if err != nil {
		t.Fatalf("client tls: %v", err)
	}
	if err := healthRPC(t, srv.Endpoint(), grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))); err == nil {
		t.Fatal("客户端连上了不受信任的服务端证书")
	}
}

// TestGRPCServer_TLSIncompleteContractFailsFast 契约写了 tls 段却配不出证书：
// Start 报错且不留下监听器/地址（否则会注册出幽灵节点）。
func TestGRPCServer_TLSIncompleteContractFailsFast(t *testing.T) {
	srv := NewGRPCServerWithRegister(
		&bootstrapv1.Server_Grpc{Addr: "127.0.0.1:0", Tls: &bootstrapv1.Server_TLS{}}, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := srv.Start(ctx)
	if err == nil {
		t.Fatal("空 tls 段应启动失败，却返回 nil")
	}
	if !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want 指出缺证书", err)
	}
	if ep := srv.Endpoint(); ep != "" {
		t.Errorf("启动失败后 Endpoint = %q, want 空", ep)
	}
}

func certs(t *testing.T) *tlstest.Certs {
	t.Helper()
	c, err := tlstest.Generate()
	if err != nil {
		t.Fatalf("tlstest.Generate: %v", err)
	}
	return c
}

func clientTLS(c *tlstest.Certs) (*tls.Config, error) {
	return tlsconf.Client(c.ClientTLS())
}
