// Copyright 2026 Lingfei Kong <colin404@foxmail.com>. All rights reserved.
// Use of this source code is governed by a MIT style
// license that can be found in the LICENSE file.

package matcher

import (
	"context"
	"testing"

	"github.com/kalandramo/bald/pkg/middleware"
)

// executed 记录中间件执行序。顺序断言走执行期而非身份表：闭包共享代码指针，
// reflect.Pointer 无法区分同一工厂产出的中间件；执行序同时验证 Match 的
// 返回序与 Chain 的组合序，是更强的断言。
type recorder struct {
	executed []string
}

func (r *recorder) factory(name string) middleware.Middleware {
	// 对称记录：↓ 入栈、↑ 出栈——回卷序也可断言。
	return func(next middleware.Handler) middleware.Handler {
		return func(ctx context.Context, _ interface{}) (interface{}, error) {
			r.executed = append(r.executed, name+"↓")
			resp, err := next(ctx, nil)
			r.executed = append(r.executed, name+"↑")
			return resp, err
		}
	}
}

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// run 把 Match 的结果组合后对 handler 执行一遍，返回执行序。
func run(r *recorder, m *Matcher, operation string) []string {
	compose := Match(m)
	tr := middleware.NewTransporter(middleware.KindGRPC, operation, "", nil, nil)
	defer middleware.ReleaseTransporter(tr)
	ctx := middleware.NewServerContext(context.Background(), tr)

	r.executed = nil
	h := compose(func(ctx context.Context, _ interface{}) (interface{}, error) {
		r.executed = append(r.executed, "handler")
		return nil, nil
	})
	_, _ = h(ctx, nil)
	return r.executed
}

// TestMatchOrdering 钉住 Match 的三层顺序：defaults → 精确 → 前缀（长者先），
// 且经 Chain 组合后的**执行序**一致。
func TestMatchOrdering(t *testing.T) {
	r := &recorder{}
	m := New()
	m.Use(r.factory("d1"))
	m.Add("/pkg.Service/Method", r.factory("exact"))
	m.Add("/pkg.Service/*", r.factory("prefix-long"))
	m.Add("/pkg.*", r.factory("prefix-short"))

	got := run(r, m, "/pkg.Service/Method")
	want := []string{"d1↓", "exact↓", "prefix-long↓", "prefix-short↓", "handler", "prefix-short↑", "prefix-long↑", "exact↑", "d1↑"}
	if !equalSlice(got, want) {
		t.Fatalf("execution order: got %v, want %v", got, want)
	}
}

// TestMatchExactOnly 无前缀命中时只落 defaults + 精确。
func TestMatchExactOnly(t *testing.T) {
	r := &recorder{}
	m := New()
	m.Use(r.factory("d"))
	m.Add("/pkg.Other/Method", r.factory("exact"))

	got := run(r, m, "/pkg.Service/Method")
	if !equalSlice(got, []string{"d↓", "handler", "d↑"}) {
		t.Fatalf("got %v, want [d handler]", got)
	}
}

// TestMatchWildcard 星号选择器匹配全部 operation。
func TestMatchWildcard(t *testing.T) {
	r := &recorder{}
	m := New()
	m.Add("*", r.factory("global"))

	for _, op := range []string{"/pkg.Svc/M", "GET /v1/x", "anything"} {
		got := run(r, m, op)
		if !equalSlice(got, []string{"global↓", "handler", "global↑"}) {
			t.Errorf("wildcard missed %q: %v", op, got)
		}
	}
}

// TestMatchNoHit 未命中任何选择器时只执行 handler（next 直通分支）。
func TestMatchNoHit(t *testing.T) {
	r := &recorder{}
	m := New()
	m.Add("/pkg.Service/*", r.factory("p"))

	got := run(r, m, "/other.Svc/M")
	if !equalSlice(got, []string{"handler"}) {
		t.Fatalf("got %v, want [handler]", got)
	}
}

// TestMatchMiddlewareComposes 无 Transporter 的 ctx 直通 next（降级分支）。
func TestMatchMiddlewareComposes(t *testing.T) {
	r := &recorder{}
	m := New()
	m.Add("/pkg.Service/*", r.factory("p"))

	compose := Match(m)
	r.executed = nil
	h := compose(func(ctx context.Context, _ interface{}) (interface{}, error) {
		r.executed = append(r.executed, "handler")
		return nil, nil
	})
	if _, err := h(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(r.executed) != 1 || r.executed[0] != "handler" {
		t.Fatalf("no-transporter path should bypass matcher: %v", r.executed)
	}
}
