package convention

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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

// stdlibLogCalls 是标准库 log 的直打调用名单。
var stdlibLogCalls = []string{
	"log.Print", "log.Println", "log.Printf", "log.Fatalf", "log.Fatal", "log.Fatalln",
	"log.Panicf", "log.Panic", "log.Panicln",
}

// slogCalls 是 log/slog 的直打调用名单。attr 构造（slog.String/Int/…）不在
// 名单——它们是传给契约层 log.ContextWithAttrs 的值构造，不是打日志。
var slogCalls = []string{"slog.Info(", "slog.Warn(", "slog.Error(", "slog.Debug(", "slog.Log("}

// loggingExemptPrefixes 是 logging-stdlib 的职责豁免目录：log/ 是契约层的
// 实现本体（import slog/log 是本职），cobramcp/ 是独立零依赖 CLI 库（不依赖
// bald/log，其日志面由自身治理）。
var loggingExemptPrefixes = []string{"log/", "cobramcp/"}

// checkLoggingStdlib 钉 logging-r01 的可判定面：业务代码的**日志调用**走
// bald/log 契约层（包级函数 log.Info/Warn/Error(ctx, msg, kv...)），标准库
// log 直打与 log/slog 直打是禁止的形态。检测是**调用级**而非 import 级——
// import slog 构造 Attr（slog.String 传给 log.ContextWithAttrs）不违规；
// import 而不调用（如类型引用）同样不报。职责豁免见 loggingExemptPrefixes。
// 项目里没有 Go 源文件时视为环境不可判（skip）。
func checkLoggingStdlib(dir string) ([]Finding, string, error) {
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
		for _, prefix := range loggingExemptPrefixes {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}
		src := string(raw)
		hasLog := importsOf(src, "log")
		hasSlog := importsOf(src, "log/slog")
		if !hasLog && !hasSlog {
			return nil
		}
		for i, line := range strings.Split(src, "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue
			}
			hit := ""
			if hasLog {
				for _, c := range stdlibLogCalls {
					if strings.Contains(line, c+"(") {
						hit = c
					}
				}
			}
			if hasSlog && hit == "" {
				for _, c := range slogCalls {
					if strings.Contains(line, c) {
						hit = strings.TrimSuffix(c, "(")
					}
				}
			}
			if hit == "" {
				continue
			}
			findings = append(findings, Finding{
				RuleID:     checkerRule["logging-stdlib"],
				Level:      LevelMandatory,
				File:       rel,
				Line:       i + 1,
				Message:    "日志直打 " + hit + "（logging-r01：打日志走 bald/log 契约层 log.Info/Warn/Error(ctx, msg, kv...)）",
				Suggestion: "改用 github.com/kalandramo/bald/log 的包级函数；标准库 log/slog 是 log 包内部适配面",
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

// importsOf 报告源文件是否 import 了给定包路径（单行 import 与 import 块都认）。
func importsOf(src, pkg string) bool {
	quoted := `"` + pkg + `"`
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if t == ")" {
				inBlock = false
			} else if strings.Contains(t, quoted) {
				return true
			}
			continue
		}
		if strings.HasPrefix(t, "import ") && strings.Contains(t, quoted) {
			return true
		}
	}
	return false
}

// checkNoPanic 钉 testing-r07 的运行期面：请求处理、中间件链与业务函数里的
// panic 让调用方的 error 流失效，且跨包恢复不可测试。AST 解析（字符串字面量
// 里的 "panic("——模板文本、文档建议——天然不误报），三类合法形态豁免：
//
//  1. Must 防御：panic("pkg: message")——构造期不变量断言，表达「程序性 bug
//     而非运行期错误」（Go 生态标准形态，如 regexp.MustCompile）；
//  2. fail-fast 转发：panic(标识符)——上游错误在装配/初始化期的主动终止
//     （J7「声明了却静默不生效是最坏结果」的实现手法）；
//  3. 构造期函数：New*/Must* 前缀命名的函数体。
//
// cmd/ 入口豁免（统一出口形态）。项目里没有 Go 源文件时 skip。
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
	fset := token.NewFileSet()
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
		f, err := parser.ParseFile(fset, path, raw, 0)
		if err != nil {
			return nil // 解析失败的非合法 Go 文件不判
		}
		scanned++
		rel, _ := filepath.Rel(root, path)
		if strings.HasPrefix(rel, "cobramcp/") {
			return nil // 独立零依赖 CLI 库，panic/日志面由自身治理（与 logging 豁免同源）
		}
		inCmdEntry := strings.HasPrefix(rel, "cmd/")
		curFn := ""
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.FuncDecl:
				curFn = node.Name.Name
			case *ast.CallExpr:
				fn, ok := node.Fun.(*ast.Ident)
				if !ok || fn.Name != "panic" || len(node.Args) != 1 {
					return true
				}
				if curFn != "" && (strings.HasPrefix(curFn, "New") || strings.HasPrefix(curFn, "Must")) {
					return true // 豁免 3：构造期函数
				}
				// 豁免 1：Must 防御——实参子树含 "pkg: message" 形态的字符串
				// 字面量（纯字面量或与上下文拼接）。
				hasMustMessage := false
				ast.Inspect(node.Args[0], func(a ast.Node) bool {
					if lit, ok := a.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(lit.Value, ": ") {
						hasMustMessage = true
					}
					return !hasMustMessage
				})
				if hasMustMessage {
					return true
				}
				if _, ok := node.Args[0].(*ast.Ident); ok {
					return true // 豁免 2：fail-fast 转发（panic(err)）
				}
				if inCmdEntry {
					return true
				}
				findings = append(findings, Finding{
					RuleID:     checkerRule["no-panic"],
					Level:      LevelMandatory,
					File:       rel,
					Line:       fset.Position(node.Pos()).Line,
					Message:    "运行期路径 panic——testing-r07：可预期失败返回 error（业务语义用 berrors 构造），panic 让调用方的 error 流失效",
					Suggestion: "返回 error 让调用方决定；构造期不变量用 Must 防御形态（panic(\"pkg: reason\"))，不可恢复故障在 cmd 入口统一退出",
				})
			}
			return true
		})
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
			// ran 而非 ok：OK 列表是「checker 执行成功」（可能带 finding）——
			// 是否违规只看 findings 计数，混用会让读者误判。
			fmt.Fprintln(streams.Out, "ran:", name)
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
