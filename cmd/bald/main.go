// Command bald 是 bald 框架的官方开发工具 CLI：
// 提供 gen proto / gen store / gen app / gen domain 生成子命令，以及维护者
// 使用的 release 发布子命令（计划 / 依赖归位 / 变更日志 / 打 tag）。
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
//	bald gen app --spec <AppSpec.json>                 # 按 AppSpec 生成应用装配目录树
//	bald gen domain --spec <AppSpec.json>              # 按域声明生成域产物（gen app 之后）
//	bald release plan                                  # 计算待发布模块与下一版本
//	bald release sync --write                          # 归位计划内模块的内部依赖
//
// 代码生成不替代手写业务：生成物是可编译骨架，业务路由/handler/grpc service
// 注册留填充点（见各模板头部注释）。
package main

import (
	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/internal/codegen"
	"github.com/kalandramo/bald/internal/release"
)

func main() {
	root := &cobra.Command{
		Use:   "bald",
		Short: "bald 服务框架官方 CLI：代码生成与发布工具",
		Long: `bald 是 Go 服务框架（库）；本 CLI 提供配套的开发工具。

代码生成脚手架 gen 输出可编译骨架（proto / store / app），作为 bald 生态的
starter：模板质量以 _example/bald 消费者模块的端到端编译/运行测试固化为防线。

release 面向框架维护者：从 git 提交历史推导待发布模块与版本，统一生成
变更日志并打 tag。`,
		SilenceUsage:  true, // 错误不打 usage（flag 错误由 cobra 单独提示）
		SilenceErrors: true, // 错误统一由 CheckErr 出口打印
	}
	root.AddCommand(codegen.NewCommand(codegen.NewDefaultIOStreams()))
	root.AddCommand(release.NewCommand(release.NewDefaultIOStreams()))
	codegen.CheckErr(root.Execute())
}
