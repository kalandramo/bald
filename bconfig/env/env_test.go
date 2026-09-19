package env

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/bconfig"
)

// TestNew_NoError 钉住 New 的失败面：当前实现只填默认 options，永不返回 error。
// 参数错误（如非法 prefix）不在此处拦截——与 fs 源「New(nil) 报错」不同。
func TestNew_NoError(t *testing.T) {
	src, err := New()
	if err != nil {
		t.Fatalf("New() = %v, want nil error", err)
	}
	if src == nil {
		t.Fatal("New() returned nil source")
	}
}

// TestResolveKey 钉住 key 归一规则（纯逻辑，不经环境变量）：
// 显式 key 优先于默认 key；prefix 非空且 key 非空时加前缀。
func TestResolveKey(t *testing.T) {
	cases := []struct {
		name   string
		opts   []Option
		key    string
		want   string
	}{
		{"显式 key 直通", nil, "FOO", "FOO"},
		{"空 key 用默认 key", []Option{WithKey("DEF")}, "", "DEF"},
		{"显式 key 优先于默认", []Option{WithKey("DEF")}, "FOO", "FOO"},
		{"prefix 加到显式 key", []Option{WithPrefix("APP_")}, "FOO", "APP_FOO"},
		{"prefix 加到默认 key", []Option{WithPrefix("APP_"), WithKey("DEF")}, "", "APP_DEF"},
		{"prefix 空串不加", []Option{WithPrefix("")}, "FOO", "FOO"},
		{"prefix 非空但 key 空则不加", []Option{WithPrefix("APP_")}, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src, err := New(c.opts...)
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := src.resolveKey(c.key); got != c.want {
				t.Errorf("resolveKey(%q) = %q, want %q", c.key, got, c.want)
			}
		})
	}
}

// TestLoad_NoKeySpecified 空 key 且无默认 key → 报错（而非静默返回 nil）。
func TestLoad_NoKeySpecified(t *testing.T) {
	src, _ := New()
	_, err := src.Load(context.Background(), "")
	if err == nil {
		t.Fatal("Load with no key should fail")
	}
}

// TestLoad_Unset 变量未设置 → (nil, nil)，不是错误。
// 这是与 fs「文件不存在报错」不同的关键语义：env 源按「未配置该变量」处理。
func TestLoad_Unset(t *testing.T) {
	src, _ := New(WithKey("BALD_ENV_TEST_DEFINITELY_ABSENT_9f3a"))
	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load(unset) = error %v, want nil", err)
	}
	if data != nil {
		t.Errorf("Load(unset) = %q, want nil", data)
	}
}

// TestLoad_Set 变量已设置 → 返回其字节值。
func TestLoad_Set(t *testing.T) {
	t.Setenv("BALD_ENV_TEST_VALUE", "log:\n  level: debug")
	src, _ := New(WithKey("BALD_ENV_TEST_VALUE"))
	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "log:\n  level: debug" {
		t.Errorf("Load() = %q", data)
	}
}

// TestLoad_EmptyValue 变量显式置空串 → 返回空字节切片而非 nil。
// os.LookupEnv 对空串返回 ("", true)，故 env 源区分「未设置(nil)」与
// 「设置为空([]byte{})」——空值会让上层解码得到空文档。
func TestLoad_EmptyValue(t *testing.T) {
	t.Setenv("BALD_ENV_TEST_EMPTY", "")
	src, _ := New(WithKey("BALD_ENV_TEST_EMPTY"))
	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if data == nil {
		t.Error("显式空值应返回非 nil 空切片（区别于未设置）")
	}
	if len(data) != 0 {
		t.Errorf("Load() = %q, want empty", data)
	}
}

// TestLoad_Prefix 前缀真正拼接到查找名上（经真实环境变量验证）。
func TestLoad_Prefix(t *testing.T) {
	t.Setenv("BALD_ENV_TEST_APP_KEY", "value-with-prefix")
	src, _ := New(WithPrefix("BALD_ENV_TEST_APP_"), WithKey("KEY"))
	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "value-with-prefix" {
		t.Errorf("Load() = %q, want value-with-prefix", data)
	}
}

// TestLoad_ExplicitKeyOverridesDefault 显式 key 优先于默认 key；两者都经 prefix 拼接。
func TestLoad_ExplicitKeyOverridesDefault(t *testing.T) {
	t.Setenv("PREFIX_DEF", "from-default")
	t.Setenv("PREFIX_EXPLICIT", "from-explicit")
	src, _ := New(WithPrefix("PREFIX_"), WithKey("DEF"))

	// Load("") → 用默认 key "DEF" → 拼 prefix → "PREFIX_DEF"
	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load(default): %v", err)
	}
	if string(data) != "from-default" {
		t.Errorf("默认 key：got %q, want from-default", data)
	}

	// Load("EXPLICIT") → 显式 key 覆盖默认 → 拼 prefix → "PREFIX_EXPLICIT"
	data, err = src.Load(context.Background(), "EXPLICIT")
	if err != nil {
		t.Fatalf("Load(explicit): %v", err)
	}
	if string(data) != "from-explicit" {
		t.Errorf("显式 key：got %q, want from-explicit", data)
	}
}

// TestLoad_ExplicitKeyWithoutPrefix 无 prefix 时显式 key 直通。
func TestLoad_ExplicitKeyWithoutPrefix(t *testing.T) {
	t.Setenv("BALD_ENV_TEST_RAW", "raw-value")
	src, _ := New()
	data, err := src.Load(context.Background(), "BALD_ENV_TEST_RAW")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "raw-value" {
		t.Errorf("Load() = %q, want raw-value", data)
	}
}

// TestSource_ImplementsReader 编译期断言的运行时对照（断言本身在 env.go）。
func TestSource_ImplementsReader(t *testing.T) {
	var src bconfig.Reader = &source{}
	if src == nil {
		t.Fatal("source should implement bconfig.Reader")
	}
}
