package appkit

import (
	"context"
	"strings"
	"testing"
)

// bizDomain 声明一个使用 `biz.<section>` 段的可协调节点（Q8）。
func bizDomain(name string, segs ...string) Domain {
	return Domain{
		Name: name,
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{
				Key:      ResourceKey("domain." + name + ".policy"),
				Segments: segs,
				Phase:    PhaseWire,
				Reload:   ReloadReconcile,
			}}, nil
		},
		ApplyNode: func(context.Context, *App, Node) error { return nil },
		Reconcile: func(context.Context, *App, Node) error { return nil },
	}
}

func desiredNode(t *testing.T, p *Plan, key ResourceKey) Node {
	t.Helper()
	for _, n := range p.Nodes {
		if n.Key == key {
			return n
		}
	}
	t.Fatalf("node %q missing from plan", key)
	return Node{}
}

// TestPlan_BizSegmentDesiredState 钉住 Q8：`biz.<path>` 段把业务配置带进
// 可协调节点的期望态——同一快照稳定同摘要，配置变化换摘要，段缺失与有值可区分。
func TestPlan_BizSegmentDesiredState(t *testing.T) {
	key := ResourceKey("domain.audit.policy")
	compile := func(t *testing.T, extra string) *Plan {
		t.Helper()
		p, err := Compile(context.Background(),
			ConfigFile(writePlanConfig(t, "biz-segment", extra)),
			bizDomain("audit", "biz.audit"))
		if err != nil {
			t.Fatalf("Compile: %v", err)
		}
		return p
	}

	pa := compile(t, "biz:\n  audit:\n    policy: \"a\"\n")
	na := desiredNode(t, pa, key)
	if na.Reload != ReloadReconcile || na.DesiredHash == "" {
		t.Fatalf("biz reconcile node = %+v, want reconcile with desired hash", na)
	}

	// 同一快照双跑：摘要与计划哈希都稳定（I2）。
	pb := compile(t, "biz:\n  audit:\n    policy: \"a\"\n")
	if nb := desiredNode(t, pb, key); nb.DesiredHash != na.DesiredHash {
		t.Fatalf("same settings produced different desired hash: %s vs %s", na.DesiredHash, nb.DesiredHash)
	}
	if pa.Hash() != pb.Hash() {
		t.Fatalf("same settings produced different plan hash")
	}

	// 业务配置变化 → 摘要变化 → 计划哈希变化（热更按 Key diff 的输入）。
	pc := compile(t, "biz:\n  audit:\n    policy: \"b\"\n")
	if nc := desiredNode(t, pc, key); nc.DesiredHash == na.DesiredHash {
		t.Fatalf("changed biz settings kept the same desired hash: %s", nc.DesiredHash)
	}
	if pa.Hash() == pc.Hash() {
		t.Fatalf("changed biz settings kept the same plan hash")
	}

	// 段缺失与有值必须可区分（否则删除配置不会触发协调）。
	pd := compile(t, "")
	if nd := desiredNode(t, pd, key); nd.DesiredHash == "" || nd.DesiredHash == na.DesiredHash {
		t.Fatalf("absent biz section must hash distinctly: %+v", nd)
	}
}

// TestPlan_BizSegmentValidation 钉住 biz 段的结构校验：`biz.<path>` 合法，
// 空的 / 含空段的路径在计划期报错，不能被当作「消费了一个不存在的段」。
func TestPlan_BizSegmentValidation(t *testing.T) {
	cfgPath := writePlanConfig(t, "biz-segment-validation", "biz:\n  audit:\n    policy: \"a\"\n")

	if _, err := Compile(context.Background(), ConfigFile(cfgPath), bizDomain("ok", "biz.audit.policy")); err != nil {
		t.Fatalf("Compile rejected valid biz segment: %v", err)
	}

	for _, seg := range []string{"biz.", "biz..audit", "biz.audit.", "bizx.audit"} {
		t.Run(seg, func(t *testing.T) {
			_, err := Compile(context.Background(), ConfigFile(cfgPath), bizDomain("bad", seg))
			if err == nil {
				t.Fatalf("Compile accepted malformed segment %q", seg)
			}
			if !strings.Contains(err.Error(), "unknown segment path") {
				t.Fatalf("error = %v, want unknown segment path", err)
			}
		})
	}
}

// TestNodeDesiredHash_SettingsDeterminism 钉住 Settings 子树的确定性序列化：
// map 构造顺序不影响摘要，桶内容变化才影响。
func TestNodeDesiredHash_SettingsDeterminism(t *testing.T) {
	n := Node{
		Key:      "domain.audit.policy",
		Kind:     "domain",
		Segments: []string{"biz.audit"},
		Reload:   ReloadReconcile,
	}
	first := map[string]any{"biz": map[string]any{"audit": map[string]any{
		"policy": "a",
		"limits": []any{1, "x"},
	}}}
	second := map[string]any{"biz": map[string]any{"audit": map[string]any{
		"limits": []any{1, "x"},
		"policy": "a",
	}}}
	if h1, h2 := nodeDesiredHash(nil, first, n), nodeDesiredHash(nil, second, n); h1 != h2 {
		t.Fatalf("map insertion order changed the desired hash: %s vs %s", h1, h2)
	}

	third := map[string]any{"biz": map[string]any{"audit": map[string]any{"policy": "b"}}}
	if nodeDesiredHash(nil, third, n) == nodeDesiredHash(nil, first, n) {
		t.Fatal("changed subtree value kept the same desired hash")
	}
}
