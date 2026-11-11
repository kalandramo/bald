package convention

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

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
	"gen-app-drift":   "codegen-r03",
	"logging-stdlib":  "logging-r01",
	"no-panic":        "testing-r07",
	"prd-prompt-seam": "prd-r06",
	"prd-structure":   "prd-r03",
}

// registry 是确定性检查器注册表。Phase 1 只收 gen-app-drift（薄封装 gen app
// --check 这道既有零差异门）；其余卡规则暂无 checker（卡里以级别语义约束，
// 靠评审落实），后续按需收编。
var registry = map[string]checker{
	"gen-app-drift":   checkGenAppDrift,
	"logging-stdlib":  checkLoggingStdlib,
	"no-panic":        checkNoPanic,
	"prd-prompt-seam": checkPRDPromptSeam,
	"prd-structure":   checkPRDStructure,
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
			Suggestion: "运行 `bald gen app --check` 查看逐文件差异；确认声明无误后用 --adopt 重新生成 owned 文件（owned 清单见 appspec.manifest.json；误改的生成物可 git checkout 还原）",
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

// checkLoggingStdlib 钉 logging-r01 的可判定面：业务代码只 import bald/log
// 契约层，标准库 log 与 log/slog 直用是被禁止的形态（bald 的 slog 适配面在
// log 包内部）。递归扫描非测试 Go 源文件，vendor/.git/工具目录跳过。项目里
// 没有 Go 源文件时视为环境不可判（skip）。
func checkLoggingStdlib(dir string) ([]Finding, string, error) {
	root := dir
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		// 不存在与「dir 本身是文件」（ENOTDIR）都视为环境不可判。
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, "no go.mod in " + dir, nil
		}
		return nil, "", fmt.Errorf("stat %s: %w", dir, err)
	}
	var findings []Finding
	var scanned int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目不可读不阻断整体检查
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case "vendor", ".git", "node_modules", "_example", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		scanned++
		for _, bad := range []string{`"log"`, `"log/slog"`} {
			if importBlockContains(string(raw), bad) {
				rel, _ := filepath.Rel(root, path)
				findings = append(findings, Finding{
					RuleID:     checkerRule["logging-stdlib"],
					Level:      LevelMandatory,
					File:       rel,
					Message:    "业务代码直接 import " + bad + "（logging-r01：只 import bald/log 契约层）",
					Suggestion: "改用 github.com/kalandramo/bald/log（包级函数 log.Info/Warn/Error(ctx, msg, kv...)）；标准库 slog 是 log 包内部适配面",
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("walk %s: %w", dir, err)
	}
	if scanned == 0 {
		return nil, "no go source files in " + dir, nil
	}
	return findings, "", nil
}

// importBlockContains 判定源文件是否 import 了给定包路径：快筛引号字面量，
// 再确认它出现在 import 块或单行 import 里（避免误伤注释与字符串常量里的
// "log" 字样）。
func importBlockContains(src, pkg string) bool {
	if !strings.Contains(src, pkg) {
		return false
	}
	for i, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		isImport := strings.HasPrefix(trimmed, "import") || trimmed == ")" ||
			(strings.HasPrefix(trimmed, "\"") && inImportBlock(src, i))
		if isImport && strings.HasSuffix(trimmed, pkg) {
			return true
		}
	}
	return false
}

// inImportBlock 判定第 i 行是否处于 import ( ... ) 块内（块头在之前的行）。
func inImportBlock(src string, i int) bool {
	lines := strings.Split(src, "\n")
	for j := i - 1; j >= 0 && j > i-50; j-- {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, "import (") {
			return true
		}
		if strings.HasPrefix(t, "import ") || t == ")" {
			return false
		}
	}
	return false
}

// checkNoPanic 钉 testing-r07：库代码（非 cmd/ 入口、非 _test.go）禁止 panic
// ——可预期失败返回 error，panic 让调用方的 error 流失效且跨包恢复不可测试。
// 扫描口径与 checkLoggingStdlib 同款（vendor/.git/testdata 跳过）；cmd/ 下的
// main 入口允许 CheckErr/os.Exit 形态的终态处理。
func checkNoPanic(dir string) ([]Finding, string, error) {
	root := dir
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, "no go.mod in " + dir, nil
		}
		return nil, "", fmt.Errorf("stat %s: %w", dir, err)
	}
	var findings []Finding
	var scanned int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			switch name {
			case "vendor", ".git", "node_modules", "_example", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		inCmdEntry := strings.HasPrefix(rel, "cmd/")
		for i, line := range strings.Split(string(raw), "\n") {
			call := panicCall(line)
			if call == "" || inCmdEntry {
				continue
			}
			findings = append(findings, Finding{
				RuleID:     checkerRule["no-panic"],
				Level:      LevelMandatory,
				File:       rel,
				Line:       i + 1,
				Message:    "库代码 panic(" + call + ")——testing-r07：可预期失败返回 error（业务语义用 berrors），不可恢复故障在入口统一处理",
				Suggestion: "返回 error 让调用方决定；确实不可恢复时在 cmd 入口统一退出",
			})
		}
		return nil
	})
	if err != nil {
		return nil, "", fmt.Errorf("walk %s: %w", dir, err)
	}
	if scanned == 0 {
		return nil, "no go source files in " + dir, nil
	}
	return findings, "", nil
}

