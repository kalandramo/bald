package test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

// captureT 在独立 goroutine 里用零值 testing.T 跑 fn：Fatalf/FailNow 经
// runtime.Goexit 终结该 goroutine（defer 仍执行），Errorf 则正常返回，两种情形都
// 由 done 回收，返回的 T 供断言 Failed()。零值 T 不支持 Run（nil panic），故只
// 用于不含 t.Run 的助手；含 t.Run 的 ToolsForCommand 失败分支走 re-exec 子进程。
func captureT(fn func(t *testing.T)) *testing.T {
	ft := new(testing.T)
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(ft)
	}()
	<-done
	return ft
}

// noopToolsCmd 造一个 foo/tools 空操作命令（不写 mcp-tools.json），让
// GetToolsForCommand 的 Execute 成功，而文件状态完全由调用方控制。
func noopToolsCmd() *cobra.Command {
	root := &cobra.Command{Use: "noop", SilenceErrors: true, SilenceUsage: true}
	foo := &cobra.Command{Use: "foo"}
	foo.AddCommand(&cobra.Command{Use: "tools", Run: func(*cobra.Command, []string) {}})
	root.AddCommand(foo)
	return root
}

// Execute 失败（未知命令）→ Fatalf。
func TestGetToolsForCommand_ExecuteError(t *testing.T) {
	ft := captureT(func(ft *testing.T) { GetToolsForCommand(ft, noopToolsCmd(), "nonexistent") })
	require.True(t, ft.Failed())
}

// Execute 成功但未生成 mcp-tools.json → ReadFile 失败 → Fatalf。
func TestGetToolsForCommand_ReadError(t *testing.T) {
	t.Chdir(t.TempDir())
	ft := captureT(func(ft *testing.T) { GetToolsForCommand(ft, noopToolsCmd(), "foo") })
	require.True(t, ft.Failed())
}

// mcp-tools.json 非法 JSON → Unmarshal 失败 → Fatalf。
func TestGetToolsForCommand_UnmarshalError(t *testing.T) {
	t.Chdir(t.TempDir())
	require.NoError(t, os.WriteFile("mcp-tools.json", []byte("not json"), 0o644))
	ft := captureT(func(ft *testing.T) { GetToolsForCommand(ft, noopToolsCmd(), "foo") })
	require.True(t, ft.Failed())
}

