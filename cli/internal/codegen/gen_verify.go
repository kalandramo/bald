package codegen

// gen_verify.go 是 gen verify 子命令：**只读**聚合校验——把 gen app --check 与
// gen domain --check 串成一次「生成树与声明是否一致」的回答。
//
// 为什么需要它（而不是把 gen domain 挂进 MCP 面）：
//
//	cobramcp 的 flag 选择器只能**排除** flag，不能**钉住** --check。把 gen domain
//	暴露成工具，等于把「重写域产物」交给 AI——一次误调就把 owned 文件全刷了。
//	给它标 destructiveHint 只是让客户端弹个确认框，责任仍在调用方。
//	反过来，AI 真正需要的是「我改完声明，树还对得上吗」这一问的答案。gen verify
//	从构造上只读（两个子流程都走各自的 --check 分支，零写入），不需要破坏性
//	注解，也不需要信任调用方——这正是 onexai 「把判定权交给确定性工具」的形态。
//
// 它不新增生成逻辑：两条腿各自复用 appOptions/domainOptions 的 --check 分支，
// 所以「一致」的口径与手跑两条命令**同一份实现**。verify 只负责顺序、前缀与
// 汇总，不产生第二种判定。

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/cobramcp"
)

// verifyOptions 是 gen verify 叶子命令的领域配置。
type verifyOptions struct {
	Spec    string // --spec：AppSpec JSON 路径（缺省发现 cwd 的 appspec.json）
	OutRoot string // --out：产出根（默认 .）
	NoBind  bool   // --no-bind：跳过注解绑定校验（新 proto 的 Go 类型尚未生成时）

	ioStreams
}

func (o *verifyOptions) Complete() error {
	if o.OutRoot == "" {
		o.OutRoot = "."
	}
	return nil
}

func (o *verifyOptions) Validate() error {
	if !discoverSpec(&o.Spec, o.Out) {
		return fmt.Errorf("--spec <AppSpec.json> is required (or run it in the directory holding appspec.json)")
	}
	return nil
}

// Run 纯执行：逐段校验，任何一段不一致即整体失败（前缀标明是哪一段）。
func (o *verifyOptions) Run() error {
	fmt.Fprintln(o.Out, "verify: app tree")
	if err := runGenAppSpec(o.Spec, o.OutRoot, false, true, o.ioStreams); err != nil {
		return fmt.Errorf("app tree: %w", err)
	}
	fmt.Fprintln(o.Out, "verify: domain artifacts")
	dom := &domainOptions{Spec: o.Spec, OutDir: o.OutRoot, Check: true, NoBind: o.NoBind, ioStreams: o.ioStreams}
	if err := dom.Complete(); err != nil {
		return err
	}
	if err := dom.Validate(); err != nil {
		return err
	}
	if err := dom.Run(); err != nil {
		return fmt.Errorf("domain artifacts: %w", err)
	}
	fmt.Fprintln(o.Out, "verified:", o.Spec, "(app tree + domain artifacts match the declaration)")
	return nil
}

// genVerifyCmd 组装 gen verify 叶子命令。
func genVerifyCmd(streams ioStreams) *cobra.Command {
	o := &verifyOptions{ioStreams: streams}
	cmd := &cobra.Command{
		Use:   "verify --spec <AppSpec.json>",
		Short: "只读校验生成树与声明是否一致（app 树 + 域产物，零写入）",
		Long: `把 gen app --check 与 gen domain --check 串成一次只读校验：

  1. app 树   cmd/<app>/{main.go,app/command.go}、bootstrap 装配、configs、
             taskfile.yml、build/docker/** 是否与声明逐字节一致
  2. 域产物   internal/bootstrap/domains.gen.go、handler/convert、authz/alias、
             handler/gin/*.bind.gen.go、routes/*.gen.go 是否与声明一致

零写入：两条腿都走各自的 --check 分支，不生成、不改清单、不执行外部命令。
有差异即失败，错误里带逐文件清单（哪份文件缺、哪份与声明不同）。

这条命令是给「改完声明想自证」的场合用的——包括 AI 会话（它作为只读 MCP 工具
暴露，因为从构造上不可能写盘）。人工改完声明后的完整重建仍用 gen app --adopt
与 gen domain --adopt。`,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(*cobra.Command, []string) error {
			if err := o.Complete(); err != nil {
				return err
			}
			if err := o.Validate(); err != nil {
				return err
			}
			return o.Run()
		},
	}
	cmd.Flags().StringVar(&o.Spec, "spec", "", "AppSpec JSON 路径（缺省取 cwd 的 appspec.json）")
	cmd.Flags().StringVar(&o.OutRoot, "out", "", "产出根（默认 .）")
	cmd.Flags().BoolVar(&o.NoBind, "no-bind", false, "跳过注解绑定校验（新 proto 的 Go 类型尚未生成时）")
	// MCP 注解：只读是**构造性**的（两条腿都是 --check），不是承诺。
	cmd.Annotations = map[string]string{
		cobramcp.AnnotationReadOnly:   "true",
		cobramcp.AnnotationIdempotent: "true",
		cobramcp.AnnotationTitle:      "Verify generated tree matches the AppSpec",
	}
	_ = cmd.MarkFlagFilename("spec", ".json")
	cmd.SetErr(streams.ErrOut)
	return cmd
}
