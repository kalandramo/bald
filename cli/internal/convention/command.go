package convention

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/kalandramo/bald/cobramcp"
)

// Streams 是本包命令的标准出入流（与 codegen/release 各自的 ioStreams 同型：
// 命令不直接写 os.Stdout，测试注入 buffer 即可断言输出）。
type Streams struct {
	In     io.Reader
	Out    io.Writer
	ErrOut io.Writer
}

// NewDefaultStreams 返回绑定进程标准流的 Streams（生产路径）。
func NewDefaultStreams() Streams {
	return Streams{In: os.Stdin, Out: os.Stdout, ErrOut: os.Stderr}
}

// readOnly 把查询类命令标注为 MCP 只读工具（cobramcp 读注解生成 mcp.ToolAnnotation）。
func readOnly(title string) map[string]string {
	return map[string]string{
		cobramcp.AnnotationReadOnly: "true",
		cobramcp.AnnotationTitle:    title,
	}
}

// NewCommand 构造 convention 命令树：
//
//	bald convention overview        # 索引 + 取用方式（AI 的入口）
//	bald convention list            # 卡清单（--level/--category 过滤）
//	bald convention get <id>        # 卡全文（--format md|json）
//	bald convention check           # 卡完整性 + 已注册确定性检查器
func NewCommand(streams Streams) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "convention",
		Short: "查询与检查 bald 工程规范（蒸馏卡）",
		Long: `bald 工程规范的可查询、可检查版本。

规范分三层消费：overview 先给索引（人/AI 都从这里进），get 按需取卡全文
（CLI 给人看，MCP 工具 bald_convention_get 给 AI 用），check 跑卡声明的
确定性检查器（生成后自检、CI 拦截）。

它不创建文件、不修改项目：解决的是「当代码由人、AI 或两者共同编写时，
如何让所有人遵守同一套工程约定」。check 通过不代表代码一定符合规范——
它只是机械化的底线，代码评审不可替代。`,
		SilenceUsage: true,
	}
	cmd.AddCommand(overviewCmd(streams))
	cmd.AddCommand(listCmd(streams))
	cmd.AddCommand(getCmd(streams))
	cmd.AddCommand(checkCmd(streams))
	return cmd
}

func overviewCmd(streams Streams) *cobra.Command {
	return &cobra.Command{
		Use:         "overview",
		Short:       "全部规范卡的索引与取用方式（AI 的入口）",
		Args:        cobra.NoArgs,
		Annotations: readOnly("bald 规范总览"),
		RunE: func(_ *cobra.Command, _ []string) error {
			cards, err := Load()
			if err != nil {
				return err
			}
			fmt.Fprint(streams.Out, renderOverview(cards))
			return nil
		},
	}
}

func listCmd(streams Streams) *cobra.Command {
	o := &struct {
		Level    string
		Category string
	}{}
	cmd := &cobra.Command{
		Use:         "list",
		Short:       "列出规范卡（--level/--category 过滤）",
		Args:        cobra.NoArgs,
		Annotations: readOnly("bald 规范卡清单"),
		RunE: func(_ *cobra.Command, _ []string) error {
			cards, err := Load()
			if err != nil {
				return err
			}
			for _, c := range cards {
				if o.Level != "" && c.Level != o.Level {
					continue
				}
				if o.Category != "" && c.Category != o.Category {
					continue
				}
				fmt.Fprintf(streams.Out, "%-10s %-12s %-6s %s\n", c.ID, c.Category, c.Level, c.Title)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Level, "level", "", "按主体级别过滤（强制|建议|参考）")
	cmd.Flags().StringVar(&o.Category, "category", "", "按类别过滤（core|codegen|ops）")
	return cmd
}

func getCmd(streams Streams) *cobra.Command {
	o := &struct {
		Format string
	}{}
	cmd := &cobra.Command{
		Use:   "get <id>",
		Short: "取一张卡全文（--format md|json）",
		Args:  cobra.ExactArgs(1),
		Annotations: map[string]string{
			cobramcp.AnnotationReadOnly:   "true",
			cobramcp.AnnotationTitle:      "取 bald 规范卡全文",
			cobramcp.AnnotationOutputJSON: "true", // --format json 时 stdout 是 JSON 契约
		},
		RunE: func(_ *cobra.Command, args []string) error {
			cards, err := Load()
			if err != nil {
				return err
			}
			card, err := getCard(cards, args[0])
			if err != nil {
				return err
			}
			switch o.Format {
			case "json":
				raw, err := marshalCard(*card)
				if err != nil {
					return err
				}
				fmt.Fprintln(streams.Out, string(raw))
			case "md", "":
				fmt.Fprintln(streams.Out, strings.TrimRight(card.Body, "\n"))
			default:
				return fmt.Errorf("--format %q must be md or json", o.Format)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Format, "format", "md", "输出格式：md | json")
	return cmd
}

func checkCmd(streams Streams) *cobra.Command {
	o := &checkOptions{Dir: ".", MinLevel: "mandatory", Format: "text"}
	cmd := &cobra.Command{
		Use:   "check",
		Short: "卡集完整性 + 已注册确定性检查器（发现问题时默认零退出码）",
		Long: `规范文档解决「怎么做」，check 解决「有没有做到」。

只跑能被程序稳定判断的检查：卡集完整性（规则 ID 唯一合法）与卡声明的
确定性检查器（当前注册表：gen-app-drift，即 gen app --check 的零差异门）。
check 通过不代表代码一定符合规范——它只是机械化的底线，代码评审不可替代。

退出码语义（onexai 口径）：默认发现问题时仍为 0，让 MCP 客户端拿到完整
结果；CI 拦截必须显式 --fail-on-findings。`,
		Args:        cobra.NoArgs,
		Annotations: readOnly("bald 规范确定性检查"),
		RunE: func(_ *cobra.Command, _ []string) error {
			res, err := runCheck(*o)
			if err != nil {
				return err
			}
			if printCheck(res, *o, streams) {
				// 与 CheckErr 的码 1 区分开不必要——这里就是「有 finding 且
				// 要求拦截」的唯一出口，直接退 1（叶子命令，无依赖 defer）。
				os.Exit(1)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&o.Dir, "dir", ".", "被检项目根（checker 的工作目录，如 appspec.json 所在目录）")
	cmd.Flags().StringVar(&o.MinLevel, "min-level", "mandatory", "最低检查级别：mandatory | recommended | reference")
	cmd.Flags().StringVar(&o.Format, "format", "text", "输出格式：text | json")
	cmd.Flags().BoolVar(&o.FailOnFindings, "fail-on-findings", false, "发现问题时以非零码退出（CI 拦截用；默认 0 以便 MCP 拿完整结果）")
	var rulesRaw string
	cmd.Flags().StringVar(&rulesRaw, "rules", "", "只跑绑定这些规则 ID 的 checker（逗号分隔，如 codegen-r03）")
	// --rules 在执行前切分进 opts.Rules（PreRunE 在 Validate/RunE 之前跑）。
	cmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		if rulesRaw != "" {
			o.Rules = strings.Split(rulesRaw, ",")
		}
		return nil
	}
	return cmd
}
