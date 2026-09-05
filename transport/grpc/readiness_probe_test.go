package grpcserver

import (
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestGRPCServer_WithPollInterval WithReadinessPollInterval 覆盖轮询间隔。
func TestGRPCServer_WithPollInterval(t *testing.T) {
	g := NewGRPCServerWithRegister(
		&bootstrapv1.Server_Grpc{Addr: ":0"},
		nil, nil, nil,
		WithReadinessPollInterval(500*time.Millisecond),
	)
	if g.readinessInterval != 500*time.Millisecond {
		t.Fatalf("readiness interval = %v, want 500ms", g.readinessInterval)
	}
	// 默认值。
	g2 := NewGRPCServerWithRegister(&bootstrapv1.Server_Grpc{Addr: ":0"}, nil, nil, nil)
	if g2.readinessInterval != defaultReadinessPollInterval {
		t.Fatalf("default readiness interval = %v, want %v", g2.readinessInterval, defaultReadinessPollInterval)
	}
}
