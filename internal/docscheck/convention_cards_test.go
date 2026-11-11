package docscheck

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 本文件守卫 bald 工程规范蒸馏卡（cli/internal/convention/cards/）的完整性：
// 卡是 CLI 内嵌的 AI 规范通道（onexai convention 的对应物），坏了不是显示问题，
// 是 AI 按错误规则写码。三条不变量：
//
//  1. 卡的 front-matter 与文件名一致（id == 文件名 stem），级别/类别取值合法；
//  2. sources 指向的母文档在仓库内真实存在（防卡与母文档解耦漂移）；
//  3. 规则 ID 全库唯一、格式 <卡id>-rNN 且前缀匹配卡 id；卡声明的 checker 在
//     注册表内，且绑定的规则真存在于该卡。
//
// 解析逻辑与 cli/internal/convention 的实现各自独立（根 module 不能 import cli
// module——那是零 replace 独立 module，install 语义决定的）。两份实现若有分歧，
// 本闸门按更严格的口径判红。
//
// checkerRuleRegistry 必须与 cli/internal/convention/check.go 的 checkerRule
// 保持一致：名字清单 + 绑定规则。改动任一侧时同步另一侧（本注释同时钉两边）。
var checkerRuleRegistry = map[string]string{
	"gen-app-drift":   "codegen-r03",
	"logging-stdlib":  "logging-r01",
	"prd-prompt-seam": "prd-r06",
	"prd-structure":   "prd-r03",
}

var (
	cardFMID       = regexp.MustCompile(`(?m)^id:\s*(\S+)\s*$`)
	cardFMLevel    = regexp.MustCompile(`(?m)^level:\s*(\S+)\s*$`)
	cardFMCategory = regexp.MustCompile(`(?m)^category:\s*(\S+)\s*$`)
	cardRuleLine   = regexp.MustCompile(`^-\s+\[([a-z]+(?:-[a-z]+)*-r[0-9]{2})\]\s+\[(强制|建议|参考)\]\s+\S`)
	cardLevels     = map[string]bool{"强制": true, "建议": true, "参考": true}
	cardCategories = map[string]bool{"core": true, "contract": true, "codegen": true, "ops": true}
)

// parseCardFrontMatter 解析单卡 front-matter（--- 围栏内），返回 sources 与
// checks 列表。列表归属由「当前打开的头」决定，与 cli/internal/convention
// 的解析语义一致。
func parseCardFrontMatter(t *testing.T, raw string) (sources, checks []string) {
	t.Helper()
	rest := strings.TrimLeft(raw, "\n")
	if !strings.HasPrefix(rest, "---\n") {
		return nil, nil
	}
	end := strings.Index(rest[4:], "\n---\n")
	if end < 0 {
		return nil, nil
	}
	open := ""
	for _, line := range strings.Split(rest[4:4+end], "\n") {
		if item, ok := strings.CutPrefix(line, "  - "); ok {
			switch open {
			case "sources":
				sources = append(sources, item)
			case "checks":
				checks = append(checks, item)
			}
			continue
		}
		key, _, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		open = ""
		switch strings.TrimSpace(key) {
		case "sources":
			open = "sources"
		case "checks":
			open = "checks"
		}
	}
	return sources, checks
}

// parseCardRules 解析单卡：返回规则 ID 清单（正文规则行）。
func parseCardRules(t *testing.T, raw string) (rules []string) {
	t.Helper()
	for _, line := range strings.Split(raw, "\n") {
		if m := cardRuleLine.FindStringSubmatch(line); m != nil {
			rules = append(rules, m[1])
		}
	}
	return rules
}

// TestConventionCards 守卫蒸馏卡的三条不变量（见文件头注释）。
func TestConventionCards(t *testing.T) {
	root := repoRoot(t)
	dir := filepath.Join(root, "cli", "internal", "convention", "cards")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read cards dir: %v", err)
	}
	seenRule := map[string]string{} // 规则 ID → 卡 id（全库唯一性）
	found := 0
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		found++
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read card %s: %v", name, err)
		}
		text := string(raw)

		stem := strings.TrimSuffix(name, ".md")
		m := cardFMID.FindStringSubmatch(text)
		if m == nil {
			t.Errorf("card %s: front-matter missing id", name)
		} else if m[1] != stem {
			t.Errorf("card %s: id %q must equal filename stem %q", name, m[1], stem)
		}
		if lm := cardFMLevel.FindStringSubmatch(text); lm == nil || !cardLevels[lm[1]] {
			t.Errorf("card %s: level must be one of 强制|建议|参考, got %v", name, lm)
		}
		if cm := cardFMCategory.FindStringSubmatch(text); cm == nil || !cardCategories[cm[1]] {
			t.Errorf("card %s: category must be one of core|codegen|ops, got %v", name, cm)
		}

		// sources：母文档必须真实存在（相对仓库根）。
		sources, checks := parseCardFrontMatter(t, text)
		for _, path := range sources {
			if _, err := os.Stat(filepath.Join(root, path)); err != nil {
				t.Errorf("card %s: source %q does not resolve in repo", name, path)
			}
		}

		// 规则 ID：格式、前缀、全库唯一。
		rules := parseCardRules(t, text)
		ruleSet := map[string]bool{}
		for _, id := range rules {
			if !strings.HasPrefix(id, stem+"-r") {
				t.Errorf("card %s: rule id %s does not use card prefix %s-rNN", name, id, stem)
			}
			if prev, dup := seenRule[id]; dup {
				t.Errorf("rule id %s defined in both %s and %s", id, prev, name)
			}
			seenRule[id] = name
			ruleSet[id] = true
		}
		if len(rules) == 0 {
			t.Errorf("card %s: no rule lines found", name)
		}

		// checks 声明：名字在注册表内，绑定的规则真存在于本卡。
		for _, c := range checks {
			bound, known := checkerRuleRegistry[c]
			if !known {
				t.Errorf("card %s: unknown checker %q (registry: %v)", name, c, registryKeys())
				continue
			}
			if !ruleSet[bound] {
				t.Errorf("card %s: checker %q binds rule %s which is not in this card", name, c, bound)
			}
		}
	}
	if found < 6 {
		t.Fatalf("expected at least 6 convention cards, found %d", found)
	}
}

func registryKeys() []string {
	keys := make([]string, 0, len(checkerRuleRegistry))
	for k := range checkerRuleRegistry {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
