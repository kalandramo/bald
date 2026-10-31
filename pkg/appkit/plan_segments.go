package appkit

import (
	"sort"
	"strings"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

// buildSegmentUses 汇总每个出现段/白名单段的消费节点（I3 的检查面）。
// Framework=true 表示框架无条件消费（app/logger 默认通道、预填 server 段），
// 没有用户可见节点。
func buildSegmentUses(cfg *bootstrapv1.BootstrapConfig, d *Decl, pp *providerPlan, nodes []Node) []SegmentUse {
	exact := map[string][]ResourceKey{}
	for _, n := range nodes {
		for _, s := range n.Segments {
			exact[s] = append(exact[s], n.Key)
		}
	}
	covered := consumedSegments(cfg, d, pp)
	byPath := map[string]SegmentUse{}
	add := func(su SegmentUse) {
		if su.Consumers != nil {
			su.Consumers = sortedUniqueKeys(su.Consumers)
		}
		byPath[su.Path] = su
	}

	for _, top := range presentTopLevelSegments(cfg) {
		cons := topConsumers(exact, top)
		wl := pp.unimplemented[top]
		add(SegmentUse{
			Path:        top,
			Consumers:   cons,
			Whitelisted: wl,
			Framework:   !wl && len(cons) == 0 && covered[top],
		})
	}
	if srv := cfg.GetServer(); srv != nil {
		serverFramework := byPath["server"].Framework
		for _, child := range presentChildSegments(srv.ProtoReflect()) {
			path := "server." + child
			if _, ok := byPath[path]; ok {
				continue
			}
			cons := sortedUniqueKeys(exact[path])
			wl := pp.unimplemented[path] || pp.unimplemented["server"]
			add(SegmentUse{
				Path:        path,
				Consumers:   cons,
				Whitelisted: wl,
				Framework:   builtinServerSegments[child] && !wl && len(cons) == 0 && serverFramework,
			})
		}
	}
	// 白名单段即使未出现在契约里也要可见（逃逸舱不隐身）。
	for name := range pp.unimplemented {
		if _, ok := byPath[name]; ok {
			continue
		}
		add(SegmentUse{Path: name, Whitelisted: true})
	}
	// 业务配置段（biz.<path>）：不在契约段集合里，但节点确实读取——同样进
	// 汇总（有消费者），否则 segments 视图对 Q8 节点说谎。
	for path, keys := range exact {
		if path != "biz" && !strings.HasPrefix(path, "biz.") {
			continue
		}
		if _, ok := byPath[path]; ok {
			continue
		}
		add(SegmentUse{Path: path, Consumers: keys})
	}

	out := make([]SegmentUse, 0, len(byPath))
	for _, su := range byPath {
		out = append(out, su)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// topConsumers 汇总顶层段及其全部子段的消费节点（读取子段即读取顶层段）。
func topConsumers(exact map[string][]ResourceKey, top string) []ResourceKey {
	var out []ResourceKey
	prefix := top + "."
	for path, keys := range exact {
		if path != top && !strings.HasPrefix(path, prefix) {
			continue
		}
		out = append(out, keys...)
	}
	return sortedUniqueKeys(out)
}

// sortedUniqueKeys 排序去重资源键。
func sortedUniqueKeys(in []ResourceKey) []ResourceKey {
	if len(in) == 0 {
		return nil
	}
	out := append([]ResourceKey(nil), in...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	dup := out[:1]
	for _, k := range out[1:] {
		if k != dup[len(dup)-1] {
			dup = append(dup, k)
		}
	}
	return dup
}
