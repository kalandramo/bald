// teardown.go 推导停机序（I5）：Apply 逆序 + TeardownAfter 约束。
//
// 声明式回归：停机顺序不再是引擎手工表（旧 cleanupOrder），而是由节点数据
// 推出——基础语义是「最后应用的资源最先释放」（栈式），节点可用
// TeardownAfter 把自身推迟到指定节点之后（["*"] = 全部其它节点）。审计
// 后端用 ["*"] 排到最后，让 stream 尾批事件在指标/span flush 之后收集；
// 指标/追踪用 ["logger"] 系列保持「先停暴露端、再 flush、最后恢复日志」。
package appkit

import (
	"fmt"
	"sort"
	"strings"
)

// teardownOrder 返回节点的停机序（先执行者在前）。nodes 须键唯一。
//
// 边语义：node 在 TeardownAfter 列出的每个节点之后释放。未知键视为已满足
// （跨阶段/框架外资源）；"*" 展开为全部其它节点。同层候选按 Apply 逆序
// 选取（后应用者先释放），即输入切片下标从大到小——这是 I5 的基础语义。
func teardownOrder(nodes []Node) ([]ResourceKey, error) {
	index := make(map[ResourceKey]int, len(nodes))
	for i, n := range nodes {
		if _, ok := index[n.Key]; ok {
			return nil, fmt.Errorf("appkit: teardown: duplicate node key %q", n.Key)
		}
		index[n.Key] = i
	}
	indeg := make(map[ResourceKey]int, len(nodes))
	dependents := make(map[ResourceKey][]ResourceKey, len(nodes))
	for _, n := range nodes {
		seen := make(map[ResourceKey]bool)
		for _, dep := range n.TeardownAfter {
			var deps []ResourceKey
			if dep == "*" {
				for _, other := range nodes {
					if other.Key != n.Key {
						deps = append(deps, other.Key)
					}
				}
			} else if _, ok := index[dep]; ok {
				deps = []ResourceKey{dep}
			} else {
				continue // 未知键：框架外资源，视为已满足
			}
			for _, d := range deps {
				if seen[d] {
					continue
				}
				seen[d] = true
				indeg[n.Key]++
				dependents[d] = append(dependents[d], n.Key)
			}
		}
		if _, ok := indeg[n.Key]; !ok {
			indeg[n.Key] = 0
		}
	}

	out := make([]ResourceKey, 0, len(nodes))
	done := make(map[ResourceKey]bool, len(nodes))
	for len(out) < len(nodes) {
		pick := -1
		for i, n := range nodes {
			if done[n.Key] || indeg[n.Key] != 0 {
				continue
			}
			if pick < 0 || i > pick {
				pick = i // Apply 逆序：下标最大者先释放
			}
		}
		if pick < 0 {
			var stuck []string
			for _, n := range nodes {
				if !done[n.Key] {
					stuck = append(stuck, string(n.Key))
				}
			}
			sort.Strings(stuck)
			return nil, fmt.Errorf("appkit: teardown: TeardownAfter cycle among nodes: %s", strings.Join(stuck, ", "))
		}
		key := nodes[pick].Key
		out = append(out, key)
		done[key] = true
		for _, dep := range dependents[key] {
			indeg[dep]--
		}
	}
	return out, nil
}
