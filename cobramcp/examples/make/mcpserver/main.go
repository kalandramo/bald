// Command mcpserver 演示把"已有的 make 包装命令"直接暴露为 MCP 工具。
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
	File      string
	Directory string
}

func main() {
	cfg := cobramcp.MCPOptions{
		Enabled: true,
		Addr:    ":8999",
		Name:    "make-mcp",
		Version: "0.1.0",
	}

	srv, err := cobramcp.NewMCPServer(cfg, makeCmd)
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

func makeCmd() *cobra.Command {
	f := &flags{}

	cmd := &cobra.Command{
		Use:   "make [targets...]",
		Short: "Execute make targets",
		RunE:  f.run,
		Args:  cobra.ArbitraryArgs,
	}

	cmd.Flags().StringVarP(&f.File, "file", "f", "", "Use FILE as a makefile")
	cmd.Flags().StringVarP(&f.Directory, "directory", "C", "", "Change to directory before doing anything")
	if err := cmd.MarkFlagRequired("directory"); err != nil {
		panic(err)
	}

	return cmd
}

func (f *flags) run(cmd *cobra.Command, args []string) error {
	if f.File != "" {
		args = append(args, "-f", f.File)
	}
	if f.Directory != "" {
		args = append(args, "-C", f.Directory)
	}

	subCmd := exec.CommandContext(cmd.Context(), "make", args...)
	subCmd.Stdout = cmd.OutOrStdout()
	subCmd.Stderr = cmd.ErrOrStderr()

	return subCmd.Run()
}
