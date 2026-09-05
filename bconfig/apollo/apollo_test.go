package apollo

import (
	"testing"
)

// TestNew_ParameterValidation 自建模式参数校验（不触网即拒绝）。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("New() without appid should fail")
	}
	if _, err := New(WithAppID("demo")); err == nil {
		t.Error("New() without endpoint should fail")
	}
	if _, err := New(WithAppID("demo"), WithEndpoint("http://apollo:8080")); err == nil {
		t.Error("New() without namespace should fail")
	}
	if _, err := NewWithClient(nil, WithNamespace("app")); err == nil {
		t.Error("NewWithClient(nil) should fail")
	}
}

// TestFormat 命名空间后缀 → 格式。
func TestFormat(t *testing.T) {
	cases := map[string]string{
		"application":     json, // 无后缀 → json
		"application.yml": yml,
		"app-config.yaml": yaml,
		"manifest.json":   json,
		"app.properties":  json,
		"weird.unknown":   json,
	}
	for ns, want := range cases {
		if got := format(ns); got != want {
			t.Errorf("format(%q) = %q, want %q", ns, got, want)
		}
	}
}

// TestGenKey 展开键生成（格式后缀剥离）。
func TestGenKey(t *testing.T) {
	if got := genKey("application", "log.level"); got != "application.log.level" {
		t.Errorf("genKey(properties) = %q", got)
	}
	if got := genKey("app-config.yaml", "log.level"); got != "app-config.log.level" {
		t.Errorf("genKey(yaml) = %q", got)
	}
	if got := genKey("", "k"); got != "k" {
		t.Errorf("genKey(empty ns) = %q", got)
	}
}

// TestResolve 扁平 KV → 嵌套 map。
func TestResolve(t *testing.T) {
	m := map[string]any{}
	resolve("app.name", "bald", m)
	resolve("app.log.level", "debug", m)

	app, ok := m["app"].(map[string]any)
	if !ok {
		t.Fatalf("m[app] missing: %#v", m)
	}
	if app["name"] != "bald" {
		t.Errorf("app.name = %v", app["name"])
	}
	sub, ok := app["log"].(map[string]any)
	if !ok {
		t.Fatalf("app.log missing: %#v", app)
	}
	if sub["level"] != "debug" {
		t.Errorf("app.log.level = %v", sub["level"])
	}
}
