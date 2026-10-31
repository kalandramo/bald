package appkit

import (
	"fmt"
	"sort"
	"strings"
)

// Route 是一条域自持 REST 路由的声明（域名 + 方法 + 路径 + 可选 RPC 名）。
//
// 它是计划可检视的路由清单，不是路由的实现：域在 Domain.RouteTable 里声明
// 自己实际挂载的路由（生成器可从 proto 的 google.api.http 注解派生），
// Compile 汇总进 Plan.Routes。方法归一化为大写，路径须以 "/" 开头；
// 同一方法下路径冲突（完全重复，或 gin 无法并存的通配形态）是编译错误——
// 路由冲突在计划期暴露，而不是等 gin 在启动期 panic。
type Route struct {
	Domain string `json:"domain"`
	Method string `json:"method"`
	Path   string `json:"path"`
	RPC    string `json:"rpc,omitempty"`
	// Variant 非空表示这条路由由命中变体声明（Domain = "variant:<name>"）。
	Variant string `json:"variant,omitempty"`
}

// httpRoutingMethods 是计划期接受的路由方法（typo 在计划期报错）。
var httpRoutingMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true,
	"DELETE": true, "HEAD": true, "OPTIONS": true,
}

// collectDomainRoutes 汇总域的 RouteTable 为计划路由清单：
//   - 方法归一化为大写、路径原样（校验在 validateDomains：方法白名单、以 "/"
//     开头、非空）；
//   - 同一 (方法, 路径) 被两个域声明即冲突——两个域都想挂同一路由，运行期
//     必然踩踏（gin panic 或静默覆盖），在计划期报错并点名双方；
//   - 声称能挂、gin 却挂不上去的形态也在计划期报错：同一方法下通配段重名
//     不一致（/users/:id 与 /users/:name）会 panic，catch-all 与任何更长路径
//     也无法并存——这类冲突不靠「路径字符串完全相同」就能发现，按段比对；
//   - 声明了 Routes 闭包却没有 RouteTable 的域给 custom-routes 警告：
//     手写路由计划看不见，这是诚实缺省，不是错误（Q5 的 --no-routes/--no-bind 逃逸）。
func collectDomainRoutes(d *Decl) ([]Route, []Diagnostic, error) {
	var (
		out   []Route
		warns []Diagnostic
	)
	owner := map[string]Route{} // "METHOD path" -> 已登记的 Route
	for _, dom := range d.Domains {
		if len(dom.RouteTable) == 0 {
			if dom.Routes != nil {
				warns = append(warns, Diagnostic{
					Level:   DiagWarn,
					Code:    "custom-routes",
					Subject: dom.Name,
					Message: "domain registers REST routes through a closure with no RouteTable; the plan cannot list them",
				})
			}
			continue
		}
		for _, r := range dom.RouteTable {
			method := strings.ToUpper(strings.TrimSpace(r.Method))
			path := strings.TrimSpace(r.Path)
			if err := validateRouteShape(dom.Name, method, path); err != nil {
				return nil, nil, err
			}
			key := method + " " + path
			if prev, ok := owner[key]; ok {
				if prev.Domain == dom.Name {
					// 同一域重复登记常见于「生成器派生了清单、域里还留着
					// 手写 RouteTable」——运行期同样会注册两遍，点名处置方式。
					return nil, nil, fmt.Errorf("appkit: REST route %s %s is declared twice by domain %q (drop the hand-written RouteTable or the generated one)", method, path, dom.Name)
				}
				return nil, nil, fmt.Errorf("appkit: REST route %s %s is declared by both domain %q and %q", method, path, prev.Domain, dom.Name)
			}
			for _, prev := range out {
				if prev.Method != method {
					continue // 方法各自一棵 gin 路由树，互不干扰
				}
				reason := routeShapeConflict(prev.Path, path)
				if reason == "" {
					continue
				}
				if prev.Domain == dom.Name {
					return nil, nil, fmt.Errorf("appkit: domain %q declares conflicting REST routes %s %s and %s %s: %s",
						dom.Name, method, prev.Path, method, path, reason)
				}
				return nil, nil, fmt.Errorf("appkit: REST routes %s %s (domain %q) and %s %s (domain %q) collide in the gin route tree: %s",
					method, prev.Path, prev.Domain, method, path, dom.Name, reason)
			}
			now := Route{Domain: dom.Name, Method: method, Path: path, RPC: r.RPC}
			owner[key] = now
			out = append(out, now)
		}
	}
	sortRoutes(out)
	return out, warns, nil
}

// sortRoutes 按 (Domain, Path, Method, RPC) 排序（计划置换不变性的来源）。
func sortRoutes(routes []Route) {
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Domain != routes[j].Domain {
			return routes[i].Domain < routes[j].Domain
		}
		if routes[i].Path != routes[j].Path {
			return routes[i].Path < routes[j].Path
		}
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].RPC < routes[j].RPC
	})
}

