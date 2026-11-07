// Package convention 是 bald 工程规范的蒸馏卡层（onexai convention 的 bald 对应物）。
//
// 分层纪律：docs/devel 的设计文档是论证与细节的真相源（母文档），本包的蒸馏卡是
// 机器消费的界面——`bald convention overview` 先给 AI 索引，`bald convention get`
// 按需取卡全文，`bald convention check` 跑卡声明的确定性检查器。卡以 embed.FS
// 内嵌于 cli module（CLI 是零 replace 独立 module，embed 只能嵌本 module 文件），
// 母文档留在仓库 docs/ 下，卡与母文档的防漂移由根 module 的 internal/docscheck
// 闸门校验（来源链接可解析、规则 ID 唯一合法）。
//
// 卡格式（front-matter 手写解析，不引重型依赖）：
//
//	---
//	id: judgments
//	title: 设计判据（J1–J9）
//	level: 强制            ← 卡的主体级别（用于索引过滤；级别是规则级的，见下）
//	category: core         ← core | codegen | ops
//	sources:               ← 母文档（相对仓库根），docscheck 闸门校验可解析
//	  - docs/devel/zh-CN/设计判据.md
//	checks:                ← 声明的确定性 checker（必须存在于本包注册表）
//	  - gen-app-drift
//	---
//	（body：规则清单，每条带规则 ID 与级别前缀）
//	- [judgments-r01] [强制] 框架能力零调用不是删除依据……
//
// 规则 ID 形如 <卡id>-rNN：评审意见、--rules 过滤、check 的 JSON finding 三处
// 共用（onexai convention 第 13 章的机制：规则 ID 不是装饰）。级别是规则级的，
// 一张卡可同时含强制/建议/参考条目；front-matter 的 level 只记主体级别。
package convention

import (
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

//go:embed cards/*.md
var cardsFS embed.FS

// 卡的主体级别与类别（枚举值与 overview 的分组一一对应）。
const (
	LevelMandatory   = "强制"
	LevelRecommended = "建议"
	LevelReference   = "参考"

	categoryCore    = "core"
	categoryCodegen = "codegen"
	categoryOps     = "ops"
)

// Rule 是卡内一条规则。Line 是卡文件内的 1-based 行号，check 的 finding 用它定位。
type Rule struct {
	ID    string // 形如 judgments-r01
	Level string // 强制 | 建议 | 参考
	Text  string
	Line  int
}

// Card 是一张蒸馏卡。
type Card struct {
	ID       string
	Title    string
	Level    string   // 主体级别（front-matter）
	Category string   // core | codegen | ops
	Sources  []string // 母文档路径（相对仓库根）
	Checks   []string // 声明的确定性 checker 名
	Body     string   // front-matter 之后的原文（get 的 md 输出）
	Rules    []Rule
	File     string // cards/ 内文件名（finding 的 file 用它）
}

// ruleRe 匹配规则行：- [judgments-r01] [强制] 规则正文……
var ruleRe = regexp.MustCompile(`^-\s+\[([a-z]+-r[0-9]{2})\]\s+\[(强制|建议|参考)\]\s+(.+)$`)

// Load 解析内嵌卡库并做集合级完整性校验（卡内规则 ID 前缀、全库唯一、级别合法）。
// 完整性失败直接报错：卡集本身坏了属于闸门级故障，不是 finding。
func Load() ([]Card, error) { return LoadFS(cardsFS) }

// LoadFS 从任意 fs.FS 解析卡库（cards/*.md 的根即 fsys 根），测试注入用。
func LoadFS(fsys fs.FS) ([]Card, error) {
	entries, err := fs.ReadDir(fsys, "cards")
	if err != nil {
		return nil, fmt.Errorf("read cards dir: %w", err)
	}
	var cards []Card
	seenRule := map[string]string{} // 规则 ID → 卡 ID（全库唯一性）
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		raw, err := fs.ReadFile(fsys, "cards/"+name)
		if err != nil {
			return nil, fmt.Errorf("read card %s: %w", name, err)
		}
		card, err := parseCard(name, string(raw))
		if err != nil {
			return nil, fmt.Errorf("card %s: %w", name, err)
		}
		for _, r := range card.Rules {
			if prev, dup := seenRule[r.ID]; dup {
				return nil, fmt.Errorf("card %s: rule id %s already defined in card %s", name, r.ID, prev)
			}
			seenRule[r.ID] = card.ID
		}
		cards = append(cards, *card)
	}
	if len(cards) == 0 {
		return nil, fmt.Errorf("no cards found under cards/")
	}
	sortCards(cards)
	return cards, nil
}

