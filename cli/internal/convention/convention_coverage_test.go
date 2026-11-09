package convention

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kalandramo/bald/cobramcp"
)

// TestFirstLine 覆盖 check  finding message 截断：多行取首行，单行原样。
func TestFirstLine(t *testing.T) {
	if got := firstLine("第一行\n第二行"); got != "第一行" {
		t.Errorf("multi-line: got %q, want 第一行", got)
	}
	if got := firstLine("只有一行"); got != "只有一行" {
		t.Errorf("single-line: got %q, want 只有一行", got)
	}
}

// TestToCardJSONNilSlices 校验 toCardJSON 的空切片归一化：Sources/Checks 为 nil
// 时投影成 [] 而非 null，保证 JSON 契约对结构化消费方稳定。
func TestToCardJSONNilSlices(t *testing.T) {
	out := toCardJSON(Card{ID: "demo", Title: "演示", Level: LevelMandatory,
		Category: categoryCore, Rules: []Rule{{ID: "demo-r01", Level: LevelMandatory, Text: "规则"}}})
	if out.Sources == nil || len(out.Sources) != 0 {
		t.Errorf("nil Sources must project to empty slice, got %#v", out.Sources)
	}
	if out.Checks == nil || len(out.Checks) != 0 {
		t.Errorf("nil Checks must project to empty slice, got %#v", out.Checks)
	}
	if len(out.Rules) != 1 || out.Rules[0].ID != "demo-r01" {
		t.Errorf("rules not projected: %#v", out.Rules)
	}
	// 非空 sources/checks 原样带出。
	full := toCardJSON(Card{ID: "demo", Sources: []string{"docs/a.md"}, Checks: []string{"gen-app-drift"}})
	if len(full.Sources) != 1 || len(full.Checks) != 1 {
		t.Errorf("non-empty sources/checks dropped: %#v %#v", full.Sources, full.Checks)
	}
}

// TestMarshalCard 校验 marshalCard 的 JSON 输出可被反序列化且带 id/rules 字段。
func TestMarshalCard(t *testing.T) {
	cards, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	raw, err := marshalCard(cards[0])
	if err != nil {
		t.Fatalf("marshalCard: %v", err)
	}
	var got cardJSON
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal marshalCard output: %v", err)
	}
	if got.ID != cards[0].ID || len(got.Rules) != len(cards[0].Rules) {
		t.Errorf("round-trip mismatch: got id=%q rules=%d, want id=%q rules=%d",
			got.ID, len(got.Rules), cards[0].ID, len(cards[0].Rules))
	}
}

// TestReadOnlyMap 校验 readOnly 生成 MCP 只读注解（键与值都对齐 cobramcp）。
func TestReadOnlyMap(t *testing.T) {
	m := readOnly("标题")
	if m[cobramcp.AnnotationReadOnly] != "true" {
		t.Errorf("readOnlyHint = %q, want true", m[cobramcp.AnnotationReadOnly])
	}
	if m[cobramcp.AnnotationTitle] != "标题" {
		t.Errorf("title = %q, want 标题", m[cobramcp.AnnotationTitle])
	}
}

// TestNewDefaultStreams 生产路径绑定的进程标准流非空。
func TestNewDefaultStreams(t *testing.T) {
	s := NewDefaultStreams()
	if s.In == nil || s.Out == nil || s.ErrOut == nil {
		t.Errorf("NewDefaultStreams returned nil stream(s): %#v", s)
	}
}

// drive 执行 NewCommand 树里的一个子命令（注入 buffer 流），返回 stdout/stderr 与错误。
func drive(t *testing.T, args ...string) (out, errOut string, err error) {
	t.Helper()
	var ob, eb bytes.Buffer
	root := NewCommand(Streams{In: strings.NewReader(""), Out: &ob, ErrOut: &eb})
	root.SetArgs(args)
	err = root.Execute()
	return ob.String(), eb.String(), err
}

// TestCommandTree 校验根命令装配了四个子命令，且 overview/list/get/check 的 RunE
// 都能用内嵌卡库跑通（测试 cwd 有 cards/，LoadAll(".") 稳定成功）。
func TestCommandTree(t *testing.T) {
	root := NewCommand(NewDefaultStreams())
	if root.Use != "convention" {
		t.Fatalf("root Use = %q, want convention", root.Use)
	}
	names := map[string]bool{}
	for _, c := range root.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"overview", "list", "get", "check"} {
		if !names[want] {
			t.Errorf("missing subcommand %q", want)
		}
	}

	// overview：索引全文进 stdout。
	out, _, err := drive(t, "overview")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if !strings.Contains(out, "bald convention overview") {
		t.Errorf("overview output missing header: %q", out)
	}

	// list：无过滤 + 按主体级别/类别过滤各跑一次。
	out, _, err = drive(t, "list")
	if err != nil || out == "" {
		t.Fatalf("list: out=%q err=%v", out, err)
	}
	if _, _, err := drive(t, "list", "--level", LevelMandatory); err != nil {
		t.Fatalf("list --level: %v", err)
	}
	if _, _, err := drive(t, "list", "--category", categoryCore); err != nil {
		t.Fatalf("list --category: %v", err)
	}

	// get：md（默认）取全文、json 取结构化、未命中报错、非法 format 报错。
	out, _, err = drive(t, "get", "judgments")
	if err != nil || !strings.Contains(out, "judgments-r01") {
		t.Fatalf("get md: out=%q err=%v", out, err)
	}
	out, _, err = drive(t, "get", "--format", "json", "catalog")
	if err != nil {
		t.Fatalf("get json: %v", err)
	}
	var got cardJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("get json not valid JSON (%q): %v", out, err)
	}
	if got.ID != "catalog" {
		t.Errorf("get json id = %q, want catalog", got.ID)
	}
	if _, _, err := drive(t, "get", "nonexistent"); err == nil {
		t.Errorf("get nonexistent should error")
	}
	if _, _, err := drive(t, "get", "--format", "xml", "judgments"); err == nil {
		t.Errorf("get bad format should error")
	}

	// check：text 与 json 各一次。cwd 无 appspec.json ⇒ gen-app-drift skip ⇒ 零
	// finding ⇒ 不触发 checkCmd 的 os.Exit(1)（fail 需 FailOnFindings && 有 finding）。
	out, _, err = drive(t, "check")
	if err != nil {
		t.Fatalf("check text: %v", err)
	}
	if !strings.Contains(out, "checked: cards integrity") {
		t.Errorf("check text output malformed: %q", out)
	}
	_, errOut, err := drive(t, "check", "--format", "json")
	if err != nil {
		t.Fatalf("check json: %v", err)
	}
	if !strings.Contains(errOut, "findings: 0") {
		t.Errorf("check json summary missing: %q", errOut)
	}
}
