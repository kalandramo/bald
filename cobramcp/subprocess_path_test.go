package cobramcp

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H1 回归：子进程模型的命令路径还原。
//
// 工具名由 CommandPath 的空格替换为下划线生成（selector.go toolName），该编码
// 不是双射——命令名本身可能含下划线。因此执行期绝不能从工具名反解命令路径，
// 必须使用注册期预存的路径（toolMeta.cmdPath）。
//
// 本测试钉住的契约：只要注册期记录了命令路径，buildCommandArgs 就必须还原出
// 与该路径完全一致的 CLI 参数前缀，与工具名中是否含下划线无关。

// registerTreeForTest 构造一棵 root 名与子命令名均含下划线的命令树并注册工具，
// 返回工具名 → toolMeta。
func registerTreeForTest(t *testing.T) map[string]toolMeta {
	t.Helper()

	root := &cobra.Command{Use: "my_app"}
	sre := &cobra.Command{Use: "sre"}
	open := &cobra.Command{
		Use: "open",
		Run: func(*cobra.Command, []string) {},
	}
	sre.AddCommand(open)
	root.AddCommand(sre)

	c := &Config{}
	c.registerTools(root)

	return c.toolMetas
}

func TestSubprocessPath_UnderscoreInRootName(t *testing.T) {
	metas := registerTreeForTest(t)

	// root "my_app" + 子命令 "sre open" → 工具名 "my_app_sre_open"
	meta, ok := metas["my_app_sre_open"]
	require.True(t, ok, "工具 my_app_sre_open 应已注册，实际注册项：%v", keysOf(metas))

	// 注册期必须记录真实命令路径（root 名之后的部分）。
	assert.Equal(t, []string{"sre", "open"}, meta.cmdPath)

	// 执行期从注册期元数据还原参数，而非从工具名反解。
	args := buildCommandArgs(ToolInput{CmdPath: meta.cmdPath})
	assert.Equal(t, []string{"sre", "open"}, args,
		"命令路径必须原样还原；从工具名反解会把 my_app 切碎成 my/app 并丢失首段")
}

func TestSubprocessPath_UnderscoreInCommandName(t *testing.T) {
	root := &cobra.Command{Use: "myapp"}
	getAll := &cobra.Command{
		Use: "get_all",
		Run: func(*cobra.Command, []string) {},
	}
	root.AddCommand(getAll)

	c := &Config{}
	c.registerTools(root)

	meta, ok := c.toolMetas["myapp_get_all"]
	require.True(t, ok, "工具 myapp_get_all 应已注册，实际注册项：%v", keysOf(c.toolMetas))

	// 子命令名 get_all 是一个整体，不能被下划线切成 get all。
	assert.Equal(t, []string{"get_all"}, meta.cmdPath)

	args := buildCommandArgs(ToolInput{CmdPath: meta.cmdPath})
	assert.Equal(t, []string{"get_all"}, args,
		"子命令名中的下划线不是层级分隔符")
}

// keysOf 返回 map 的键，便于失败信息可读。
func keysOf(m map[string]toolMeta) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