// panicCall 提取一行里的 panic( 调用实参首段；不是 panic 调用返回空。
func panicCall(line string) string {
	idx := strings.Index(line, "panic(")
	if idx < 0 {
		return ""
	}
	// 注释里的 panic( 不算调用。
	if c := strings.Index(line[:idx], "//"); c >= 0 {
		return ""
	}
	return strings.TrimSpace(line[idx+len("panic("):])
}

// prdDir 是 PRD 工作流产物的约定目录（prompt = 需求描述，prd = 翻译产物）。
const prdDir = "docs/prd"

// listPRDFiles 返回 dir 下指定后缀的文件名（稳定排序）。目录不存在视为
// 环境不可判（skip）——没有 PRD 工作流的项目不判这张卡。
func listPRDFiles(dir, suffix string) ([]string, string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, prdDir))
	if err != nil {
		// 不存在与「dir 本身是文件」（ENOTDIR）都视为环境不可判——与
		// gen-app-drift 对 appspec 缺失的 skip 语义同款：没有 PRD 工作流的
		// 项目不判这张卡。
		if os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR) {
			return nil, "no " + prdDir + " in " + dir, nil
		}
		return nil, "", fmt.Errorf("readdir %s: %w", prdDir, err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			names = append(names, e.Name())
		}
	}
	return names, "", nil
}

// checkPRDPromptSeam 钉 prd-r06 的接缝：需求描述（*.prompt.md）尾部必须带
// 「先查规范。」——不查规范直接翻译 = 产物脱离工程既有契约面。
func checkPRDPromptSeam(dir string) ([]Finding, string, error) {
	names, skip, err := listPRDFiles(dir, ".prompt.md")
	if err != nil || skip != "" {
		return nil, skip, err
	}
	var findings []Finding
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, prdDir, name))
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", name, err)
		}
		if !strings.Contains(string(raw), "先查规范") {
			findings = append(findings, Finding{
				RuleID:     checkerRule["prd-prompt-seam"],
				Level:      LevelMandatory,
				File:       prdDir + "/" + name,
				Message:    "需求描述缺「先查规范」接缝：翻译前必须先查 convention",
				Suggestion: "在 prompt 尾部补一行「先查规范。」，或运行 bald ide init 接入会话引导物",
			})
		}
	}
	return findings, "", nil
}

// checkPRDStructure 钉 prd-r03 的四要素：翻译产物（*.prd.md）的每个用户故事
// 必须带 Status/Priority/Description/Acceptance Criteria；结构骨架（一级标题、
// Introduction）由 prd-r01 以卡语义约束、评审落实，这里只收可确定性判定的部分。
func checkPRDStructure(dir string) ([]Finding, string, error) {
	names, skip, err := listPRDFiles(dir, ".prd.md")
	if err != nil || skip != "" {
		return nil, skip, err
	}
	fields := []string{"**Status:**", "**Priority:**", "**Description:**", "**Acceptance Criteria:**"}
	var findings []Finding
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, prdDir, name))
		if err != nil {
			return nil, "", fmt.Errorf("read %s: %w", name, err)
		}
		// 按 "### US-" 切故事节：骨架外的散段不判（可能是标题与概述）。
		for _, sec := range strings.Split(string(raw), "### US-")[1:] {
			missing := missingFields(sec, fields)
			if len(missing) > 0 {
				findings = append(findings, Finding{
					RuleID:     checkerRule["prd-structure"],
					Level:      LevelMandatory,
					File:       prdDir + "/" + name,
					Message:    "用户故事缺四要素：" + strings.Join(missing, "、"),
					Suggestion: "补齐 Status/Priority/Description/Acceptance Criteria 四要素（prd-r03）",
				})
			}
		}
	}
	return findings, "", nil
}

// missingFields 返回段落里缺失的字段标记。
func missingFields(section string, fields []string) []string {
	var missing []string
	for _, f := range fields {
		if !strings.Contains(section, f) {
			missing = append(missing, f)
		}
	}
	return missing
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
