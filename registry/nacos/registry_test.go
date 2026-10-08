package nacos

import (
	"sync/atomic"
	"testing"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/naming_client"
)

// fakeNamingClient 通过内嵌接口满足 INamingClient，仅覆写 CloseClient 计数。
// 其余方法若被调用会因内嵌 nil 而 panic——正好保证本测试只触发 Close 路径。
type fakeNamingClient struct {
	naming_client.INamingClient
	closeCalls atomic.Int32
}

func (f *fakeNamingClient) CloseClient() { f.closeCalls.Add(1) }

// TestClose_OwnedClientIsClosed 自建模式（New / newRegistry(owned=true)）：
// Close 必须关闭 naming client——否则 gRPC 长连接与心跳/订阅协程在停机时泄漏。
//
// 回归背景：此前 Close 是纯 no-op，注释称「SDK 未暴露 client 级 Close」，
// 但 nacos-sdk-go v2 的 INamingClient 暴露 CloseClient()——注释与事实相反。
func TestClose_OwnedClientIsClosed(t *testing.T) {
	cli := &fakeNamingClient{}
	r := newRegistry(cli, newOptions(), true)

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if got := cli.closeCalls.Load(); got != 1 {
		t.Fatalf("owned client CloseClient calls = %d, want 1", got)
	}
}

// TestClose_InjectedClientIsNotClosed 注入模式（NewWithClient）：client 归调用方，
// Registry 不得关闭它（对齐 registry/etcd 的 owned 语义）。
func TestClose_InjectedClientIsNotClosed(t *testing.T) {
	cli := &fakeNamingClient{}
	r := newRegistry(cli, newOptions(), false)

	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}
	if got := cli.closeCalls.Load(); got != 0 {
		t.Fatalf("injected client must NOT be closed, got %d calls", got)
	}
}

// TestClose_Idempotent Close 幂等：重复调用只关一次（停机 Effect 与测试 cleanup
// 可能重复触发）。
func TestClose_Idempotent(t *testing.T) {
	cli := &fakeNamingClient{}
	r := newRegistry(cli, newOptions(), true)

	_ = r.Close()
	_ = r.Close()
	_ = r.Close()

	if got := cli.closeCalls.Load(); got != 1 {
		t.Fatalf("CloseClient calls = %d, want exactly 1 (idempotent)", got)
	}
}

// TestNewWithClient_PropagatesInjectedNotOwned 经公开构造 NewWithClient 建立的实例，
// Close 同样不得关闭注入 client。
func TestNewWithClient_PropagatesInjectedNotOwned(t *testing.T) {
	cli := &fakeNamingClient{}
	r, err := NewWithClient(cli)
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if got := cli.closeCalls.Load(); got != 0 {
		t.Fatalf("NewWithClient client must not be closed, got %d calls", got)
	}
}
