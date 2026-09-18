package cobramcp

import (
	"context"
	"fmt"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// M4 回归：buildFlagArgs 输出必须确定性——同一输入多次调用得到完全相同的
// 参数序列（map 迭代顺序在 Go 中是随机的，不排序会导致顺序漂移）。
func TestBuildFlagArgs_DeterministicOrder(t *testing.T) {
	flags := map[string]any{
		"alpha":   "1",
		"beta":    "2",
		"gamma":   "3",
		"delta":   "4",
		"epsilon": true,
	}

	first := buildFlagArgs(flags)
	require.NotEmpty(t, first)

	for i := 0; i < 50; i++ {
		assert.Equal(t, first, buildFlagArgs(flags),
			"第 %d 次调用的参数顺序与首次不一致——buildFlagArgs 不是确定性的", i)
	}

	// 排序后 delta 必在 gamma 之前。
	deltaIdx := indexOf(first, "--delta")
	gammaIdx := indexOf(first, "--gamma")
	require.NotEqual(t, -1, deltaIdx)
	require.NotEqual(t, -1, gammaIdx)
	assert.Less(t, deltaIdx, gammaIdx, "flag 应按名称排序输出")
}

// L1 回归：未知工具名必须 fail-closed，而不是退化为执行 root 命令。
func TestRunInProcess_UnknownToolFailsClosed(t *testing.T) {
	var executed string

	factory := func() *cobra.Command {
		root := &cobra.Command{Use: "myapp"}
		root.RunE = func(cmd *cobra.Command, _ []string) error {
			executed = cmd.CommandPath()
			return nil
		}
		return root
	}

	srv, err := NewMCPServer(MCPOptions{Enabled: false}, factory)
	require.NoError(t, err)

	_, _, err = srv.runInProcess(context.Background(),
		mcp.CallToolRequest{Params: mcp.CallToolParams{Name: "does_not_exist"}},
		ToolInput{FlatInput: map[string]any{}, FlagNames: map[string]struct{}{}})

	require.Error(t, err, "未知工具名必须返回错误")
	assert.Contains(t, err.Error(), "does_not_exist")
	assert.Equal(t, "", executed, "未知工具名不得执行任何命令（尤其不得退化执行 root）")
	fmt.Printf("L1: unknown tool rejected: %v\n", err)
}
