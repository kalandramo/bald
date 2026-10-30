// doctor.go 实现设计 §4.9 的 doctor 报告：把装配计划的逃逸舱（Opaque 节点、
// 白名单段、被覆盖的段、警告）与 build tag 分叉集中成一份可读诊断。
//
// doctor 只读：编译计划（Compile 同一路径，不触发 Provider.Build、钩子或
// 阶段 A logger），不写文件、不联网。生成的清单校验属代码生成器职责——
// 运行期二进制不 import 生成器，报告末尾给出 `bald gen --check` 的出口提示。
package appkit

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
)

// Doctor 编译装配计划并向 stdout 输出诊断报告（纯读取，可安全用于运维探针与 CI）。
// 声明了 WithPlanCache 时，报告追加一段与基线的只读对比（缓存不可读只提示，不报错）。
func Doctor(ctx context.Context, opts ...Option) error {
	d := newDecl(opts...)
	d.ConfigFile = parseConfigFlag(d.ConfigFile)
	p, err := compileDecl(ctx, d)
	if err != nil {
		return err
	}
	writeDoctor(os.Stdout, p, BuildTags(), comparePlanCache(d.PlanCache, p), newDomainReport(d.Domains))
	return nil
}

// domainReport 汇总业务域的声明式迁移进度（设计 §4.6 的量化口径）：声明了
// Declare 的域进入 declared（拓扑可检视），其余整块是 legacy Opaque。名称
// 排序保证同一装配字节稳定。
type domainReport struct {
	declared []string
	legacy   []string
}

func newDomainReport(domains []Domain) domainReport {
	var r domainReport
	for _, d := range domains {
		if d.Declare != nil {
			r.declared = append(r.declared, d.Name)
			continue
		}
		r.legacy = append(r.legacy, d.Name)
	}
	sort.Strings(r.declared)
	sort.Strings(r.legacy)
	return r
}

// write 渲染域迁移进度（无业务域时整体省略）。
func (r domainReport) write(w io.Writer) {
	total := len(r.declared) + len(r.legacy)
	if total == 0 {
		return
	}
	fmt.Fprintf(w, "\ndomains: %d (declared %d, legacy %d)\n", total, len(r.declared), len(r.legacy))
	printSegments(w, "declared", r.declared)
	printSegments(w, "legacy", r.legacy)
}

