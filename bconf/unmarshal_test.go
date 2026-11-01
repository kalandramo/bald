package bconf

import (
	"reflect"
	"testing"
	"time"
)

// TestUnmarshalMapDoesNotMutateInput：类型规范化会改名与搬键，但必须只作用于
// 内部副本——调用方常把同一份快照先后反序列化到契约与业务两个消息。
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
	before := cloneTree(m)

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
