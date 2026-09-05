package bconf

import (
	"testing"
	"time"
)

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
