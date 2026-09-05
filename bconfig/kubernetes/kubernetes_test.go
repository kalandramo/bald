package kubernetes

import "testing"

// TestNew_Lazy 构造不建连（惰性 init）。
func TestNew_Lazy(t *testing.T) {
	k := New(WithNamespace("ns1"), WithConfigMapName("app"), WithDataKey("app.yaml"))
	if k.opts.Namespace != "ns1" || k.opts.ConfigMapName != "app" || k.opts.DataKey != "app.yaml" {
		t.Errorf("opts = %+v", k.opts)
	}
	if k.client != nil {
		t.Error("client should be lazy")
	}
}

// TestResolveKey 键解析与契约默认回填。
func TestResolveKey(t *testing.T) {
	k := New(WithNamespace("ns-def"), WithConfigMapName("cm-def"), WithDataKey("key-def"))

	// 完整三段。
	if ns, name, dk := k.resolveKey("a/b/c"); ns != "a" || name != "b" || dk != "c" {
		t.Errorf("three-part: %q/%q/%q", ns, name, dk)
	}
	// 两段：dataKey 空（合并全部）。
	if ns, name, dk := k.resolveKey("a/b"); ns != "a" || name != "b" || dk != "" {
		t.Errorf("two-part: %q/%q/%q", ns, name, dk)
	}
	// 单段：用默认 namespace，dataKey 空。
	if ns, name, dk := k.resolveKey("name"); ns != "ns-def" || name != "name" || dk != "" {
		t.Errorf("one-part: %q/%q/%q", ns, name, dk)
	}
	// 空 key：契约装配回填全部默认。
	if ns, name, dk := k.resolveKey(""); ns != "ns-def" || name != "cm-def" || dk != "key-def" {
		t.Errorf("empty-key: %q/%q/%q", ns, name, dk)
	}
}

// TestResolveKey_NoDefaults 未配置默认值时空 key 回填全空。
func TestResolveKey_NoDefaults(t *testing.T) {
	k := New()
	if ns, name, dk := k.resolveKey(""); ns != "" || name != "" || dk != "" {
		t.Errorf("empty defaults: %q/%q/%q", ns, name, dk)
	}
}
