package convention

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/kalandramo/bald/cli/internal/codegen"
)

// Finding 是 check 的一条结论（JSON 契约对齐 onexai convention check：
// rule_id / level / file / line / message / suggestion）。Exit-code 语义也
// 对齐：默认发现问题时退出码仍为 0（MCP 客户端要拿完整结果），CI 拦截必须
// 显式 --fail-on-findings。
type Finding struct {
	RuleID     string `json:"rule_id"`
	Level      string `json:"level"`
	File       string `json:"file"`
	Line       int    `json:"line"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

// checker 是一个确定性检查器：dir 是被检项目根。skip 非空表示本环境不可判
// （如目录里没有 appspec.json），不算 finding 也不算错误。
type checker func(dir string) (findings []Finding, skip string, err error)

// checkerRule 记录 checker 绑定的规则 ID（finding 的 rule_id 来源）。绑定是
// 显式的：卡声明 checks 时 docscheck 闸门校验「名字在注册表内、且绑定的规则
// 真存在于该卡」，卡与注册表漂移时 CI 红。
var checkerRule = map[string]string{
	"gen-app-drift": "codegen-r03",
}

// registry 是确定性检查器注册表。Phase 1 只收 gen-app-drift（薄封装 gen app
// --check 这道既有零差异门）；其余卡规则暂无 checker（卡里以级别语义约束，
// 靠评审落实），后续按需收编。
var registry = map[string]checker{
	"gen-app-drift": checkGenAppDrift,
}

// checkGenAppDrift 复用 gen app 的零差异门（checkTree）：spec 不存在视为
// 环境不可判（skip），有差异产出一条绑定 codegen-r03 的 finding。诊断流走
// io.Discard——差异明细已随 error 进入 finding 的 message。
func checkGenAppDrift(dir string) ([]Finding, string, error) {
	spec := filepath.Join(dir, "appspec.json")
	if _, err := os.Stat(spec); err != nil {
		if os.IsNotExist(err) {
			return nil, "no appspec.json in " + dir, nil
		}
		return nil, "", fmt.Errorf("stat %s: %w", spec, err)
	}
	if err := codegen.CheckAppSpec(spec, dir, io.Discard, io.Discard); err != nil {
		return []Finding{{
			RuleID:     checkerRule["gen-app-drift"],
			Level:      LevelMandatory,
			File:       "appspec.json",
			Message:    "生成树与 AppSpec 漂移：" + firstLine(err.Error()),
			Suggestion: "运行 `bald gen app --check` 查看逐文件差异；确认声明无误后用 --adopt 重新生成 owned 文件",
		}}, "", nil
	}
	return nil, "", nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// levelRank 把级别映射为可比较的序（强制 0 < 建议 1 < 参考 2）。
func levelRank(level string) int {
	switch level {
	case LevelMandatory:
		return 0
	case LevelRecommended:
		return 1
	default:
		return 2
	}
}

// minLevelFlag 是 --min-level 的合法取值（onexai 口径：默认只检强制）。
var minLevelFlag = map[string]string{
	"mandatory":   LevelMandatory,
	"recommended": LevelRecommended,
	"reference":   LevelReference,
}

// checkOptions 是 check 的领域配置。
type checkOptions struct {
	Dir            string   // --dir：被检项目根（checker 的工作目录）
	Rules          []string // --rules：只跑绑定这些规则 ID 的 checker
	MinLevel       string   // --min-level：mandatory|recommended|reference（flag 值）
	Format         string   // --format：text | json
	FailOnFindings bool     // --fail-on-findings：有 finding 时非零退出（CI 拦截用）
}

// checkResult 是一次 check 的汇总（命令层据此打印与决定退出码）。
type checkResult struct {
	Cards   int
	Rules   int
	OK      []string
	Skipped []string
	Found   []Finding
}

// runCheck 执行卡集完整性校验（错误即失败，不是 finding）与注册表内 checker。
func runCheck(opts checkOptions) (*checkResult, error) {
	cards, err := Load()
	if err != nil {
		return nil, err // 卡集本身坏了：闸门级故障，直接红
	}
	res := &checkResult{Cards: len(cards)}
	for _, c := range cards {
		res.Rules += len(c.Rules)
	}

	onlyRules := map[string]bool{}
	for _, r := range opts.Rules {
		onlyRules[strings.TrimSpace(r)] = true
	}
	minLevel, ok := minLevelFlag[opts.MinLevel]
	if !ok {
		return nil, fmt.Errorf("--min-level %q must be one of mandatory|recommended|reference", opts.MinLevel)
	}

	// 卡 → 声明的 checker（遍历按 Load 的稳定顺序）。
	for _, c := range cards {
		for _, name := range c.Checks {
			bound, ok := checkerRule[name]
			if !ok {
				return nil, fmt.Errorf("card %s declares unknown checker %q (known: gen-app-drift)", c.ID, name)
			}
			if len(onlyRules) > 0 && !onlyRules[bound] {
				res.Skipped = append(res.Skipped, name+" (--rules filter)")
				continue
			}
			if levelRank(c.Level) > levelRank(minLevel) {
				res.Skipped = append(res.Skipped, name+" (--min-level "+opts.MinLevel+")")
				continue
			}
			findings, skip, err := registry[name](opts.Dir)
			if err != nil {
				return nil, fmt.Errorf("checker %s: %w", name, err)
			}
			if skip != "" {
				res.Skipped = append(res.Skipped, name+" ("+skip+")")
				continue
			}
			res.Found = append(res.Found, findings...)
			res.OK = append(res.OK, name)
		}
	}
	return res, nil
}

// printCheck 按 format 输出结果。text：全部进 Out；json：findings 数组进 Out
// （结构化消费方只看 stdout），状态摘要进 ErrOut。返回是否应以非零码退出。
func printCheck(res *checkResult, opts checkOptions, streams Streams) (fail bool) {
	if opts.Format == "json" {
		found := res.Found
		if found == nil {
			found = []Finding{}
		}
		raw, err := json.MarshalIndent(found, "", "  ")
		if err == nil {
			fmt.Fprintln(streams.Out, string(raw))
		}
		fmt.Fprintf(streams.ErrOut, "cards: %d rules: %d ok: %v skipped: %v findings: %d\n",
			res.Cards, res.Rules, res.OK, res.Skipped, len(res.Found))
	} else {
		fmt.Fprintf(streams.Out, "checked: cards integrity (%d cards, %d rules)\n", res.Cards, res.Rules)
		for _, name := range res.OK {
			fmt.Fprintln(streams.Out, "ok:", name)
		}
		for _, s := range res.Skipped {
			fmt.Fprintln(streams.Out, "skip:", s)
		}
		for _, f := range res.Found {
			fmt.Fprintf(streams.Out, "%s:%d: [%s] %s\n", f.File, f.Line, f.RuleID, f.Message)
			fmt.Fprintf(streams.Out, "  suggestion: %s\n", f.Suggestion)
		}
		fmt.Fprintf(streams.Out, "findings: %d\n", len(res.Found))
	}
	return opts.FailOnFindings && len(res.Found) > 0
}
