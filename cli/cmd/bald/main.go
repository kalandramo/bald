// Command bald 是 bald 框架的官方开发工具 CLI：
// 提供 gen proto / gen store / gen app / gen domain 生成子命令，convention
// 规范查询与确定性检查子命令（AI 会话的规范通道），ide init 会话引导物，
// 以及维护者使用的 release 发布子命令（计划 / 依赖归位 / 变更日志 / 打 tag）。
//
// 安装：CLI 是独立 module github.com/kalandramo/bald/cli，其 go.mod 零 replace，
// 所以 Go 的「按版本装命名包」不会拒它（根模块带着同仓 replace，拒的就是那条）：
//
//	go install github.com/kalandramo/bald/cli/cmd/bald@latest
//
// 要改生成器本身才从源码检出装：
//
//	git clone https://github.com/kalandramo/bald.git && cd bald/cli
//	go install ./cmd/bald
//
// 库按各自的 module 路径引用（如 github.com/kalandramo/bald/pkg）；仓库根模块
// 只剩本仓工具包，不对外提供可导入包。
//
// 用法：
//
//	bald gen proto <name>                              # 生成 protobuf 服务骨架
//	bald gen proto <name> --go-package "<module>/...;v1" # 指定 go_package（默认不写）
//	bald gen store <name>                              # 生成实体骨架（gorm tag + keyOf）
//	bald gen app --spec <AppSpec.json>                 # 按 AppSpec 生成应用装配目录树
//	bald gen domain --spec <AppSpec.json>              # 按域声明生成域产物（gen app 之后）
//	bald convention overview                           # 规范卡索引（AI 的入口）
//	bald convention get <卡id>                         # 取卡全文（--format json 供工具消费）
//	bald convention check [--fail-on-findings]         # 卡完整性 + 确定性检查器（CI 用 fail-on-findings）
//	bald ide init --ide=claude                         # 初始化 AI 会话引导物（.mcp.json/AGENTS.md/.claude）
//	bald release plan                                  # 计算待发布模块与下一版本
//	bald release sync --write                          # 归位计划内模块的内部依赖
//
// cobramcp 把 CLI 子命令暴露为 MCP 工具（bald mcp start），selector 只放行
// convention 查询面——gen 写文件、release 打 tag 的外向/写动作不进 AI 工具面。
//
// 代码生成不替代手写业务：生成物是可编译骨架，业务路由/handler/grpc service
// 注册留填充点（见各模板头部注释）。
package main

import (
	"github.com/kalandramo/bald/cobramcp"
	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/cli/internal/codegen"
	"github.com/kalandramo/bald/cli/internal/convention"
	"github.com/kalandramo/bald/cli/internal/ide"
	"github.com/kalandramo/bald/cli/internal/prd"
	"github.com/kalandramo/bald/cli/internal/release"
)

func main() {
	root := &cobra.Command{
		Use:   "bald",
		Short: "bald 服务框架官方 CLI：代码生成、规范查询与会话引导工具",
		Long: `bald 是 Go 服务框架（库）；本 CLI 提供配套的开发工具。

代码生成脚手架 gen 输出可编译骨架（proto / store / app / domain），作为 bald
生态的 starter：模板质量以 _example/bald 消费者模块的端到端编译/运行测试固化
为防线。

convention 把工程规范做成可查询、可检查的蒸馏卡：overview 先看索引，get 按
需取卡（MCP 工具 bald_convention_get 供 AI 用），check 跑确定性检查器（CI 用
--fail-on-findings 拦截）。ide init 为 AI 会话生成引导物（规范去哪里查）。

release 面向框架维护者：从 git 提交历史推导待发布模块与版本，统一生成
变更日志并打 tag。`,
		SilenceUsage:  true, // 错误不打 usage（flag 错误由 cobra 单独提示）
		SilenceErrors: true, // 错误统一由 CheckErr 出口打印
	}
	root.AddCommand(codegen.NewCommand(codegen.NewDefaultIOStreams()))
	root.AddCommand(convention.NewCommand(convention.NewDefaultStreams()))
	root.AddCommand(ide.NewCommand())
	root.AddCommand(prd.NewCommand())
	root.AddCommand(release.NewCommand(release.NewDefaultIOStreams()))
	// MCP 面：只放行 convention（查询规范，只读）。gen 是写动作、release 是
	// 打 tag 的外向动作，都不进 AI 工具面（渐进式放行见 docs/architecture/
	// onexai-gap-review.md §6.2）。
	root.AddCommand(cobramcp.Command(&cobramcp.Config{
		Selectors: []cobramcp.Selector{
			{CmdSelector: cobramcp.AllowCmdsContaining("convention")},
		},
	}))
	codegen.CheckErr(root.Execute())
}
