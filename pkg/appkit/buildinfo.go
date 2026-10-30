package appkit

import (
	"runtime/debug"
	"strings"
)

// BuildTags 返回构建期 -tags 设置（未显式指定或 BuildInfo 不可用时返回
// nil）。doctor 的 `build tags` 行与生成物的 variant 投影共用同一口径：
// 变体是否命中，取决于「这个二进制实际怎么编的」，而不是生成期猜测。
func BuildTags() []string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	for _, s := range bi.Settings {
		if s.Key == "-tags" && s.Value != "" {
			return strings.Split(s.Value, ",")
		}
	}
	return nil
}

// HasBuildTags 报告 tags 是否同时包含 want 的全部项（want 为空返回 true：
// 无 tag 要求的变体总是命中）。
func HasBuildTags(tags []string, want ...string) bool {
	if len(want) == 0 {
		return true
	}
	have := make(map[string]bool, len(tags))
	for _, t := range tags {
		have[strings.TrimSpace(t)] = true
	}
	for _, w := range want {
		if !have[strings.TrimSpace(w)] {
			return false
		}
	}
	return true
}
