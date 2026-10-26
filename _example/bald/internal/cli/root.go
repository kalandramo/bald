// Package cli 构造示例进程的 cobra 入口（角色 → 命令树）。
//
// 角色与二进制一一对应：cmd/bald-demo（api 角色）与 cmd/bald-demo-worker
// （worker 角色）各自的 main.go → app.Execute → 本包——入口里没有装配知识，
// 与生成物 cmd/<app>/app/command.go 的分工一致（Execute 的唯一职责是把角色
// 交给 bootstrap.Options）。两个角色共用本包，故没有两份复制的命令树。
package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/pkg/appkit"

	"github.com/kalandramo/bald/example/bald/internal/bootstrap"
	usercmd "github.com/kalandramo/bald/example/bald/user"
)

// Main 运行指定角色的根命令（serve 为默认行为）。
//
// use 是二进制名（cobra 的 Use），short 是一句话说明；/--config、--log.* 等
// 框架 flag 由 MustServe 内化的 parseConfigFlag 自行扫描 os.Args，不在此 Bind。
func Main(role, use, short string) error {
	root := &cobra.Command{
		Use:   use,
		Short: short,
		RunE: func(*cobra.Command, []string) error {
			// 唯一装配入口：Options(role) 是装配投影（internal/bootstrap）。
			// 失败在 MustServe 内记日志并以非零码退出。
			appkit.MustServe(context.Background(), bootstrap.Options(role)...)
			return nil
		},
	}
	// 存储后端切换演示子命令（业务子命令挂 root，与 serve 并存）。
	root.AddCommand(usercmd.NewUserCommand())
	root.FParseErrWhitelist.UnknownFlags = true
	return runWithPlugin(root)
}

// runWithPlugin 实现 kubectl 风格插件发现：首参不是已知子命令时，转发到
// PATH 中的 bald-<name> 可执行文件（未找到则交回 cobra 打原生 unknown 提示）。
func runWithPlugin(root *cobra.Command) error {
	if len(os.Args) > 1 {
		sub := os.Args[1]
		if !isKnownCommand(root, sub) {
			plugin := "bald-" + sub
			path, err := exec.LookPath(plugin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "unknown command %q (and no plugin %q found in PATH)\n", sub, plugin)
				return root.Execute()
			}
			cmd := exec.Command(path, os.Args[2:]...)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			return cmd.Run()
		}
	}
	return root.Execute()
}

// isKnownCommand 判断 name 是否 root 的已注册子命令。
func isKnownCommand(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}
