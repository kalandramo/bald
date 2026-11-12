package bconf

import (
	"reflect"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// TestUnmarshalMapDoesNotMutateInput：类型规范化会改名与搬键，但必须只作用于
// 内部副本——调用方常把同一份快照先后反序列化到契约与业务两个消息。
// snapshotTree 测试内深拷贝：钉住 UnmarshalMap 输入不可变这一契约时的基线快照。

func snapshotTree(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, vv := range x {
			out[k] = snapshotTree(vv)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = snapshotTree(vv)
		}
		return out
	default:
		return v
	}
}

func TestUnmarshalMapDoesNotMutateInput(t *testing.T) {
	m := map[string]any{
		"server": map[string]any{
			"http": map[string]any{"addr": ":18080"},
		},
		"logger": map[string]any{
			"backends": []any{
				map[string]any{"type": "slog", "slog": map[string]any{"output_path": "stdout"}},
			},
		},
	}
	before := snapshotTree(m)

	if err := UnmarshalMap(m, NewBootstrap()); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(m, before) {
		t.Fatalf("input mutated:\n got %#v\nwant %#v", m, before)
	}

	// 同一份 map 二次反序列化仍应成功（典型的热更新回调里契约 + 业务各一次）。
	if err := UnmarshalMap(m, NewBootstrap()); err != nil {
		t.Fatalf("second unmarshal: %v", err)
	}
}

// TestCoerceDurationFormat 直接验证 duration 格式化：不能用 time.Duration.String()，
// 因为 "1m30s" 这类复合表示会被 protojson 拒绝。
func TestCoerceDurationFormat(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{10 * time.Second, "10s"},
		{90 * time.Second, "90s"}, // 不是 "1m30s"
		{1500 * time.Millisecond, "1.5s"},
		{0, "0s"},
	}
	for _, tc := range cases {
		if got := formatDuration(tc.in); got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUnmarshalNestedBackoffSection 证明跨后端的 Backoff 段真能从配置树落进消息：
// 三层嵌套（registry → etcd → backoff）、嵌套消息里的 google.protobuf.Duration、
// 以及按名字写的两个枚举。消费侧（`registry/etcd/contract`）的翻译只有在解得出来
// 时才有意义，所以这条判的是「写了读得到」，不是翻译对不对。
func TestUnmarshalNestedBackoffSection(t *testing.T) {
	m := map[string]any{
		"registry": map[string]any{
			"type": "etcd",
			"etcd": map[string]any{
				"endpoints": []any{"127.0.0.1:2379"},
				"backoff": map[string]any{
					"curve":   "LINEAR",
					"initial": "1500ms",
					"step":    "500ms",
					"max":     "90s",
					"jitter":  "EQUAL",
				},
			},
		},
	}

	cfg := NewBootstrap()
	if err := UnmarshalMap(m, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	b := cfg.GetRegistry().GetEtcd().GetBackoff()
	if b == nil {
		t.Fatal("registry.etcd.backoff 没落进消息（GetBackoff 返回 nil）")
	}
	if got := b.GetCurve(); got != bootstrapv1.Backoff_LINEAR {
		t.Errorf("curve = %v, want LINEAR", got)
	}
	if got := b.GetJitter(); got != bootstrapv1.Backoff_EQUAL {
		t.Errorf("jitter = %v, want EQUAL", got)
	}
	for _, c := range []struct {
		name string
		got  *durationpb.Duration
		want time.Duration
	}{
		{"initial", b.GetInitial(), 1500 * time.Millisecond},
		{"step", b.GetStep(), 500 * time.Millisecond},
		{"max", b.GetMax(), 90 * time.Second},
	} {
		if d := c.got.AsDuration(); d != c.want {
			t.Errorf("%s = %v, want %v", c.name, d, c.want)
		}
	}
}

// TestUnmarshalAbsentBackoffStaysNil：段缺席必须是 nil，不是零值消息。
// 消费侧靠这个 Presence 区分「沿用后端默认曲线」和「按契约覆盖」。
func TestUnmarshalAbsentBackoffStaysNil(t *testing.T) {
	cfg := NewBootstrap()
	if err := UnmarshalMap(map[string]any{
		"registry": map[string]any{"type": "etcd", "etcd": map[string]any{"ttl": 20}},
	}, cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if b := cfg.GetRegistry().GetEtcd().GetBackoff(); b != nil {
		t.Fatalf("缺席的 backoff 段落成了 %#v，want nil", b)
	}
}

// TestUnmarshalDurationSpellings 四种时长写法必须落进同一个字段值。
// "1500ms"/"1m30s" 是 Go 合法串却不是 protojson 合法值——早先字符串分支只校验
// 不重排，配置里写这两式会在 protojson 那步报一个不带键名的位置错。
func TestUnmarshalDurationSpellings(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"10s", 10 * time.Second},
		{"1.5s", 1500 * time.Millisecond},
		{"1500ms", 1500 * time.Millisecond},
		{"1m30s", 90 * time.Second},
	}
	for _, tc := range cases {
		cfg := NewBootstrap()
		err := UnmarshalMap(map[string]any{
			"registry": map[string]any{
				"type": "etcd",
				"etcd": map[string]any{
					"backoff": map[string]any{"initial": tc.in, "max": "5m"},
				},
			},
		}, cfg)
		if err != nil {
			t.Fatalf("initial=%q: unmarshal: %v", tc.in, err)
		}
		b := cfg.GetRegistry().GetEtcd().GetBackoff()
		if b == nil {
			t.Fatalf("initial=%q: backoff 段没落进消息", tc.in)
		}
		if got := b.GetInitial().AsDuration(); got != tc.want {
			t.Errorf("initial=%q: 落进 %v, want %v", tc.in, got, tc.want)
		}
		if got := b.GetMax().AsDuration(); got != 5*time.Minute {
			t.Errorf("initial=%q: max=5m 落进 %v, want 5m", tc.in, got)
		}
	}
}
