package contract

// mapping_offline_test.go —— instrumentation.prometheus 三行映射的运行期闸门。
//
// 判据形状：每行都判**落在 Wire 上的指标全名**，不判构造是否成功。
// client_golang 的 Registry.Gather() 是纯内存观测面，一条真后端都不需要：
// namespace/subsystem 决定族名拼接，use_default_registry 决定族出现在**哪个
// Gatherer** 里。摘掉任一行，红的是名字或归属，不是错误。
//
// 名称唯一性（seq）是这条腿能在同进程复跑（-count=2）的前提：
// use_default_registry=true 会把指标注册进**进程全局**的
// prometheus.DefaultRegisterer，重名注册会让 promauto 直接 panic。

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	clientprom "github.com/prometheus/client_golang/prometheus"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
)

var seq atomic.Uint64

// uniqName 返回本次调用独有的指标名。
func uniqName(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, seq.Add(1))
}

// gatherFamilies 取一个 Gatherer 上的全部族名集合。
func gatherFamilies(t *testing.T, g clientprom.Gatherer) map[string]bool {
	t.Helper()
	families, err := g.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	out := make(map[string]bool, len(families))
	for _, f := range families {
		out[f.GetName()] = true
	}
	return out
}

// TestMappingOffline_PrefixRowsReachWire 判 namespace 与 subsystem 两行：
// 四原语的族名都必须是 "<ns>_<sub>_<name>"。
//
// 分辨力：摘 namespace ⇒ 实际名 "gatesub_x"；摘 subsystem ⇒ "gatens_x"；
// 两行互换 ⇒ "gatesub_gatens_x"。三种破坏都撞在同一条相等断言上，但**红的
// 字符串各不相同**，所以事后分得清是哪一行漂了。
func TestMappingOffline_PrefixRowsReachWire(t *testing.T) {
	const (
		ns  = "gatens"
		sub = "gatesub"
	)

	p, cleanup, err := Provider(&bootstrapv1.Instrumentation_Prometheus{
		Namespace: ns,
		Subsystem: sub,
	})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	if cleanup != nil {
		t.Fatal("cleanup 应为 nil：client_golang 的 Registry 没有可关闭的东西")
	}

	ctx := context.Background()
	names := map[string]string{
		"Counter":   uniqName("gate_counter"),
		"Histogram": uniqName("gate_hist"),
		"Gauge":     uniqName("gate_gauge"),
		"GaugeAdd":  uniqName("gate_gadd"),
	}
	// 四原语共用同一份 cfg，前缀拼接发生在各自的 Opts 里——逐条判精确族名才能
	// 抓到「只有 Counter 那条读了 namespace」这种半截实现。
	p.Counter(ctx, names["Counter"], 1, nil)
	p.Histogram(ctx, names["Histogram"], 0.5, nil)
	p.Gauge(ctx, names["Gauge"], 2, nil)
	p.GaugeAdd(ctx, names["GaugeAdd"], 3, nil)

	have := gatherFamilies(t, p.Registry())
	for method, name := range names {
		full := ns + "_" + sub + "_" + name
		if !have[full] {
			t.Fatalf("%s 族名缺 %q（实际应拼 <namespace>_<subsystem>_<name>）；已注册族 %v", method, full, keysOf(have))
		}
	}
	// 裸名不得同时出现——那意味着某条构造走的是无prefix路径而不是 cfg。
	for _, name := range names {
		if have[name] {
			t.Fatalf("族 %q 以裸名出现：前缀没盖住这一条", name)
		}
	}
}

// TestMappingOffline_UseDefaultRegistryChoosesGatherer 判
// use_default_registry 行：true 时族出现在进程默认 Gatherer，false 时只出现在
// 实例私有 Registry——落点轴，与「族名对不对」正交。
func TestMappingOffline_UseDefaultRegistryChoosesGatherer(t *testing.T) {
	ctx := context.Background()

	t.Run("true 落默认注册表", func(t *testing.T) {
		name := uniqName("gate_default")
		p, _, err := Provider(&bootstrapv1.Instrumentation_Prometheus{UseDefaultRegistry: true})
		if err != nil {
			t.Fatalf("Provider: %v", err)
		}
		p.Counter(ctx, name, 1, nil)

		if !gatherFamilies(t, clientprom.DefaultGatherer)[name] {
			t.Fatalf("指标 %q 未出现在 prometheus.DefaultGatherer（use_default_registry=true 应落默认注册表）", name)
		}
		if p.Registry() != clientprom.DefaultGatherer {
			t.Fatal("Registry() 未回指默认 Gatherer：cfg.registry 没接进 DefaultRegisterer")
		}
	})

	t.Run("false 落私有 Registry", func(t *testing.T) {
		name := uniqName("gate_private")
		p, _, err := Provider(&bootstrapv1.Instrumentation_Prometheus{})
		if err != nil {
			t.Fatalf("Provider: %v", err)
		}
		p.Counter(ctx, name, 1, nil)

		if gatherFamilies(t, p.Registry())[name] == false {
			t.Fatalf("私有 Registry 里没有 %q", name)
		}
		if gatherFamilies(t, clientprom.DefaultGatherer)[name] {
			t.Fatalf("指标 %q 漏进了默认注册表（use_default_registry=false 应隔离）", name)
		}
		if p.Registry() == clientprom.DefaultGatherer {
			t.Fatal("Registry() 指向默认 Gatherer：私有 Registry 没生效")
		}
	})
}

// TestMappingOffline_LabelsReachVec 判「埋点通路的标签维度」：带标签的四原语
// 必须在族里留下对应的 label 维度，否则 WithMetrics 注进去也只统计出一个数。
func TestMappingOffline_LabelsReachVec(t *testing.T) {
	name := uniqName("gate_labeled")
	p, _, err := Provider(&bootstrapv1.Instrumentation_Prometheus{Namespace: "gatelabel"})
	if err != nil {
		t.Fatalf("Provider: %v", err)
	}
	ctx := context.Background()
	p.Counter(ctx, name, 1, map[string]string{"status": "ok"})
	p.Counter(ctx, name, 1, map[string]string{"status": "err"})

	families, err := p.Registry().Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	full := "gatelabel_" + name
	var (
		found  bool
		series int
		seen   = map[string]bool{}
	)
	for _, f := range families {
		if f.GetName() != full {
			continue
		}
		found = true
		series = len(f.GetMetric())
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "status" {
					seen[l.GetValue()] = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("族 %q 缺席（已注册族 %v）", full, keysOf(gatherFamilies(t, p.Registry())))
	}
	if series != 2 {
		t.Fatalf("族 %q 的序列数 = %d, want 2（两个 status 取值各一条）", full, series)
	}
	if !seen["ok"] || !seen["err"] {
		t.Fatalf("status 维度 = %v, want 同时含 ok 与 err", seen)
	}
}

// TestProvider_RejectsNilSection 判构造期那一道：子段为 nil 必须报错，
// 不能产出一个「全默认」的半成品实例。
func TestProvider_RejectsNilSection(t *testing.T) {
	if _, _, err := Provider(nil); err == nil {
		t.Fatal("Provider(nil) 应报错")
	}
}

func keysOf(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}
