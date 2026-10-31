package appkit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// JSON 返回机读格式（--plan-format=json）。
func (p *Plan) JSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// Hash 对规范化 JSON（紧凑、字段序固定、切片已排序）求 sha256。
func (p *Plan) Hash() string {
	b, err := json.Marshal(p)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Explain 输出人读格式（--plan 默认）。
func (p *Plan) Explain(w io.Writer) {
	fmt.Fprintf(w, "app: %s %s (env=%s)\n", p.App.Name, p.App.Version, p.App.Env)
	if p.Role != "" {
		fmt.Fprintf(w, "role: %s\n", p.Role)
	}
	fmt.Fprintf(w, "input: %s\n", p.InputHash)

	fmt.Fprintln(w, "nodes:")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, n := range p.Nodes {
		var flags []string
		origin := string(n.Origin)
		if n.Variant != "" {
			origin = "variant:" + n.Variant
		}
		flags = append(flags, origin)
		if n.Opaque {
			flags = append(flags, "opaque")
		}
		if n.Override {
			flags = append(flags, "override: "+n.Reason)
		}
		if n.Reload != ReloadStatic {
			flags = append(flags, "reload="+n.Reload.String())
		}
		fmt.Fprintf(tw, "  %s\t%s\tpriority=%d\t%s\n", n.Phase, n.Key, n.Priority, strings.Join(flags, ", "))
		if len(n.Segments) > 0 {
			fmt.Fprintf(tw, "\t\t\tsegments=%s\n", strings.Join(n.Segments, ","))
		}
		if len(n.DependsOn) > 0 {
			deps := make([]string, len(n.DependsOn))
			for i, d := range n.DependsOn {
				deps[i] = string(d)
			}
			fmt.Fprintf(tw, "\t\t\tdependsOn=%s\n", strings.Join(deps, ","))
		}
	}
	_ = tw.Flush()

	fmt.Fprintf(w, "opaque: %d node(s)\n", p.OpaqueCount)

	if len(p.Routes) > 0 {
		fmt.Fprintln(w, "routes:")
		for _, r := range p.Routes {
			rpc := ""
			if r.RPC != "" {
				rpc = " -> " + r.RPC
			}
			fmt.Fprintf(w, "  %-12s %-7s %s%s\n", r.Domain, r.Method, r.Path, rpc)
		}
	}

	if len(p.Variants) > 0 {
		active := 0
		for _, v := range p.Variants {
			if v.Active {
				active++
			}
		}
		fmt.Fprintf(w, "variants: %d (active %d)\n", len(p.Variants), active)
		for _, v := range p.Variants {
			state := "declared"
			if v.Active {
				state = "active"
			}
			fmt.Fprintf(w, "  %-8s %-20s when=%s\n", state, v.Name, variantWhenLabel(v))
			for _, eff := range variantEffectLines(v) {
				fmt.Fprintf(w, "           %-20s %s\n", "", eff)
			}
		}
	}

	fmt.Fprintln(w, "segments:")
	for _, s := range p.Segments {
		var status []string
		if len(s.Consumers) > 0 {
			keys := make([]string, len(s.Consumers))
			for i, k := range s.Consumers {
				keys[i] = string(k)
			}
			status = append(status, "consumers="+strings.Join(keys, ","))
		}
		if s.Framework {
			status = append(status, "framework")
		}
		if s.Whitelisted {
			status = append(status, "whitelisted")
		}
		if len(status) == 0 {
			status = append(status, "unconsumed")
		}
		fmt.Fprintf(w, "  %-20s %s\n", s.Path, strings.Join(status, ", "))
	}

	if len(p.Imperative) > 0 {
		fmt.Fprintln(w, "imperative:")
		for _, it := range p.Imperative {
			name := it.Name
			if name == "" {
				name = "-"
			}
			fmt.Fprintf(w, "  %-12s %s\n", it.Kind, name)
		}
	}

	if len(p.Warnings) > 0 {
		fmt.Fprintln(w, "warnings:")
		for _, warn := range p.Warnings {
			subject := ""
			if warn.Subject != "" {
				subject = " [" + warn.Subject + "]"
			}
			fmt.Fprintf(w, "  %s %s%s: %s\n", warn.Level, warn.Code, subject, warn.Message)
		}
	}
	fmt.Fprintf(w, "plan hash: %s\n", p.Hash())
}

// writePlan 按 --plan-format 输出计划。
func writePlan(w io.Writer, p *Plan, format string) error {
	switch strings.ToLower(format) {
	case "", "text":
		p.Explain(w)
		return nil
	case "json":
		b, err := p.JSON()
		if err != nil {
			return err
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("appkit: unknown --plan-format %q (want text or json)", format)
	}
}
