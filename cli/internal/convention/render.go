package convention

import (
	"encoding/json"
	"fmt"
	"strings"
)

// renderOverview 渲染总览（AI 友好 GFM，onexai convention overview 的 bald 版）：
// 索引 + 明确的取用方式。上下文成本原则——AI 先读总览再按任务取卡，不全量喂入。
func renderOverview(cards []Card) string {
	var b strings.Builder
	b.WriteString("# bald convention overview\n\n")
	b.WriteString("每张卡都可作为规范单元获取：MCP 工具名 `bald_convention_get`（入参 id=卡 id），")
	b.WriteString("或等价 CLI `bald convention get <卡id>`。\n")
	b.WriteString("> 先看本总览，按当前任务取对应卡；**写代码前先查规范**。\n\n")
	b.WriteString("## Cards by Category (按类别)\n")
	cur := ""
	for _, c := range cards {
		if c.Category != cur {
			cur = c.Category
			b.WriteString("\n### " + categoryTitle(cur) + " (" + cur + ")\n\n")
			b.WriteString("| 卡 | 标题 | 主体级别 | 规则级别分布 |\n|---|---|---|---|\n")
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.ID, c.Title, c.Level, levelCounts(c))
	}
	b.WriteString("\n## Rule Levels (规则级别)\n\n")
	b.WriteString("| 级别 | 语义 |\n|---|---|\n")
	b.WriteString("| [强制] | 违反即门禁红或评审必打回，豁免需正式评审；存量分期改进，新代码自设计期强制。 |\n")
	b.WriteString("| [建议] | 默认遵守，偏离需在评审中给出正当理由；存量渐进采纳。 |\n")
	b.WriteString("| [参考] | 按项目情境采纳。 |\n\n")
	b.WriteString("check 只覆盖可确定性判断的规则：它通过不代表代码一定符合规范，代码评审不可替代。\n")
	return b.String()
}

// levelCounts 统计一张卡的规则级别分布，如「强制 9」或「强制 5 · 建议 1」。
func levelCounts(c Card) string {
	order := []string{LevelMandatory, LevelRecommended, LevelReference}
	var parts []string
	for _, lv := range order {
		n := 0
		for _, r := range c.Rules {
			if r.Level == lv {
				n++
			}
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d", lv, n))
		}
	}
	return strings.Join(parts, " · ")
}

// categoryTitle 是类别的人类可读名（overview 分组标题用）。
func categoryTitle(c string) string {
	switch c {
	case categoryCore:
		return "核心原则"
	case categoryContract:
		return "契约与编码"
	case categoryCodegen:
		return "生成与门禁"
	default:
		return "运维与发布"
	}
}

// ruleJSON 是 get --format json 与 check finding 共用的规则投影。
type ruleJSON struct {
	ID    string `json:"id"`
	Level string `json:"level"`
	Text  string `json:"text"`
}

// cardJSON 是 get --format json 的输出契约（配 cobramcp 的 outputJSONHint 注解，
// 结构化结果直出 structuredContent.data，LLM 不必二次解析文本）。
type cardJSON struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Level    string     `json:"level"`
	Category string     `json:"category"`
	Sources  []string   `json:"sources"`
	Checks   []string   `json:"checks"`
	Rules    []ruleJSON `json:"rules"`
	Body     string     `json:"body"`
}

func toCardJSON(c Card) cardJSON {
	out := cardJSON{
		ID: c.ID, Title: c.Title, Level: c.Level, Category: c.Category,
		Sources: c.Sources, Checks: c.Checks, Body: c.Body,
	}
	if out.Sources == nil {
		out.Sources = []string{}
	}
	if out.Checks == nil {
		out.Checks = []string{}
	}
	out.Rules = make([]ruleJSON, 0, len(c.Rules))
	for _, r := range c.Rules {
		out.Rules = append(out.Rules, ruleJSON{ID: r.ID, Level: r.Level, Text: r.Text})
	}
	return out
}

func marshalCard(c Card) ([]byte, error) {
	return json.MarshalIndent(toCardJSON(c), "", "  ")
}

// getCard 按 id 取卡（未命中返回带全部候选的错误，方便 AI 自纠）。
func getCard(cards []Card, id string) (*Card, error) {
	for i := range cards {
		if cards[i].ID == id {
			return &cards[i], nil
		}
	}
	ids := make([]string, 0, len(cards))
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	return nil, fmt.Errorf("unknown card %q (available: %s)", id, strings.Join(ids, ", "))
}
