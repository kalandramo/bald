package consul

import (
	"context"
	"testing"
)

// TestNew_ParameterValidation path 必填。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("New() without path should fail")
	}
	if _, err := NewWithClient(nil, WithPath("cfg")); err == nil {
		t.Error("NewWithClient(nil) should fail")
	}
}

// TestNew_DefaultClient 自建模式构造成功（api.NewClient 懒连接，不发请求）。
func TestNew_DefaultClient(t *testing.T) {
	src, err := New(WithPath("config/app.yaml"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if src.client == nil {
		t.Fatal("client should be built eagerly in New")
	}
	if src.opts.path != "config/app.yaml" {
		t.Errorf("path = %q", src.opts.path)
	}
}

// TestNew_OptionOverrides 地址/令牌/协议覆盖默认值。
func TestNew_OptionOverrides(t *testing.T) {
	src, err := New(
		WithPath("cfg"),
		WithAddress("consul.internal:8500"),
		WithToken("tok"),
		WithScheme("https"),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if src.opts.address != "consul.internal:8500" || src.opts.token != "tok" || src.opts.scheme != "https" {
		t.Errorf("opts = %+v", src.opts)
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

// TestWatchValue_InvalidPlan 无效 watch plan 报错。
// （key 计划合法，此处验证通道路径：连不上 consul 时 Load 报错。）
func TestLoad_Unreachable(t *testing.T) {
	src, err := New(WithPath("cfg"), WithAddress("127.0.0.1:1"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := src.Load(context.Background(), ""); err == nil {
		t.Fatal("Load on unreachable consul should fail")
	}
}
