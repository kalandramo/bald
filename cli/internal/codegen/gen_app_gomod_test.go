package codegen

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// TestSeedGoMod_WritesValidGoDirective 复现种子 go.mod 的 go 指令无效：
// runtime.Version() 返回 "go1.27.1"（带 go 前缀），直接拼进 go 指令会生成
// `go go1.27.1`——每个新生成树的第一次 `go mod tidy` 即失败，且 go.mod 不在
// manifest 清单内，--check 拦不住。修复后复用 appspec_gen.go 的
// goMinorVersion()（剥前缀、取主次版本），go 指令必须是 1.NN 形式。
func TestSeedGoMod_WritesValidGoDirective(t *testing.T) {
	root := t.TempDir()
	streams := NewDefaultIOStreams()

	wrote, err := seedGoMod(root, "example.com/skeleton", streams)
	if err != nil {
		t.Fatalf("seedGoMod: %v", err)
	}
	if !wrote {
		t.Fatal("空目录下 seedGoMod 应写入 go.mod")
	}

	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	// 已存在时不覆盖（幂等口径沿用）。
	wroteAgain, err := seedGoMod(root, "example.com/skeleton", streams)
	if err != nil || wroteAgain {
		t.Fatalf("二次 seedGoMod 不应重写: wrote=%v err=%v", wroteAgain, err)
	}

	// go 指令必须匹配 go.mod 语法的版本形式（1.NN[.patch]），不带工具链前缀。
	goLine := regexp.MustCompile(`(?m)^go (.+)$`).FindSubmatch(data)
	if goLine == nil {
		t.Fatalf("go.mod 缺少 go 指令:\n%s", data)
	}
	if !regexp.MustCompile(`^[1-9][0-9]*(\.[0-9]+){1,2}$`).Match(goLine[1]) {
		t.Fatalf("go 指令版本无效 %q（修复前是 runtime.Version() 的 go1.NN 原文）:\n%s", goLine[1], data)
	}
}
