package main

// main_test.go 钉住 MCP 工具面的**边界**（不是清单本身）：
//
// 工具面每扩一格，AI 就多一份能改用户仓库的能力。边界靠三件事守住：
//
//  1. 写命令不进（gen app/domain/proto/store/resource、release、ide init、prd）
//  2. 进的那条要么天然只读（convention），要么构造上只读（gen verify 两条腿
//     都是 --check）
//  3. 新增命令默认不进——空选择器会让工具面被动扩大，所以这里遍历真实命令树
//     逐个判，而不是维护一份允许清单

import (
	"strings"
	"testing"

	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// commandPath 返回命令路径（去掉根命令名，空格分隔）。
func commandPath(cmd *cobra.Command) string {
	parts := strings.Fields(cmd.CommandPath())
	if len(parts) > 0 {
		parts = parts[1:]
	}
	return strings.Join(parts, " ")
}

// leafCommands 遍历真实命令树，返回所有可运行叶子（跳过 help/completion/mcp
// 这类框架内置命令——它们由 cobramcp 的安全过滤器统一处理）。
func leafCommands(root *cobra.Command) map[string]*cobra.Command {
	out := map[string]*cobra.Command{}
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			name := child.Name()
			if name == "help" || name == "completion" || name == "mcp" {
				continue
			}
			if !child.HasSubCommands() {
				out[commandPath(child)] = child
			}
			walk(child)
		}
	}
	walk(root)
	return out
}

// selectorFor 模拟 cobramcp 的选择：按顺序取第一个命中的 CmdSelector。
func selectorFor(cmd *cobra.Command, sels []cobramcp.Selector) *cobramcp.Selector {
	for i := range sels {
		if sels[i].CmdSelector == nil || sels[i].CmdSelector(cmd) {
			return &sels[i]
		}
	}
	return nil
}

// TestMCPSurface_OnlyReadOnlyCommands 全树对账：进 MCP 面的命令逐条符合
// 「只读或已排掉写 flag」，且写命令一条都不在。
func TestMCPSurface_OnlyReadOnlyCommands(t *testing.T) {
	root := newRootCommand()
	sels := mcpSelectors()

	// 明确不进工具面的写/外向命令（新增写命令时这条断言逼你表态）。
	//   - gen app 整体不进：它的 schema 里 --check 是可选布尔，客户端不传时
	//     执行的就是写树路径——排除个别 flag 钉不住只读承诺（cobramcp 没有
	//     「钉住取值」的原语），自证统一走 gen verify。
	//   - release changelog 写 CHANGELOG.md，prd translate 写翻译提示词文件，
	//     都是真实写路径，与 plan/sync/tag 同罪。
	mustBeExcluded := []string{
		"gen app", "gen proto", "gen store", "gen domain", "gen resource",
		"ide init", "release plan", "release sync", "release changelog", "release tag",
		"prd translate",
		// compose 整组不进：起停本机容器是不可回滚的环境副作用；只读的那三个
		// （list/config/status）也不进——本机环境归人管，信息在 `compose list` 里。
		"compose up", "compose stop", "compose down", "compose status",
		"compose list", "compose config",
	}
	leaves := leafCommands(root)
	for _, path := range mustBeExcluded {
		cmd, ok := leaves[path]
		if !ok {
			continue // 命令不存在（重命名/移除）不算失败
		}
		if sel := selectorFor(cmd, sels); sel != nil {
			t.Errorf("%q must not be exposed over MCP (it writes or acts outward), got selector localFlags=%v",
				path, sel.LocalFlagSelector != nil)
		}
	}

	// 明确要进的两条只读通道。
	for _, path := range []string{"gen verify"} {
		cmd, ok := leaves[path]
		if !ok {
			t.Fatalf("%q must exist (read-only self-check channel)", path)
		}
		if sel := selectorFor(cmd, sels); sel == nil {
			t.Errorf("%q must be exposed over MCP", path)
		}
	}
	verify := leaves["gen verify"]
	if got := verify.Annotations[cobramcp.AnnotationReadOnly]; got != "true" {
		t.Errorf("gen verify must be annotated readOnlyHint=true (it is read-only by construction), got %q", got)
	}

	// convention：只读查询面，flags 不设限。
	conv, ok := leaves["convention check"]
	if !ok {
		t.Fatal("convention check must exist")
	}
	if selectorFor(conv, sels) == nil {
		t.Error("convention check must be exposed over MCP")
	}
}

// TestMCPSurface_GenVerifyIsReadOnlyByConstruction gen verify 的只读性来自
// 实现（两条腿都是 --check），不来自调用方自觉：它的 flag 集合里不得出现任何
// 写开关。这条断言防止有人日后给它加 --adopt/--force 并顺手带进工具面。
func TestMCPSurface_GenVerifyIsReadOnlyByConstruction(t *testing.T) {
	root := newRootCommand()
	verify, ok := leafCommands(root)["gen verify"]
	if !ok {
		t.Fatal("gen verify must exist")
	}
	writeFlags := map[string]bool{"adopt": true, "force": true, "apply-decl": true, "write": true}
	verify.Flags().VisitAll(func(f *pflag.Flag) {
		if writeFlags[f.Name] {
			t.Errorf("gen verify must not carry a write flag --%s (its MCP readOnlyHint would become a lie)", f.Name)
		}
	})
}
