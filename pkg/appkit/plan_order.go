package appkit

import (
	"fmt"
	"sort"
	"strings"
)

// resolveNodeConflicts 对同 Key 节点做 I4 裁决：恰有一个 Override（且带
// Reason）者胜出，其余丢弃并记 override 诊断；无 Override 或都声明 Override
// 即编译错误。返回存活下标（保输入序）。
func resolveNodeConflicts(nodes []Node) ([]int, []Diagnostic, error) {
	groups := map[ResourceKey][]int{}
	var order []ResourceKey
	for i, n := range nodes {
		if n.Key == "" {
			return nil, nil, fmt.Errorf("appkit: compile: node with empty key (kind %q)", n.Kind)
		}
		if _, ok := groups[n.Key]; !ok {
			order = append(order, n.Key)
		}
		groups[n.Key] = append(groups[n.Key], i)
	}
	var (
		survivors []int
		warnings  []Diagnostic
	)
	for _, key := range order {
		idxs := groups[key]
		if len(idxs) == 1 {
			survivors = append(survivors, idxs[0])
			continue
		}
		var winners []int
		for _, i := range idxs {
			if nodes[i].Override {
				winners = append(winners, i)
			}
		}
		switch len(winners) {
		case 0:
			return nil, nil, fmt.Errorf("appkit: compile: duplicate node key %q (%s); declare exactly one of them with Override and a Reason to resolve",
				key, nodeLabels(nodes, idxs))
		case 1:
			w := winners[0]
			if nodes[w].Reason == "" {
				return nil, nil, fmt.Errorf("appkit: compile: node %q overrides %d others but declares no Reason", key, len(idxs)-1)
			}
			survivors = append(survivors, w)
			for _, i := range idxs {
				if i == w {
					continue
				}
				warnings = append(warnings, Diagnostic{
					Level:   DiagWarn,
					Code:    "override",
					Subject: string(key),
					Message: fmt.Sprintf("node %q from %s is overridden by %s (reason: %s)",
						key, nodeLabel(nodes[i]), nodeLabel(nodes[w]), nodes[w].Reason),
				})
			}
		default:
			return nil, nil, fmt.Errorf("appkit: compile: node key %q is declared with Override by %d nodes; at most one override winner is allowed", key, len(winners))
		}
	}
	return survivors, warnings, nil
}

// nodeLabel 给诊断一个稳定的节点标识（origin/kind）。
func nodeLabel(n Node) string { return string(n.Origin) + "/" + n.Kind }

// nodeLabels 汇总一组节点的标识（错误信息用）。
func nodeLabels(nodes []Node, idxs []int) string {
	labels := make([]string, 0, len(idxs))
	for _, i := range idxs {
		labels = append(labels, nodeLabel(nodes[i]))
	}
	return strings.Join(labels, ", ")
}

// orderNodes 是唯一的计划排序实现：I4 冲突裁决 → 阶段序 → 阶段内 Kahn 拓扑
// → ready 集合按 (Priority, Key) 选取。绝不使用声明下标（I2）。
func orderNodes(nodes []Node) ([]Node, []Diagnostic, error) {
	survivors, warnings, err := resolveNodeConflicts(nodes)
	if err != nil {
		return nil, nil, err
	}
	surv := make([]Node, 0, len(survivors))
	for _, i := range survivors {
		surv = append(surv, nodes[i])
	}
	ordered, err := topoOrder(surv)
	if err != nil {
		return nil, warnings, err
	}
	return ordered, warnings, nil
}

// topoOrder 对键唯一的节点做阶段序 + 阶段内 Kahn 拓扑。
func topoOrder(nodes []Node) ([]Node, error) {
	byKey := make(map[ResourceKey]Node, len(nodes))
	for _, n := range nodes {
		if n.Key == "" {
			return nil, fmt.Errorf("appkit: compile: node with empty key (kind %q)", n.Kind)
		}
		if prev, ok := byKey[n.Key]; ok {
			return nil, fmt.Errorf("appkit: compile: duplicate node key %q (kind %q and %q); domain and component names must be unique",
				n.Key, prev.Kind, n.Kind)
		}
		byKey[n.Key] = n
	}
	phases := []Phase{PhaseConfigure, PhaseBootstrap, PhaseWire, PhaseServe, PhaseDrain, PhaseTeardown}
	out := make([]Node, 0, len(nodes))
	for _, ph := range phases {
		group := make([]Node, 0, len(nodes))
		for _, n := range nodes {
			if n.Phase == ph {
				group = append(group, n)
			}
		}
		if len(group) == 0 {
			continue
		}
		ordered, err := topoPhase(group, byKey, ph)
		if err != nil {
			return nil, err
		}
		out = append(out, ordered...)
	}
	return out, nil
}

// topoPhase 对单阶段节点做 Kahn 拓扑；跨阶段依赖按「已应用」处理，指向未来
// 阶段的依赖是编译错误。ready 集合的次序为 (Priority, Key)。
func topoPhase(group []Node, all map[ResourceKey]Node, ph Phase) ([]Node, error) {
	inGroup := make(map[ResourceKey]bool, len(group))
	indeg := make(map[ResourceKey]int, len(group))
	dependents := make(map[ResourceKey][]ResourceKey, len(group))
	for _, n := range group {
		inGroup[n.Key] = true
		indeg[n.Key] = 0
	}
	for _, n := range group {
		for _, dep := range n.DependsOn {
			depNode, ok := all[dep]
			if !ok {
				return nil, fmt.Errorf("appkit: compile: node %q depends on unknown node %q", n.Key, dep)
			}
			if !inGroup[dep] {
				if depNode.Phase > ph {
					return nil, fmt.Errorf("appkit: compile: node %q (phase %s) depends on later-phase node %q (phase %s)",
						n.Key, ph, dep, depNode.Phase)
				}
				continue // 更早阶段已应用
			}
			indeg[n.Key]++
			dependents[dep] = append(dependents[dep], n.Key)
		}
	}
	sort.Slice(group, func(i, j int) bool {
		if group[i].Priority != group[j].Priority {
			return group[i].Priority < group[j].Priority
		}
		return group[i].Key < group[j].Key
	})
	out := make([]Node, 0, len(group))
	done := make(map[ResourceKey]bool, len(group))
	for len(out) < len(group) {
		picked := false
		for _, n := range group {
			if done[n.Key] || indeg[n.Key] != 0 {
				continue
			}
			out = append(out, n)
			done[n.Key] = true
			for _, dk := range dependents[n.Key] {
				indeg[dk]--
			}
			picked = true
			break
		}
		if !picked {
			var stuck []string
			for _, n := range group {
				if !done[n.Key] {
					stuck = append(stuck, string(n.Key))
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("appkit: compile: dependency cycle among nodes in phase %s: %s", ph, strings.Join(stuck, ", "))
		}
	}
	return out, nil
}