// 清理阶段 Remove 失败仅 Logf 警告（不 Fatal）：父目录只读时删除 EACCES。
// root 绕过权限位，不可复现时如实跳过。
func TestGetToolsForCommand_RemoveWarning(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 绕过文件权限位，Remove 失败分支不可复现")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "mcp-tools.json"), []byte("[]"), 0o644))
	t.Chdir(dir)
	require.NoError(t, os.Chmod(dir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	ft := captureT(func(ft *testing.T) { GetToolsForCommand(ft, noopToolsCmd(), "foo") })
	require.False(t, ft.Failed(), "Remove 失败只应警告，不应 Fatal")
}

// RawInputSchema 合法 → 走 Raw 分支返回 schema。
func TestGetInputSchema_RawValid(t *testing.T) {
	var got *jsonschema.Schema
	ft := captureT(func(ft *testing.T) {
		got = GetInputSchema(ft, &mcp.Tool{RawInputSchema: json.RawMessage(`{"type":"object"}`)})
	})
	require.False(t, ft.Failed())
	require.Equal(t, "object", got.Type)
}

// RawInputSchema 非法 JSON → Fatalf。
func TestGetInputSchema_RawInvalid(t *testing.T) {
	ft := captureT(func(ft *testing.T) {
		GetInputSchema(ft, &mcp.Tool{Name: "bad", RawInputSchema: json.RawMessage(`{bad`)})
	})
	require.True(t, ft.Failed())
}

// InputSchema 含不可编码值（chan）→ Marshal 失败 → Fatalf。
func TestGetInputSchema_MarshalError(t *testing.T) {
	tool := &mcp.Tool{Name: "bad", InputSchema: mcp.ToolInputSchema{
		Type:       "object",
		Properties: map[string]any{"x": make(chan int)},
	}}
	ft := captureT(func(ft *testing.T) { GetInputSchema(ft, tool) })
	require.True(t, ft.Failed())
}

// InputSchema 编码成功但属性值不是合法 schema（数字）→ Unmarshal 失败 → Fatalf。
func TestGetInputSchema_UnmarshalError(t *testing.T) {
	tool := &mcp.Tool{Name: "bad", InputSchema: mcp.ToolInputSchema{
		Type:       "object",
		Properties: map[string]any{"p": 42},
	}}
	ft := captureT(func(ft *testing.T) { GetInputSchema(ft, tool) })
	require.True(t, ft.Failed())
}

// 工具数量不符 → Errorf。
func TestToolNames_CountMismatch(t *testing.T) {
	ft := captureT(func(ft *testing.T) { ToolNames(ft, []*mcp.Tool{{Name: "a"}}, "a", "b") })
	require.True(t, ft.Failed())
}

// 工具名不在期望列表 → Errorf。
func TestToolNames_UnexpectedName(t *testing.T) {
	ft := captureT(func(ft *testing.T) { ToolNames(ft, []*mcp.Tool{{Name: "x"}}, "a") })
	require.True(t, ft.Failed())
}

// Tools/ToolsForCommand 快乐路径：合法工具树 → 两个子测试通过。
func TestTools_HappyPath(t *testing.T) {
	t.Chdir(t.TempDir())
	Tools(t, createTestCommand(), "testcli_get", "testcli_list")
}

// ToolsForCommand 的 schema 断言分支（type 非 object / 残留 flags / 残留 args）
// 经 t.Run 触发，零值 T 不支持 Run，故 re-exec 子进程真测：子进程喂 malformed
// schema 工具，断言进程以 1 退出且 stderr 带三条断言文案。
func TestToolsForCommand_SchemaErrors_Reexec(t *testing.T) {
	if os.Getenv("BALD_REEXEC_TOOLS_SCHEMA") == "1" {
		t.Chdir(t.TempDir())
		tools := []*mcp.Tool{
			{Name: "t1", InputSchema: mcp.ToolInputSchema{Type: "string", Properties: map[string]any{}}},
			{Name: "t2", InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{"flags": map[string]any{"type": "object"}}}},
			{Name: "t3", InputSchema: mcp.ToolInputSchema{Type: "object", Properties: map[string]any{"args": map[string]any{"type": "object"}}}},
		}
		data, err := json.Marshal(tools)
		if err != nil {
			t.Fatalf("marshal tools: %v", err)
		}
		if err := os.WriteFile("mcp-tools.json", data, 0o644); err != nil {
			t.Fatalf("write tools: %v", err)
		}
		ToolsForCommand(t, noopToolsCmd(), "foo", "t1", "t2", "t3")
		return // 子测试失败 → testing 框架以 1 退出并 flush 日志
	}

	// 显式把父进程的 GOCOVERDIR 作为 -test.gocoverdir 传给子进程：子进程经
	// testing 框架正常退出时既 flush 失败日志，又把覆盖计数写进同一目录供合并。
	args := []string{"-test.run=^TestToolsForCommand_SchemaErrors_Reexec$"}
	if gcd := os.Getenv("GOCOVERDIR"); gcd != "" {
		args = append(args, "-test.gocoverdir="+gcd)
	}
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), "BALD_REEXEC_TOOLS_SCHEMA=1")
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("ToolsForCommand 遇 malformed schema 必须以 1 退出，got err=%v out=%s", err, out)
	}
	for _, want := range []string{"expected schema type 'object'", "nested 'flags'", "nested 'args'"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("输出缺少 %q：\n%s", want, out)
		}
	}
}
