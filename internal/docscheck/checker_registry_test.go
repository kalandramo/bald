package docscheck

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 本文件把「docscheck 的镜像注册表」与「cli 侧真实注册表」的同步从注释约定
// 变成机器判定。
//
// 为什么需要这份重复：根 module 不能 import cli module（cli 是零 replace 的
// 独立 module，install 语义决定的），所以 docscheck 不得不镜像一份
// checkerRule。镜像的固有风险是漂移——cli 侧加了 checker 而这边没跟上，卡声明
// 会在闸门里被误判为「未知 checker」；反过来镜像里留着一个已不存在的 checker
// 则会让卡声明假装被机器守住了。TestCheckerRuleRegistryMatchesCLI 直接解析
// check.go 的 map 字面量逐项对账，两个方向都判。

// cliCheckerRulePath 是 cli 侧注册表的唯一真相源（相对仓库根）。
var cliCheckerRulePath = filepath.Join("cli", "internal", "convention", "check.go")

// checkerRuleAnchor 是 map 字面量的起始锚点。
const checkerRuleAnchor = "var checkerRule = map[string]string{"

// checkerRuleEntryRe 匹配 map 字面量里的一条 "name": "rule-id"。
var checkerRuleEntryRe = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]+)"`)

// errAnchorMissing 表示 check.go 的结构变了、锚点找不到（重构时同步本文件）。
var errAnchorMissing = errors.New(checkerRuleAnchor + " not found in " + cliCheckerRulePath)

// TestCheckerRuleRegistryMatchesCLI 双向对账镜像注册表与 cli 的 checkerRule。
func TestCheckerRuleRegistryMatchesCLI(t *testing.T) {
	root := repoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, cliCheckerRulePath))
	if err != nil {
		t.Fatalf("read %s: %v", cliCheckerRulePath, err)
	}
	parsed, err := parseCheckerRuleMap(string(raw))
	if err != nil {
		t.Fatalf("parse checkerRule: %v", err)
	}
	// G1 的收口线：检查器族只许增不许减，防止「覆盖率悄悄退步」。
	if len(parsed) < 17 {
		t.Fatalf("checkerRule has %d entries, want >= 17", len(parsed))
	}

	for name, rule := range parsed {
		mirrored, ok := checkerRuleRegistry[name]
		if !ok {
			t.Errorf("cli 注册表有 checker %q（%s），docscheck 镜像缺它——同步 checkerRuleRegistry", name, rule)
			continue
		}
		if mirrored != rule {
			t.Errorf("checker %q 绑定的规则不一致：cli=%s docscheck=%s", name, rule, mirrored)
		}
	}
	for name := range checkerRuleRegistry {
		if _, ok := parsed[name]; !ok {
			t.Errorf("docscheck 镜像有 checker %q，cli 注册表已无——镜像过期", name)
		}
	}
}

// parseCheckerRuleMap 抽出 check.go 里 checkerRule 的 map 字面量并逐条解析。
// 行注释先剥掉：注释里出现的引号不该被当成条目。
func parseCheckerRuleMap(src string) (map[string]string, error) {
	start := strings.Index(src, checkerRuleAnchor)
	if start < 0 {
		return nil, errAnchorMissing
	}
	rest := src[start+len(checkerRuleAnchor):]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		return nil, errAnchorMissing
	}
	out := map[string]string{}
	for _, line := range strings.Split(rest[:end], "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		for _, m := range checkerRuleEntryRe.FindAllStringSubmatch(line, -1) {
			out[m[1]] = m[2]
		}
	}
	if len(out) == 0 {
		return nil, errAnchorMissing
	}
	return out, nil
}
