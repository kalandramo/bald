// Command bald 是 bald 框架的官方开发工具 CLI（P4/P12 工具链归属落地）：
// 提供 gen proto / gen store / gen app 三个代码生成子命令，使 bald 生态的
// 新 proto 服务 / 实体 / 应用装配骨架可直接 `go install` 分发。
//
// 安装：
//
//	go install github.com/kalandramo/bald/cmd/bald@latest
//
// 用法：
//
//	bald gen proto <name>                              # 生成 protobuf 服务骨架
//	bald gen proto <name> --go-package "<module>/...;v1" # 指定 go_package（默认不写）
//	bald gen store <name>                              # 生成实体骨架（gorm tag + keyOf）
//	bald gen app <name>                                # 生成应用装配骨架 main.go（appkit 全原语）
//	bald gen app <name> --spec <AppSpec.json>          # P12：AppSpec 方言驱动装配
//
// 代码生成不替代手写业务：生成物是可编译骨架，业务路由/handler/grpc service
// 注册留 TODO 填充点（见各模板头部注释）。
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/internal/codegen"
)

func main() {
	root := &cobra.Command{
		Use:   "bald",
		Short: "bald 服务框架官方 CLI：代码生成脚手架",
		Long: `bald 是 Go 服务框架（库）；本 CLI 提供配套的开发工具。

代码生成脚手架 gen 输出可编译骨架（proto / store / app），作为 bald 生态的
starter：模板质量以 _example/bald 消费者模块的端到端编译/运行测试固化为防线。
文档见 docs/devel/zh-CN/架构优化路线.md §P12。`,
	}
	root.AddCommand(codegen.NewCommand())
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
