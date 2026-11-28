package cobramcp

import "github.com/spf13/cobra"

// walkToolCmds 是双引擎共用的工具注册遍历骨架：先下钻子命令（叶子先序），
// 被 filtered 命中的节点跳过，其余命令按登记顺序尝试 selectors，CmdSelector
// 命中后交 visit 落库。
//
// 收敛的原因：Config（子进程/REST 面）与 MCPServer（进程内面）此前各持一份
// 同形状遍历——序、过滤点、selector 求值顺序是同一份知识，两份实现即 J2
// 重复税，单侧修序另一侧静默漂移。面特有语义全在 visit：
//   - visit 返回 error 即终止整个遍历（两侧现状一致：建工具失败 = 装配失败）；
//   - visit 返回 stop=true 停止尝试该命令的后续 selector；stop=false 继续
//     下一个 selector（进程内面 RegisterHandler 失败后保留原语义继续试）。
func walkToolCmds(cmd *cobra.Command, filtered func(*cobra.Command) bool, selectors []Selector, visit func(cmd *cobra.Command, sel Selector, selIdx int) (stop bool, err error)) error {
	// register all subcommands（叶子先序，工具列表按既有序生成）
	for _, subCmd := range cmd.Commands() {
		if err := walkToolCmds(subCmd, filtered, selectors, visit); err != nil {
			return err
		}
	}

	// apply basic filters
	if filtered(cmd) {
		return nil
	}

	// cycle through selectors until one matches the cmd
	for i, s := range selectors {
		if s.CmdSelector != nil && !s.CmdSelector(cmd) {
			continue
		}
		stop, err := visit(cmd, s, i)
		if err != nil {
			return err
		}
		if stop {
			break
		}
	}

	return nil
}

// cmdFilterBase 是双引擎共用的内置安全过滤：hidden/deprecated 命令，以及
// 无 Run/RunE/PreRun/PreRunE 的纯容器节点。各面尾部的内建命令组排除语义
// 不同（Config 还要按 CommandName 摘掉自身命令组），留在各自的 cmdFilter。
func cmdFilterBase(cmd *cobra.Command) bool {
	if cmd.Hidden || cmd.Deprecated != "" {
		return true
	}

	if cmd.Run == nil && cmd.RunE == nil && cmd.PreRun == nil && cmd.PreRunE == nil {
		return true
	}

	return false
}