// writeDoctor 渲染 doctor 报告。所有清单按字典序输出，保证同一计划字节稳定。
func writeDoctor(w io.Writer, p *Plan, tags []string, cache *planCacheBaseline, domains domainReport) {
	fmt.Fprintln(w, "bald doctor")
	fmt.Fprintf(w, "app: %s %s (env=%s)\n", p.App.Name, p.App.Version, p.App.Env)
	if p.Role != "" {
		fmt.Fprintf(w, "role: %s\n", p.Role)
	}
	fmt.Fprintf(w, "input: %s\n", p.InputHash)
	fmt.Fprintf(w, "plan:  %s\n", p.Hash())
	writePlanCacheSection(w, cache, p)

	// 节点：阶段分布 + 逃逸舱清单（Opaque 节点逐个列名，I10 不隐身）。
	fmt.Fprintf(w, "\nnodes: %d (opaque %d)\n", len(p.Nodes), p.OpaqueCount)
	phases := map[string]int{}
	var opaque []string
	var overrides []Node
	for _, n := range p.Nodes {
		phases[n.Phase.String()]++
		if n.Opaque {
			opaque = append(opaque, string(n.Key))
		}
		if n.Override {
			overrides = append(overrides, n)
		}
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	names := make([]string, 0, len(phases))
	for name := range phases {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(tw, "  phase %s\t%d\n", name, phases[name])
	}
	tw.Flush()
	sort.Strings(opaque)
	if len(opaque) > 0 {
		fmt.Fprintf(w, "  opaque: %s\n", strings.Join(opaque, ", "))
	}
	sort.Slice(overrides, func(i, j int) bool { return overrides[i].Key < overrides[j].Key })
	for _, n := range overrides {
		fmt.Fprintf(w, "  override: %s (%s)\n", n.Key, n.Reason)
	}
	if len(p.Imperative) > 0 {
		// 命令式槽位可见化：闭包不进节点拓扑，但 doctor 必须如实列出。
		fmt.Fprintf(w, "  imperative: %d\n", len(p.Imperative))
		for _, it := range p.Imperative {
			name := it.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(w, "    %s: %s\n", it.Kind, name)
		}
	}
	if len(p.Routes) > 0 {
		// 路由清单来自域声明（Domain.RouteTable），按计划序打印。
		fmt.Fprintf(w, "  routes: %d\n", len(p.Routes))
		for _, r := range p.Routes {
			rpc := ""
			if r.RPC != "" {
				rpc = " -> " + r.RPC
			}
			fmt.Fprintf(w, "    %s %-6s %s%s\n", r.Domain, r.Method, r.Path, rpc)
		}
	}
	domains.write(w)

	// 段：未消费、白名单、框架自持、有消费者的段各自成组。
	var unconsumed, whitelisted, framework, consumed []string
	for _, s := range p.Segments {
		switch {
		case len(s.Consumers) > 0:
			consumed = append(consumed, s.Path)
		case s.Whitelisted:
			whitelisted = append(whitelisted, s.Path)
		case s.Framework:
			framework = append(framework, s.Path)
		default:
			unconsumed = append(unconsumed, s.Path)
		}
	}
	fmt.Fprintf(w, "\nsegments: %d (consumed %d, whitelisted %d, framework %d, unconsumed %d)\n",
		len(p.Segments), len(consumed), len(whitelisted), len(framework), len(unconsumed))
	printSegments(w, "whitelisted", whitelisted)
	printSegments(w, "framework", framework)
	printSegments(w, "unconsumed", unconsumed)
	if len(unconsumed) > 0 {
		fmt.Fprintln(w, "  hint: unconsumed segments fail startup (L3); provide a Provider or whitelist explicitly")
	}

	if len(tags) > 0 {
		fmt.Fprintf(w, "\nbuild tags: %s\n", strings.Join(tags, ","))
	} else {
		fmt.Fprintln(w, "\nbuild tags: (none)")
	}

	fmt.Fprintf(w, "\nwarnings: %d\n", len(p.Warnings))
	for _, warn := range p.Warnings {
		subject := ""
		if warn.Subject != "" {
			subject = " [" + warn.Subject + "]"
		}
		fmt.Fprintf(w, "  %s %s%s: %s\n", warn.Level, warn.Code, subject, warn.Message)
	}

	fmt.Fprintln(w, "\ngen: run `bald gen app --check` and `bald gen domain --check` in the generated tree")
}

// writePlanCacheSection 渲染缓存基线对比：一行摘要 + 节点级差异（+ 新增 /
// - 移除 / ~ 期望态或依赖变化）。无缓存声明时整体省略。
func writePlanCacheSection(w io.Writer, b *planCacheBaseline, p *Plan) {
	if b == nil {
		return
	}
	switch {
	case b.Err != nil:
		fmt.Fprintf(w, "cache: %s (unavailable: %v)\n", b.Path, b.Err)
		return
	case b.Entry == nil:
		if p.Role == "" {
			fmt.Fprintf(w, "cache: %s (no baseline yet)\n", b.Path)
		} else {
			fmt.Fprintf(w, "cache: %s (no baseline for role %q)\n", b.Path, p.Role)
		}
		return
	}
	fmt.Fprintf(w, "cache: %s (baseline %s, recorded %s)\n",
		b.Path, hashPrefix(b.Entry.PlanHash), b.Entry.RecordedAt)
	if b.InputChanged {
		fmt.Fprintf(w, "  input changed since baseline: %s -> %s\n",
			hashPrefix(b.Entry.InputHash), hashPrefix(p.InputHash))
	}
	if !b.PlanChanged && b.Change.Empty() {
		// 限定判据：这里只断言装配声明（节点 + 命令式槽位）未变，不断言
		// Plan.Hash 相等——告警/段视图变化不进此判据（上方 plan: 行给出
		// 当前整份计划哈希，需要严格比对时以它为准）。诊断差异另行列出。
		fmt.Fprintln(w, "  plan unchanged: nodes + imperative slots + routes")
		if len(b.Diagnostics) == 0 {
			return
		}
	}
	if b.PlanChanged {
		if b.Change.Empty() && len(b.Routes) == 0 {
			// 装配哈希变了但节点/槽位/路由身份对比为空：变化只在节点属性
			// （priority/phase/segments/opaque 等）。如实报出，不谎报 unchanged，
			// 也不假装能逐项列出。路由已有自己的差异段，不再冒充属性变化。
			fmt.Fprintln(w, "  plan changed: node attributes (priority/phase/segments)")
		}
		if len(b.Change.Changes) > 0 {
			fmt.Fprintf(w, "  plan changed: %d node(s)\n", len(b.Change.Changes))
			for _, ch := range b.Change.Changes {
				switch {
				case ch.Added:
					fmt.Fprintf(w, "    + %s (%s, %s)\n", ch.Key, ch.Kind, ch.Policy)
				case ch.Removed:
					fmt.Fprintf(w, "    - %s (%s, %s)\n", ch.Key, ch.Kind, ch.Policy)
				default:
					var what []string
					if ch.From != ch.To {
						what = append(what, "desired state")
					}
					if ch.DepsChanged {
						what = append(what, "dependencies")
					}
					fmt.Fprintf(w, "    ~ %s (%s, %s): %s\n", ch.Key, ch.Kind, ch.Policy, strings.Join(what, " + "))
				}
			}
		}
		if len(b.Change.Imperative) > 0 {
			// 命令式槽位（effect/keyWatch/reconcile/hook 等）不进节点拓扑，但闭包
			// 增删同样改变运行期行为——计划缓存要回答「装配变了什么」，就逐条列出。
			fmt.Fprintf(w, "  imperative slots changed: %d\n", len(b.Change.Imperative))
			for _, ic := range b.Change.Imperative {
				mark := "+"
				if ic.Removed {
					mark = "-"
				}
				name := ic.Name
				if name == "" {
					name = "-"
				}
				fmt.Fprintf(w, "    %s %s: %s\n", mark, ic.Kind, name)
			}
		}
		if len(b.Routes) > 0 {
			// 路由清单（Domain.RouteTable / 生成物 routes.<Name>）是声明面的
			// 一部分：加一条或换一条路径都改变运行期 REST 面，逐条列出。
			fmt.Fprintf(w, "  routes changed: %d\n", len(b.Routes))
			for _, rc := range b.Routes {
				mark := "+"
				if rc.Removed {
					mark = "-"
				}
				rpc := ""
				if rc.RPC != "" {
					rpc = " -> " + rc.RPC
				}
				fmt.Fprintf(w, "    %s %s %s %s%s\n", mark, rc.Domain, rc.Method, rc.Path, rpc)
			}
		}
	}
	if len(b.Diagnostics) > 0 {
		// 诊断（ambient 接线、覆盖裁决等）不进拓扑，但同样是声明变化：与节点
		// 差异分开列，避免把「告警变了」读成「装配变了」。
		fmt.Fprintf(w, "  diagnostics changed: %d\n", len(b.Diagnostics))
		for _, dc := range b.Diagnostics {
			mark := "+"
			if dc.Removed {
				mark = "-"
			}
			subject := ""
			if dc.Subject != "" {
				subject = " (" + dc.Subject + ")"
			}
			fmt.Fprintf(w, "    %s %s %s%s: %s\n", mark, dc.Level, dc.Code, subject, dc.Message)
		}
	}
}

// hashPrefix 截断摘要用于报告行（完整值在 plan: / input: 行）。
func hashPrefix(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12]
}

func printSegments(w io.Writer, label string, paths []string) {
	if len(paths) == 0 {
		return
	}
	sort.Strings(paths)
	fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(paths, ", "))
}