// parseCard 解析单卡：front-matter（--- 围栏，key: value + 两空格缩进的列表项）
// 与 body 规则行。front-matter 之后的所有原文进 Body（get 的输出保真）。
func parseCard(name, raw string) (*Card, error) {
	rest := strings.TrimLeft(raw, "\n")
	if !strings.HasPrefix(rest, "---\n") {
		return nil, fmt.Errorf("missing front-matter (must start with ---)")
	}
	end := strings.Index(rest[4:], "\n---\n")
	if end < 0 {
		return nil, fmt.Errorf("front-matter not closed (expected ---)")
	}
	fmBody := rest[4 : 4+end]
	body := rest[4+end+len("\n---\n"):]

	card := &Card{File: "cards/" + name}
	var open string // 当前打开的列表头（sources | checks | ""），列表项归属它
	for _, line := range strings.Split(fmBody, "\n") {
		if item, ok := strings.CutPrefix(line, "  - "); ok {
			switch open {
			case "sources":
				card.Sources = append(card.Sources, item)
				continue
			case "checks":
				card.Checks = append(card.Checks, item)
				continue
			default:
				return nil, fmt.Errorf("list item %q without a list header", item)
			}
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue // 空行
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		open = ""
		switch key {
		case "id":
			card.ID = value
		case "title":
			card.Title = value
		case "level":
			card.Level = value
		case "category":
			card.Category = value
		case "sources":
			open = "sources"
		case "checks":
			open = "checks"
		}
	}

	if err := card.validate(); err != nil {
		return nil, err
	}
	card.Body = strings.TrimLeft(body, "\n")
	if err := card.parseRules(); err != nil {
		return nil, err
	}
	return card, nil
}

// validate 校验 front-matter 标量字段与枚举值。
func (c *Card) validate() error {
	stem := strings.TrimSuffix(filepath.Base(c.File), ".md")
	if c.ID == "" {
		return fmt.Errorf("front-matter missing id")
	}
	if c.ID != stem {
		return fmt.Errorf("id %q must equal filename stem %q", c.ID, stem)
	}
	if c.Title == "" {
		return fmt.Errorf("front-matter missing title")
	}
	switch c.Level {
	case LevelMandatory, LevelRecommended, LevelReference:
	default:
		return fmt.Errorf("level %q must be one of 强制|建议|参考", c.Level)
	}
	switch c.Category {
	case categoryCore, categoryCodegen, categoryOps:
	default:
		return fmt.Errorf("category %q must be one of core|codegen|ops", c.Category)
	}
	return nil
}

// parseRules 从 body 提取规则行。每条规则 ID 必须以卡 ID 为前缀。
func (c *Card) parseRules() error {
	for i, line := range strings.Split(c.Body, "\n") {
		m := ruleRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if !strings.HasPrefix(m[1], c.ID+"-r") {
			return fmt.Errorf("rule id %s does not use card prefix %s-rNN", m[1], c.ID)
		}
		c.Rules = append(c.Rules, Rule{ID: m[1], Level: m[2], Text: m[3], Line: i + 1})
	}
	if len(c.Rules) == 0 {
		return fmt.Errorf("body has no rules (expected `- [id-rNN] [级别] text` lines)")
	}
	return nil
}

// categoryRank 决定 overview 与 list 的展示顺序：核心原则 → 生成与门禁 → 运维。
func categoryRank(c string) int {
	switch c {
	case categoryCore:
		return 0
	case categoryCodegen:
		return 1
	default:
		return 2
	}
}

func sortCards(cards []Card) {
	sort.Slice(cards, func(i, j int) bool {
		if categoryRank(cards[i].Category) != categoryRank(cards[j].Category) {
			return categoryRank(cards[i].Category) < categoryRank(cards[j].Category)
		}
		return cards[i].ID < cards[j].ID
	})
}
