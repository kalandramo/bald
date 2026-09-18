// Command mcpserver 演示把"已有的 task 包装命令"直接暴露为 MCP 工具。
//
// 关键点：cmdFactory 每次工具调用都会重建一棵全新的命令树，因此每次调用都有
// 独立的 Options/flags/闭包变量，不存在跨调用的共享状态。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"
)

type flags struct {
	Taskfile  string
	Directory string
}

func main() {
	cfg := cobramcp.MCPOptions{
		Enabled: true,
		Addr:    ":8999",
		Name:    "task-mcp",
		Version: "0.1.0",
	}

	srv, err := cobramcp.NewMCPServer(cfg, taskCmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "new MCP server: %v\n", err)
		os.Exit(1)
	}

	// Start 阻塞直到 ctx 取消；中断信号由进程默认行为处理（信号归宿主）。
	if err := srv.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "start MCP server: %v\n", err)
		os.Exit(1)
	}
}

func taskCmd() *cobra.Command {
	f := &flags{}

	cmd := &cobra.Command{
		Use:   "task [tasks...]",
		Short: "Execute task targets",
		RunE:  f.run,
		Args:  cobra.ArbitraryArgs,
	}

	cmd.Flags().StringVarP(&f.Taskfile, "taskfile", "t", "", "Choose which Taskfile to run")
	cmd.Flags().StringVarP(&f.Directory, "dir", "d", "", "Directory in which Task will execute and look for a Taskfile")
	if err := cmd.MarkFlagRequired("dir"); err != nil {
		panic(err)
	}

	return cmd
}

func (f *flags) run(cmd *cobra.Command, args []string) error {
	// Flags precede the task names: `task -d DIR -t FILE <tasks...>`.
	var taskArgs []string
	if f.Directory != "" {
		taskArgs = append(taskArgs, "-d", f.Directory)
	}
	if f.Taskfile != "" {
		taskArgs = append(taskArgs, "-t", f.Taskfile)
	}
	taskArgs = append(taskArgs, args...)

	subCmd := exec.CommandContext(cmd.Context(), "task", taskArgs...)
	subCmd.Stdout = cmd.OutOrStdout()
	subCmd.Stderr = cmd.ErrOrStderr()

	return subCmd.Run()
}
