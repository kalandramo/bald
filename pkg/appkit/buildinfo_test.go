package appkit

import "testing"

// TestHasBuildTags 变体命中的唯一判据：want 的全部 tag 都在构建 tag 里。
func TestHasBuildTags(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want []string
		ok   bool
	}{
		{"no requirement always matches", []string{"grpcgw"}, nil, true},
		{"all tags present", []string{"grpcgw", "nacos", "config"}, []string{"nacos", "config"}, true},
		{"missing one tag", []string{"grpcgw"}, []string{"nacos", "config"}, false},
		{"empty build tags", nil, []string{"grpcgw"}, false},
		{"surrounding whitespace trimmed", []string{" grpcgw "}, []string{"grpcgw"}, true},
		{"duplicate want", []string{"grpcgw"}, []string{"grpcgw", "grpcgw"}, true},
	}
	for _, tc := range cases {
		if got := HasBuildTags(tc.tags, tc.want...); got != tc.ok {
			t.Errorf("%s: HasBuildTags(%v, %v) = %v, want %v", tc.name, tc.tags, tc.want, got, tc.ok)
		}
	}
}
