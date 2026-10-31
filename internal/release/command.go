package release

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
)

// DefaultPlanPath 是计划文件的默认落点。.bald/ 已在本仓库 .gitignore 中，
// 计划是「本机一次发布的中间产物」，不进版本库。
const DefaultPlanPath = ".bald/release-plan.json"

// IOStreams 是命令的标准出入流，生产绑定进程标准流，测试可注入缓冲。
type IOStreams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// NewDefaultIOStreams 返回绑定进程标准流的 IOStreams。
func NewDefaultIOStreams() IOStreams {
	return IOStreams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
}

// NewCommand 组装 `bald release` 节点：plan / sync / changelog / tag。
func NewCommand(streams IOStreams) *cobra.Command {
	cmd := &cobra.Command{
		Use:              "release [command]",
		Short:            "多模块发布：计划 / 依赖归位 / 变更日志 / 打 tag",
		TraverseChildren: true,
		SilenceUsage:     true,
		RunE:             func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(planCmd(streams), syncCmd(streams), changelogCmd(streams), tagCmd(streams))
	cmd.SetErr(streams.ErrOut)
	return cmd
}

type planOptions struct {
	PrintJSON bool   // --json：把计划以 JSON 打到 stdout（同时仍写计划文件）
	PlanFile  string // --out：计划文件路径
	IOStreams
}

func planCmd(streams IOStreams) *cobra.Command {
	o := &planOptions{PlanFile: DefaultPlanPath, IOStreams: streams}
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "计算待发布模块与下一版本，写入计划文件",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			repo, err := OpenRepo()
			if err != nil {
				return err
			}
			plan, err := repo.BuildPlan(time.Now())
			if err != nil {
				return err
			}
			if err := WritePlan(o.PlanFile, plan); err != nil {
				return err
			}
			if o.PrintJSON {
				data, err := json.MarshalIndent(plan, "", "  ")
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintln(o.Out, string(data))
			} else if err := PrintPlan(o.Out, plan, repo.Root); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(o.ErrOut, "plan:", o.PlanFile)
			return nil
		},
	}
	cmd.Flags().BoolVar(&o.PrintJSON, "json", false, "以 JSON 输出计划（仍写入计划文件）")
	cmd.Flags().StringVar(&o.PlanFile, "out", DefaultPlanPath, "计划文件路径")
	cmd.SetErr(streams.ErrOut)
	return cmd
}

type syncOptions struct {
	PlanFile string
	Write    bool
	IOStreams
}

func syncCmd(streams IOStreams) *cobra.Command {
	o := &syncOptions{PlanFile: DefaultPlanPath, IOStreams: streams}
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "把计划内模块 go.mod 的内部依赖归位到下一版本",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			repo, plan, err := loadPlan(o.PlanFile)
			if err != nil {
				return err
			}
			return Sync(repo.Root, plan, o.Write, o.Out)
		},
	}
	cmd.Flags().StringVar(&o.PlanFile, "plan", DefaultPlanPath, "计划文件路径")
	cmd.Flags().BoolVar(&o.Write, "write", false, "真正写入 go.mod（默认只打印将做的修改）")
	cmd.SetErr(streams.ErrOut)
	return cmd
}

type changelogOptions struct {
	PlanFile string
	Write    bool
	IOStreams
}

func changelogCmd(streams IOStreams) *cobra.Command {
	o := &changelogOptions{PlanFile: DefaultPlanPath, IOStreams: streams}
	cmd := &cobra.Command{
		Use:   "changelog",
		Short: "把发布计划写入 CHANGELOG.md",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			repo, plan, err := loadPlan(o.PlanFile)
			if err != nil {
				return err
			}
			return WriteChangelog(repo.Root, plan, o.Write, o.Out)
		},
	}
	cmd.Flags().StringVar(&o.PlanFile, "plan", DefaultPlanPath, "计划文件路径")
	cmd.Flags().BoolVar(&o.Write, "write", false, "真正写入 CHANGELOG.md（默认只打印将写入的内容）")
	cmd.SetErr(streams.ErrOut)
	return cmd
}

type tagOptions struct {
	PlanFile string
	Apply    bool
	Push     bool
	IOStreams
}

func tagCmd(streams IOStreams) *cobra.Command {
	o := &tagOptions{PlanFile: DefaultPlanPath, IOStreams: streams}
	cmd := &cobra.Command{
		Use:   "tag",
		Short: "按计划在 HEAD 打 tag（默认 dry-run）",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			repo, plan, err := loadPlan(o.PlanFile)
			if err != nil {
				return err
			}
			return Tag(repo.Root, plan, o.Apply, o.Push, o.Out)
		},
	}
	cmd.Flags().StringVar(&o.PlanFile, "plan", DefaultPlanPath, "计划文件路径")
	cmd.Flags().BoolVar(&o.Apply, "apply", false, "真正创建 tag（默认只打印）")
	cmd.Flags().BoolVar(&o.Push, "push", false, "创建后推送到 origin（需同时传 --apply）")
	cmd.SetErr(streams.ErrOut)
	return cmd
}

func loadPlan(path string) (*Repo, *Plan, error) {
	repo, err := OpenRepo()
	if err != nil {
		return nil, nil, err
	}
	plan, err := ReadPlan(path)
	if err != nil {
		return nil, nil, err
	}
	return repo, plan, nil
}
