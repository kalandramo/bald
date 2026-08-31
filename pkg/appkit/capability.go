// capability.go 实现 S1 能力声明与解析（docs/devel/zh-CN/架构优化路线.md）。
//
// 背景（对照 Cordis 论文的空间可组合性/共效应）：bald 的组件此前互相依赖时
// 不声明、不解析，依赖缺失要到运行时第 N 个请求才炸（go-bald-admin M8：
// issueToken 在 InitBridges 之前调用 → Signer nil panic）。S1 把依赖显式化：
// 装配期声明 Provides/Requires，启动早期 Resolve 校验，缺依赖 fail-fast：
//
//	app := appkit.New(
//	    appkit.Provides("db", "cache-redis"),   // BeforeStart 将建立的能力
//	    appkit.Requires("audit.store", "db"),   // 审计落库依赖 DB
//	)
//	// 若漏声明 Provides("db")，Run 启动即失败：
//	// appkit: unresolved capabilities: capability "db" (required by audit.store); provided: []
//
// 能力是纯声明符号（不做实例管理——那是 C1 Component 的职责），Resolve 只校验
// 装配一致性，为 C1/R1 的组件图打底。
package appkit

import (
	"fmt"
	"sort"
	"strings"
)

// requirement 记录一个组件对能力的依赖声明。
type requirement struct {
	component string   // 依赖方组件名（报错定位用）
	caps      []string // 依赖的能力列表
}

// Provides 声明本进程将提供的能力（可多次调用，追加）。能力名建议用稳定短
// 标识："db"、"cache-redis"、"mq"。只声明确定拥有的能力（可选依赖如缓存，
// 降级运行时不声明）。同一能力重复声明视为装配笔误，Resolve 报错。
func Provides(caps ...string) Option {
	return func(a *AppKit) { a.provides = append(a.provides, caps...) }
}

// Requires 声明组件 component 依赖的能力。component 是依赖方标识（建议
// "<domain>.<name>"，如 "audit.store"），仅用于缺失时的报错定位。
// 同一组件可多次声明（合并）；同一 (component, cap) 重复声明幂等。
func Requires(component string, caps ...string) Option {
	return func(a *AppKit) { a.requires = append(a.requires, requirement{component: component, caps: caps}) }
}

// Resolve 校验能力声明的一致性：
//  1. 所有 Requires 的能力必须被 Provides 覆盖（缺失聚合报错，一次列全）；
//  2. Provides 无重复声明；
//  3. 能力名/组件名非空。
//
// Run 在启动早期自动调用（配置加载后、beforeStart 前）；手动调用用于测试
// 或「装配校验与运行解耦」场景。
func (a *AppKit) Resolve() error {
	seen := make(map[string]struct{}, len(a.provides))
	var errs []string

	// 2)/3) Provides：空名 + 重复校验。
	for _, c := range a.provides {
		if strings.TrimSpace(c) == "" {
			errs = append(errs, "appkit: Provides() contains empty capability name")
			continue
		}
		if _, dup := seen[c]; dup {
			errs = append(errs, fmt.Sprintf("appkit: capability %q provided more than once", c))
			continue
		}
		seen[c] = struct{}{}
	}

	// 1)/3) Requires：空名 + 覆盖校验（缺失聚合，一次修完）。
	var missing []string
	for _, r := range a.requires {
		if strings.TrimSpace(r.component) == "" {
			errs = append(errs, "appkit: Requires() called with empty component name")
			continue
		}
		for _, c := range r.caps {
			if strings.TrimSpace(c) == "" {
				errs = append(errs, fmt.Sprintf("appkit: component %q requires empty capability name", r.component))
				continue
			}
			if _, ok := seen[c]; !ok {
				missing = append(missing, fmt.Sprintf("capability %q (required by %s)", c, r.component))
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing) // 稳定输出，便于测试与人工比对
		errs = append(errs, "appkit: unresolved capabilities: "+strings.Join(missing, "; "))
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s; provided: [%s]", strings.Join(errs, "; "), strings.Join(a.provides, ", "))
	}
	return nil
}
