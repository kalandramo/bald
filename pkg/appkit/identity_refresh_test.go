package appkit

import (
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"google.golang.org/protobuf/types/known/durationpb"
)

// TestRefreshIdentityFromContract 钉住 AR-12 的刷新语义：最终契约的非空
// id/version/stop_timeout 覆盖预装载冻结值；空值不覆盖（保留显式 Option）。
// 冻结场景：预装载契约没有 env/flag/remote 层——注册中心的实例 version、
// 停机预算忽略远程层与 flag 的值（J9）。
func TestRefreshIdentityFromContract(t *testing.T) {
	a := &App{id: "old-id", version: "v0.0.0", stopTimeout: 30 * time.Second}

	cfg := &bootstrapv1.BootstrapConfig{App: &bootstrapv1.App{
		Id:          "final-id",
		Version:     "v9.9.9",
		StopTimeout: durationpb.New(2 * time.Second),
	}}
	a.refreshIdentityFromContract(cfg)

	if a.id != "final-id" {
		t.Fatalf("id = %q, want final-id", a.id)
	}
	if a.version != "v9.9.9" {
		t.Fatalf("version = %q, want v9.9.9", a.version)
	}
	if a.stopTimeout != 2*time.Second {
		t.Fatalf("stopTimeout = %v, want 2s", a.stopTimeout)
	}

	// 空值不覆盖：契约段缺失时保留现值（显式 Option 语义）。
	empty := &bootstrapv1.BootstrapConfig{App: &bootstrapv1.App{}}
	a.refreshIdentityFromContract(empty)
	if a.id != "final-id" || a.version != "v9.9.9" || a.stopTimeout != 2*time.Second {
		t.Fatalf("空契约不得覆盖现值: id=%q version=%q stopTimeout=%v", a.id, a.version, a.stopTimeout)
	}
}
