package appkit

import (
	"fmt"
	"sort"
	"strings"
)

// overrideWarnings 把「同一段路径被多次声明、末者胜」的隐式覆盖显式化。
// 段级覆盖仅产生告警（不改变末者胜）；节点级同 Key 冲突走 resolveNodeConflicts
// （Override + Reason 裁决），本函数让隐式段覆盖在计划里同样可见。
func overrideWarnings(d *Decl) []Diagnostic {
	owners := map[string][]int{}
	for i, p := range d.Providers {
		for _, s := range p.Segments() {
			owners[s] = append(owners[s], i)
		}
	}
	paths := make([]string, 0, len(owners))
	for path, idxs := range owners {
		if len(idxs) > 1 {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var out []Diagnostic
	for _, path := range paths {
		idxs := owners[path]
		winner := d.Providers[idxs[len(idxs)-1]]
		for _, li := range idxs[:len(idxs)-1] {
			out = append(out, Diagnostic{
				Level:   DiagWarn,
				Code:    "override",
				Subject: path,
				Message: fmt.Sprintf("segment %q is declared by %T and overridden by %T (last declaration wins)",
					path, d.Providers[li], winner),
			})
		}
	}
	return out
}

// whitelistWarnings 把 WithUnimplementedSegments 的逃逸舱做成计划警告。
func whitelistWarnings(pp *providerPlan) []Diagnostic {
	names := make([]string, 0, len(pp.unimplemented))
	for name := range pp.unimplemented {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]Diagnostic, 0, len(names))
	for _, name := range names {
		out = append(out, Diagnostic{
			Level:   DiagWarn,
			Code:    "whitelisted",
			Subject: name,
			Message: fmt.Sprintf("segment %q has no consumer and is acknowledged via WithUnimplementedSegments", name),
		})
	}
	return out
}

// opaqueBudgetWarnings 把「Opaque 节点数超预算」显式化（I10：逃逸舱不隐身）。
// budget<=0 表示未声明预算：不告警，但 Plan.OpaqueCount 仍供 CI 设门。
func opaqueBudgetWarnings(budget int, nodes []Node) []Diagnostic {
	if budget <= 0 {
		return nil
	}
	var keys []string
	for _, n := range nodes {
		if n.Opaque {
			keys = append(keys, string(n.Key))
		}
	}
	if len(keys) <= budget {
		return nil
	}
	sort.Strings(keys)
	return []Diagnostic{{
		Level:   DiagWarn,
		Code:    "opaque-budget",
		Subject: "opaque",
		Message: fmt.Sprintf("%d opaque node(s) exceed the budget %d: %s", len(keys), budget, strings.Join(keys, ", ")),
	}}
}

// sortAndDedupWarnings 让警告集合与声明序无关（I2 的哈希稳定性）。
func sortAndDedupWarnings(in []Diagnostic) []Diagnostic {
	if len(in) == 0 {
		return nil
	}
	sort.Slice(in, func(i, j int) bool {
		if in[i].Code != in[j].Code {
			return in[i].Code < in[j].Code
		}
		if in[i].Subject != in[j].Subject {
			return in[i].Subject < in[j].Subject
		}
		return in[i].Message < in[j].Message
	})
	out := in[:1]
	for _, w := range in[1:] {
		last := out[len(out)-1]
		if w.Level == last.Level && w.Code == last.Code && w.Subject == last.Subject && w.Message == last.Message {
			continue
		}
		out = append(out, w)
	}
	return out
}