// mergeRoutes 把变体声明的路由并入域路由清单，并沿用与域内相同的冲突判据：
// 完全重复（方法+路径）与 gin 无法并存的通配形态都在计划期报错——变体路由
// 不是特权通道，它和手写路由争同一棵 gin 树。
func mergeRoutes(base, extra []Route) ([]Route, error) {
	out := append([]Route(nil), base...)
	for _, r := range extra {
		for _, prev := range out {
			if prev.Method != r.Method {
				continue
			}
			if prev.Path == r.Path {
				return nil, fmt.Errorf("appkit: variant route %s %s (variant %q) is also declared by %q", r.Method, r.Path, r.Variant, prev.Domain)
			}
			if reason := routeShapeConflict(prev.Path, r.Path); reason != "" {
				return nil, fmt.Errorf("appkit: variant route %s %s (variant %q) collides with %s %s (domain %q) in the gin route tree: %s",
					r.Method, r.Path, r.Variant, prev.Method, prev.Path, prev.Domain, reason)
			}
		}
		out = append(out, r)
	}
	sortRoutes(out)
	return out, nil
}

// routeSeg 是路由路径的一个段：静态字面量、参数（:name / {name}）或
// catch-all（*name）。{name} 是 proto 注解形态，生成器已转成 :name，这里
// 一并识别，避免同一路由的两种写法绕过冲突检查。
type routeSeg struct {
	kind byte   // 's' static, 'p' param, 'a' catch-all
	name string // 通配名（静态段为空）
	text string // 原始段文本，用于报错
}

func splitRoutePath(path string) []routeSeg {
	raw := strings.Split(strings.TrimPrefix(path, "/"), "/")
	segs := make([]routeSeg, len(raw))
	for i, s := range raw {
		switch {
		case strings.HasPrefix(s, "*"):
			segs[i] = routeSeg{kind: 'a', name: s[1:], text: s}
		case strings.HasPrefix(s, ":"):
			segs[i] = routeSeg{kind: 'p', name: s[1:], text: s}
		case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
			name := strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
			name, _, _ = strings.Cut(name, "=")
			segs[i] = routeSeg{kind: 'p', name: name, text: s}
		default:
			segs[i] = routeSeg{kind: 's', text: s}
		}
	}
	return segs
}

// routeShapeConflict 按 gin 的注册语义比两条同方法路径，返回冲突原因
// （空串表示可以并存）：
//   - 静态兄弟与通配兄弟可以并存（gin 回溯匹配，静态优先），不同静态段
//     一旦分叉就再无交集；
//   - 同一位置两个参数必须同名——gin 的树按通配文本共享节点，:id 与
//     :name 会在注册时 panic；
//   - catch-all 吞掉其后所有段，与任何同前缀的更具体路径都不能并存，
//     无论注册先后。
//
// 只识别整段通配（:name / {name} / *name），段内混排通配不在检查范围。
func routeShapeConflict(a, b string) string {
	sa, sb := splitRoutePath(a), splitRoutePath(b)
	for i := 0; i < len(sa) && i < len(sb); i++ {
		x, y := sa[i], sb[i]
		switch {
		case x.kind == 'a' || y.kind == 'a':
			return fmt.Sprintf("segment %d has catch-all %q against %q, and gin cannot register a catch-all beside any other route under the same prefix", i+1, x.text, y.text)
		case x.kind == 's' && y.kind == 's':
			if x.text != y.text {
				return ""
			}
		case x.kind == 's' || y.kind == 's':
			return ""
		default:
			if x.name != y.name {
				return fmt.Sprintf("segment %d uses wildcards %q and %q, and gin requires one wildcard name per position", i+1, x.text, y.text)
			}
		}
	}
	if len(sa) < len(sb) && sa[len(sa)-1].kind == 'a' {
		return fmt.Sprintf("catch-all %q leaves no room for the longer path %q", sa[len(sa)-1].text, b)
	}
	if len(sb) < len(sa) && sb[len(sb)-1].kind == 'a' {
		return fmt.Sprintf("catch-all %q leaves no room for the longer path %q", sb[len(sb)-1].text, a)
	}
	return ""
}

// validateRouteShape 把 gin 注册期才会 panic 的路径形态提前到计划期：
// catch-all 必须在最后一段、通配符必须有名字、每段至多一个通配符、'*' 只能
// 出现在段首。{name=...} 的 proto 形态整段视为参数，不按 gin 语法拆。
func validateRouteShape(domain, method, path string) error {
	segs := splitRoutePath(path)
	for i, s := range segs {
		switch s.kind {
		case 'a':
			if s.name == "" {
				return fmt.Errorf("appkit: domain %q route %s %s: catch-all %q must be named", domain, method, path, s.text)
			}
			if i != len(segs)-1 {
				return fmt.Errorf("appkit: domain %q route %s %s: catch-all %q must be the last path segment", domain, method, path, s.text)
			}
		case 'p':
			if s.name == "" {
				return fmt.Errorf("appkit: domain %q route %s %s: wildcard %q must be named", domain, method, path, s.text)
			}
		}
		if strings.HasPrefix(s.text, "{") {
			continue
		}
		if n := strings.Count(s.text, ":") + strings.Count(s.text, "*"); n > 1 {
			return fmt.Errorf("appkit: domain %q route %s %s: segment %q has %d wildcards; gin allows at most one per segment", domain, method, path, s.text, n)
		}
		if k := strings.IndexByte(s.text, '*'); k > 0 {
			return fmt.Errorf("appkit: domain %q route %s %s: catch-all %q must start a path segment", domain, method, path, s.text)
		}
	}
	return nil
}
