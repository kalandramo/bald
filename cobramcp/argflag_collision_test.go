package cobramcp

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// H3 回归：位置参数与 flag 同名时必须拒绝注册并报错。
//
// 背景：扁平 Schema 把 flag 与位置参数都平铺为顶层属性。若某个位置参数的
// 规范化名字（normaliseName 结果）与某个 flag 名相同，二者会争用同一个属性名，
// 执行期同一个输入值会被同时当作 flag 与位置参数消费（同值双消费），
// 且 Schema 中只有一个属性胜出——静默的错误行为。
//
// 契约：检测到冲突时该工具不得注册，且必须返回错误（而非静默跳过）。

// newCollisionCmd 构造一个位置参数与 flag 同名的命令：Use 中的 <output>
// 规范化后为 "output"，与 --output flag 冲突。
func newCollisionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use: "get <output>",
		Run: func(*cobra.Command, []string) {},
	}
	cmd.Flags().String("output", "", "output format")
	return cmd
}

func TestCreateToolFromCmd_RejectsArgFlagNameCollision(t *testing.T) {
	cmd := newCollisionCmd()

	_, _, err := Selector{}.createToolFromCmd(cmd, "myapp")
	require.Error(t, err, "位置参数与 flag 同名时必须返回错误")
	assert.Contains(t, err.Error(), "output",
		"错误信息应点名冲突的属性名，便于定位")
}

func TestRegisterTools_RejectsArgFlagNameCollision(t *testing.T) {
	root := &cobra.Command{Use: "myapp"}
	root.AddCommand(newCollisionCmd())
	// 一个正常命令，用来确认冲突命令被拒绝而非整个注册静默清空。
	ok := &cobra.Command{
		Use: "list",
		Run: func(*cobra.Command, []string) {},
	}
	root.AddCommand(ok)

	c := &Config{}
	err := c.registerTools(root)
	require.Error(t, err, "注册链必须把冲突错误上抛")

	// 冲突工具不得进入工具集。
	for _, tool := range c.tools {
		assert.NotEqual(t, "myapp_get", tool.Name,
			"冲突命令 %q 不得被注册为工具", tool.Name)
	}
}

// 对照组：不同名的位置参数与 flag 必须正常注册（确认检测不过度触发）。
func TestCreateToolFromCmd_AllowsDistinctArgAndFlagNames(t *testing.T) {
	cmd := &cobra.Command{
		Use: "get <resource>",
		Run: func(*cobra.Command, []string) {},
	}
	cmd.Flags().String("output", "", "output format")

	tool, meta, err := Selector{}.createToolFromCmd(cmd, "myapp")
	require.NoError(t, err, "名字不冲突时不得报错")
	require.NotNil(t, tool)
	assert.Contains(t, meta.flagNames, "output")
	require.Len(t, meta.argSpecs, 1)
	assert.Equal(t, "resource", meta.argSpecs[0].Name)
}
