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
//	bald gen resource --spec <AppSpec.json> --domain post --kind post
//	                                                   # 给域加资源链路（契约+实体+声明+重派生）
//	bald gen verify --spec <AppSpec.json>              # 只读校验生成树与声明一致（零写入）
//	bald compose up [etcd redis]                       # 起本地集成依赖（etcd/redis/nacos，逐项对齐 CI services）
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
	"github.com/kalandramo/bald/cli/internal/compose"
	"github.com/kalandramo/bald/cli/internal/convention"
	"github.com/kalandramo/bald/cli/internal/ide"
	"github.com/kalandramo/bald/cli/internal/prd"
	"github.com/kalandramo/bald/cli/internal/release"
)

func main() {
	codegen.CheckErr(newRootCommand().Execute())
}

// mcpSelectors 是 MCP 工具面的唯一真相源（装配与测试共用，不出现第二份口径）。
//
// MCP 面：只读动作进，写动作挡（渐进式放行见 docs/architecture/
// onexai-gap-review.md §6.2 与 developer-experience-review.md DX-16）。
//   - convention：查规范，天然只读；
//   - gen verify：**从构造上只读**（内部两条腿都是各自的 --check 分支），
//     覆盖 app 树 + 域产物；标注 readOnly/idempotent，客户端无需确认框。
//
// gen app 曾经以「排除 adopt/preset 两个写 flag」的形态进面，已撤出：它的
// schema 里 --check 是可选布尔，客户端不传时执行的就是写树路径——readOnlyHint
// 与实际行为矛盾（cobramcp 没有「钉住取值」的原语，排除 flag 不够）。自证统一
// 走 gen verify，能力无损失（app 树那条腿就是 gen app --check 的同一份实现）。
//
// gen 的其它子命令（domain/proto/store/resource）与 release/compose 是写/外向
// 动作，不进——不靠 destructiveHint 让客户端提醒，而是根本不给工具面。留空的
// 选择器只会让工具面随命令新增而被动扩大，所以这条边界由 cmd/bald/main_test.go
// 钉住。
func mcpSelectors() []cobramcp.Selector {
	return []cobramcp.Selector{
		{CmdSelector: cobramcp.AllowCmdsContaining("convention")},
		{CmdSelector: cobramcp.AllowCmdsContaining("gen verify")},
	}
}

// newRootCommand 组装整棵命令树（main 与测试共用：MCP 面断言要遍历真实树）。
func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "bald",
		Short: "bald 服务框架官方 CLI：代码生成、规范查询与会话引导工具",
		Long: `bald 是 Go 服务框架（库）；本 CLI 提供配套的开发工具。

代码生成脚手架 gen 输出可编译骨架（proto / store / app / domain / resource），
作为 bald 生态的 starter：模板质量以 _example/bald 消费者模块的端到端编译/运行
测试固化为防线。

convention 把工程规范做成可查询、可检查的蒸馏卡：overview 先看索引，get 按
需取卡（MCP 工具 bald_convention_get 供 AI 用），check 跑确定性检查器（CI 用
--fail-on-findings 拦截）。ide init 为 AI 会话生成引导物（规范去哪里查）。

release 面向框架维护者：从 git 提交历史推导待发布模块与版本，统一生成
变更日志并打 tag。`,
		SilenceUsage:  true, // 错误不打 usage（flag 错误由 cobra 单独提示）
		SilenceErrors: true, // 错误统一由 CheckErr 出口打印
	}
	root.AddCommand(codegen.NewCommand(codegen.NewDefaultIOStreams()))
	root.AddCommand(compose.NewCommand(compose.NewDefaultStreams()))
	root.AddCommand(convention.NewCommand(convention.NewDefaultStreams()))
	root.AddCommand(ide.NewCommand())
	root.AddCommand(prd.NewCommand())
	root.AddCommand(release.NewCommand(release.NewDefaultIOStreams()))
	root.AddCommand(cobramcp.Command(&cobramcp.Config{Selectors: mcpSelectors()}))
	return root
}
