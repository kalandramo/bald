package appkit

import (
	"fmt"
	"go/build/constraint"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// BuildTags 返回构建期 -tags 设置（未显式指定或 BuildInfo 不可用时返回
// nil）。doctor 的 `build tags` 行与变体命中判据共用同一口径：变体是否
// 命中，取决于「这个二进制实际怎么编的」，而不是生成期猜测。
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
// 无 tag 要求的变体总是命中）。它是变体表达式的历史捷径；新声明请用
// When + EvaluateBuildConstraint。
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

// testBuildTagSet 仅供包内测试注入构建期 tag 集（nil = 用真实二进制）。
// 命中判据必须与 `go build` 同源，测试不能靠改环境变量伪造 -tags。
var testBuildTagSet func() map[string]bool

// EvaluateBuildConstraint 按 Go 自己的构建约束语义求值表达式（`//go:build`
// 行的内容，如 "grpcgw && !nacos"）。tag 集 = 二进制实际 -tags ∪
// GOOS/GOARCH/gc/cgo/go1.N/unix——与 `go build` 的隐式 tag 对齐，避免
// 「本地看命中、CI 不命中」的静默分叉。空表达式恒为 true。
func EvaluateBuildConstraint(expr string) (bool, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return true, nil
	}
	c, err := constraint.Parse("//go:build " + expr)
	if err != nil {
		return false, fmt.Errorf("appkit: build constraint %q: %w", expr, err)
	}
	set := buildTagSet()
	return c.Eval(func(tag string) bool { return set[tag] }), nil
}

// buildTagSet 汇总表达式求值可见的 tag：显式 -tags、GOOS/GOARCH、编译器、
// cgo、release tags（go1.N）与 unix 归类——go/build 与 cmd/go 的隐式 tag
// 集合。BuildTags 来自 debug.ReadBuildInfo，缺失时只剩平台 tag。
func buildTagSet() map[string]bool {
	if testBuildTagSet != nil {
		return testBuildTagSet()
	}
	set := map[string]bool{
		runtime.GOOS:   true,
		runtime.GOARCH: true,
		"gc":           true,
	}
	for _, t := range BuildTags() {
		if t = strings.TrimSpace(t); t != "" {
			set[t] = true
		}
	}
	for _, t := range releaseTags() {
		set[t] = true
	}
	if unixGOOS[runtime.GOOS] {
		set["unix"] = true
	}
	if cgoEnabled() {
		set["cgo"] = true
	}
	return set
}

// releaseTags 生成 go1.1 … go1.N（N = 当前工具链 minor）：Go 的构建约束
// 用 release tag 判版本，且历史版本 tag 全部成立。
func releaseTags() []string {
	lang := runtime.Version() // go1.27.1 / devel ...
	lang = strings.TrimPrefix(lang, "go")
	parts := strings.SplitN(lang, ".", 3)
	if len(parts) < 2 {
		return nil
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil || minor < 1 {
		return nil
	}
	out := make([]string, 0, minor)
	for i := 1; i <= minor; i++ {
		out = append(out, "go1."+strconv.Itoa(i))
	}
	return out
}

// unixGOOS 是 Go 构建约束里 `unix` tag 覆盖的 GOOS 集合（go/build 同款）。
var unixGOOS = map[string]bool{
	"aix": true, "android": true, "darwin": true, "dragonfly": true,
	"freebsd": true, "hurd": true, "illumos": true, "ios": true,
	"linux": true, "netbsd": true, "openbsd": true, "solaris": true,
}

// cgoEnabled 读 BuildInfo 的 CGO_ENABLED 设置（不可用时按未启用：
// 纯 Go 构建是默认假设，cgo 变体必须在显式启用时命中）。
func cgoEnabled() bool {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, s := range bi.Settings {
		if s.Key == "CGO_ENABLED" {
			return s.Value == "1"
		}
	}
	return false
}
