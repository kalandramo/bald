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
	"runtime/debug"
	"sort"
	"strings"
	"text/tabwriter"
)

// Doctor 编译装配计划并向 stdout 输出诊断报告（纯读取，可安全用于运维探针与 CI）。
func Doctor(ctx context.Context, opts ...Option) error {
	d := newDecl(opts...)
	d.ConfigFile = parseConfigFlag(d.ConfigFile)
	p, err := compileDecl(ctx, d)
	if err != nil {
		return err
	}
	writeDoctor(os.Stdout, p, buildTags())
	return nil
}

// buildTags 读取构建期 -tags 设置（未显式指定时返回 nil）。
func buildTags() []string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return nil
	}
	for _, s := range bi.Settings {
		if s.Key == "-tags" && s.Value != "" {
			return strings.Split(s.Value, ",")
		}
	}
	return nil
}

// writeDoctor 渲染 doctor 报告。所有清单按字典序输出，保证同一计划字节稳定。
func writeDoctor(w io.Writer, p *Plan, tags []string) {
	fmt.Fprintln(w, "bald doctor")
	fmt.Fprintf(w, "app: %s %s (env=%s)\n", p.App.Name, p.App.Version, p.App.Env)
	if p.Role != "" {
		fmt.Fprintf(w, "role: %s\n", p.Role)
	}
	fmt.Fprintf(w, "input: %s\n", p.InputHash)
	fmt.Fprintf(w, "plan:  %s\n", p.Hash())

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

func printSegments(w io.Writer, label string, paths []string) {
	if len(paths) == 0 {
		return
	}
	sort.Strings(paths)
	fmt.Fprintf(w, "  %s: %s\n", label, strings.Join(paths, ", "))
}
