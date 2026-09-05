package etcd

import (
	"context"
	"testing"
	"time"
)

// TestNew_ParameterValidation 自建模式参数校验。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("New() without endpoints should fail")
	}
	if _, err := New(WithEndpoints("127.0.0.1:2379")); err == nil {
		t.Error("New() without path should fail")
	}
}

// TestNewWithClient_ParameterValidation 注入模式参数校验。
func TestNewWithClient_ParameterValidation(t *testing.T) {
	if _, err := NewWithClient(nil, WithPath("cfg")); err == nil {
		t.Error("NewWithClient(nil) should fail")
	}
}

// TestResolveKey key 覆盖默认 path。
func TestResolveKey(t *testing.T) {
	c := &Config{opts: options{path: "default/path"}}
	if got := c.resolveKey(""); got != "default/path" {
		t.Errorf("resolveKey(\"\") = %q", got)
	}
	if got := c.resolveKey("explicit"); got != "explicit" {
		t.Errorf("resolveKey(explicit) = %q", got)
	}
}

// TestDialTimeoutOption 超时选项生效且拒绝非正值。
func TestDialTimeoutOption(t *testing.T) {
	src, err := New(WithEndpoints("127.0.0.1:2379"), WithPath("cfg"), WithDialTimeout(3*time.Second))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if src.opts.dialTimeout != 3*time.Second {
		t.Errorf("dialTimeout = %v", src.opts.dialTimeout)
	}
	src2, _ := New(WithEndpoints("127.0.0.1:2379"), WithPath("cfg"), WithDialTimeout(0))
	if src2.opts.dialTimeout != DefaultDialTimeout {
		t.Errorf("zero dialTimeout should keep default, got %v", src2.opts.dialTimeout)
	}
}

// TestLoad_Unreachable 连接不可达时 Load 阻塞直至 ctx 取消
// （clientv3.New 非阻塞，连接由 gRPC 后台重试；调用方以 ctx 控制超时）。
func TestLoad_Unreachable(t *testing.T) {
	src, err := New(
		WithEndpoints("127.0.0.1:1"), // 不会有人监听
		WithPath("cfg"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer src.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := src.Load(ctx, ""); err == nil {
		t.Fatal("Load on unreachable etcd should fail (ctx deadline)")
	}
}
