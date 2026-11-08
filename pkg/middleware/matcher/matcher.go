// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

// Package matcher 提供路由级中间件选择：把 transport operation 映射到作用于
// 它的中间件——不同端点可以携带不同的横切件（如只对 /admin/* 限流）。思想
// 借鉴 kratos 的内部 matcher，协议无关；代码自 onexmesh 移植。
//
// operation 是 gRPC 完整方法名，HTTP 则是动词加**路由模板**
// （"GET /v1/users/:userID"）而非请求路径——请求里换成什么 id 都不影响
// 匹配。见 middleware.Transporter.Operation。
package matcher

import (
	"sort"
	"strings"

	"github.com/kalandramo/bald/pkg/middleware"
)

// Matcher 按 operation 选择中间件。Use 登记全局默认；Add 按选择器登记。
// 选择器三种形态：精确 operation（"/pkg.Service/Method"）、以 * 结尾的前缀
// （"/pkg.Service/*"）、或 "*" 匹配全部。
type Matcher struct {
	defaults []middleware.Middleware
	exact    map[string][]middleware.Middleware
	prefixes map[string][]middleware.Middleware
	// sorted 持前缀键按长度降序的清单，Add 时重建——Match 在请求路径上不做
	// 任何排序或分配。
	sorted []string
}

// New 返回空 Matcher。
func New() *Matcher {
	return &Matcher{
		exact:    map[string][]middleware.Middleware{},
		prefixes: map[string][]middleware.Middleware{},
	}
}

// Use 追加作用于全部 operation 的全局中间件（最外层在前）。
func (m *Matcher) Use(mws ...middleware.Middleware) *Matcher {
	m.defaults = append(m.defaults, mws...)
	return m
}

// Add 按选择器登记中间件，返回 m 以便链式调用。
func (m *Matcher) Add(selector string, mws ...middleware.Middleware) *Matcher {
	switch {
	case selector == "*":
		m.prefixes[""] = append(m.prefixes[""], mws...)
		m.rebuildSorted()
	case strings.HasSuffix(selector, "*"):
		prefix := strings.TrimSuffix(selector, "*")
		m.prefixes[prefix] = append(m.prefixes[prefix], mws...)
		m.rebuildSorted()
	default:
		m.exact[selector] = append(m.exact[selector], mws...)
	}
	return m
}

// rebuildSorted 重算降序长度前缀序。只在 Add（启动期）运行，永不进请求路径。
func (m *Matcher) rebuildSorted() {
	m.sorted = make([]string, 0, len(m.prefixes))
	for prefix := range m.prefixes {
		m.sorted = append(m.sorted, prefix)
	}
	sort.Slice(m.sorted, func(i, j int) bool { return len(m.sorted[i]) > len(m.sorted[j]) })
}

// Match 返回作用于 operation 的中间件：defaults 先，然后精确匹配，然后前缀
// 匹配（前缀长者先）。
func (m *Matcher) Match(operation string) []middleware.Middleware {
	ms := make([]middleware.Middleware, 0, len(m.defaults)+4)
	ms = append(ms, m.defaults...)

	if exact, ok := m.exact[operation]; ok {
		ms = append(ms, exact...)
	}

	for _, prefix := range m.sorted {
		if strings.HasPrefix(operation, prefix) {
			ms = append(ms, m.prefixes[prefix]...)
		}
	}

	return ms
}
