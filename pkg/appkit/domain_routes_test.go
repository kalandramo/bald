// domain_routes_test.go 钉住 Q5 的可见性面：域声明的 REST 路由清单
// （Domain.RouteTable）进入计划——可打印、可哈希、顺序无关；跨域同路由在
// 计划期报错；手写 Routes 闭包没有清单时给 custom-routes 警告。
package appkit

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestPlan_DomainRoutesVisible(t *testing.T) {
	cfgPath := writePlanConfig(t, "routes-app", "")
	greet := Domain{
		Name: "greet",
		RouteTable: []Route{
			{Method: "post", Path: "/v1/greet", RPC: "Greet"},
			{Method: "GET", Path: "/v1/greet/{name}", RPC: "GreetGet"},
		},
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithHTTP(http.NewServeMux()), greet)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	want := []Route{
		{Domain: "greet", Method: "POST", Path: "/v1/greet", RPC: "Greet"},
		{Domain: "greet", Method: "GET", Path: "/v1/greet/{name}", RPC: "GreetGet"},
	}
	if len(p.Routes) != len(want) {
		t.Fatalf("routes = %+v, want %+v", p.Routes, want)
	}
	for i := range want {
		if p.Routes[i] != want[i] {
			t.Fatalf("routes[%d] = %+v, want %+v", i, p.Routes[i], want[i])
		}
	}

	// 计划文本与 JSON 都必须能看见路由（可检视，不靠运行）。
	var sb strings.Builder
	p.Explain(&sb)
	for _, line := range []string{"routes:", "GET     /v1/greet/{name} -> GreetGet", "POST    /v1/greet -> Greet"} {
		if !strings.Contains(sb.String(), line) {
			t.Fatalf("Explain missing %q:\n%s", line, sb.String())
		}
	}
	raw, err := p.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	if !strings.Contains(string(raw), `"path": "/v1/greet/{name}"`) {
		t.Fatalf("JSON missing route path: %s", raw)
	}
}

func TestPlan_DomainRoutesOrderInvariant(t *testing.T) {
	cfgPath := writePlanConfig(t, "routes-order", "")
	greet := Domain{Name: "greet", RouteTable: []Route{{Method: "POST", Path: "/v1/greet", RPC: "Greet"}}}
	dict := Domain{Name: "dict", RouteTable: []Route{
		{Method: "GET", Path: "/v1/dicts/{id}", RPC: "Get"},
		{Method: "POST", Path: "/v1/dicts", RPC: "Create"},
	}}
	forward, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), greet, dict)
	if err != nil {
		t.Fatalf("Compile forward: %v", err)
	}
	reversed, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), dict, greet)
	if err != nil {
		t.Fatalf("Compile reversed: %v", err)
	}
	if forward.Hash() != reversed.Hash() {
		t.Fatalf("domain order changed plan hash: %s vs %s", forward.Hash(), reversed.Hash())
	}
	if len(forward.Routes) != 3 || forward.Routes[0].Domain != "dict" || forward.Routes[2].Domain != "greet" {
		t.Fatalf("routes not sorted by (domain, path): %+v", forward.Routes)
	}
}

func TestPlan_DomainRoutesConflict(t *testing.T) {
	cfgPath := writePlanConfig(t, "routes-conflict", "")
	a := Domain{Name: "alpha", RouteTable: []Route{{Method: "POST", Path: "/v1/things"}}}
	b := Domain{Name: "beta", RouteTable: []Route{{Method: "post", Path: "/v1/things"}}}
	_, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), a, b)
	if err == nil {
		t.Fatal("duplicate REST route across domains must fail at plan time")
	}
	for _, want := range []string{"POST /v1/things", `"alpha"`, `"beta"`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want containing %q", err, want)
		}
	}
}

// TestPlan_DomainRoutesDuplicateInDomain 钉住同一域内重复登记（生成器派生的
// 清单与手写 RouteTable 并存）的报错：点名域名与处置方式，而不是含糊的
// 「两个域都声明了」。
func TestPlan_DomainRoutesDuplicateInDomain(t *testing.T) {
	cfgPath := writePlanConfig(t, "routes-dup", "")
	dom := Domain{Name: "greet", RouteTable: []Route{
		{Method: "POST", Path: "/v1/greet"},
		{Method: "post", Path: "/v1/greet"},
	}}
	_, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), dom)
	if err == nil {
		t.Fatal("duplicate REST route within one domain must fail at plan time")
	}
	for _, want := range []string{"declared twice by domain \"greet\"", "drop the hand-written RouteTable or the generated one"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v, want containing %q", err, want)
		}
	}
}

// TestPlan_DomainRoutesGinShapeConflict 钉住「路径不同但 gin 挂不上去」的
// 冲突：通配参数名不一致、catch-all 与更具体路径并存。这类冲突不能靠完全
// 相同的路径字符串发现，必须在计划期按段比对报错。
func TestPlan_DomainRoutesGinShapeConflict(t *testing.T) {
	cases := []struct {
		name string
		a, b *Domain
		want []string
	}{
		{
			name: "cross-domain wildcard names differ",
			a:    &Domain{Name: "alpha", RouteTable: []Route{{Method: "GET", Path: "/v1/users/:id"}}},
			b:    &Domain{Name: "beta", RouteTable: []Route{{Method: "GET", Path: "/v1/users/:name"}}},
			want: []string{`GET /v1/users/:id`, `"alpha"`, `GET /v1/users/:name`, `"beta"`, "one wildcard name per position"},
		},
		{
			name: "catch-all against param",
			a:    &Domain{Name: "alpha", RouteTable: []Route{{Method: "GET", Path: "/v1/files/*path"}}},
			b:    &Domain{Name: "beta", RouteTable: []Route{{Method: "GET", Path: "/v1/files/:name"}}},
			want: []string{"catch-all", "gin cannot register a catch-all beside any other route"},
		},
		{
			name: "catch-all against longer static",
			a:    &Domain{Name: "alpha", RouteTable: []Route{{Method: "GET", Path: "/v1/files/*path"}}},
			b:    &Domain{Name: "beta", RouteTable: []Route{{Method: "GET", Path: "/v1/files/raw/y"}}},
			want: []string{"catch-all", "gin cannot register a catch-all beside any other route"},
		},
		{
			name: "same domain wildcard names differ",
			a: &Domain{Name: "greet", RouteTable: []Route{
				{Method: "GET", Path: "/v1/users/{id}"},
				{Method: "GET", Path: "/v1/users/:name"},
			}},
			want: []string{`domain "greet" declares conflicting REST routes`, "one wildcard name per position"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writePlanConfig(t, "routes-shape", "")
			opts := []Option{ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), *tc.a}
			if tc.b != nil {
				opts = append(opts, *tc.b)
			}
			_, err := Compile(context.Background(), opts...)
			if err == nil {
				t.Fatal("gin-unrepresentable route pair must fail at plan time")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("err = %v, want containing %q", err, want)
				}
			}
		})
	}
}

// TestPlan_DomainRoutesGinShapeCompatible 防误报：gin 允许的形态必须保持
// 可编译——同位置同参数名、通配下的子路径、静态兄弟、以及不同方法。
func TestPlan_DomainRoutesGinShapeCompatible(t *testing.T) {
	cfgPath := writePlanConfig(t, "routes-shape-ok", "")
	alpha := Domain{Name: "alpha", RouteTable: []Route{
		{Method: "GET", Path: "/v1/users/:id"},
		{Method: "GET", Path: "/v1/users/:id/profile"},
		{Method: "GET", Path: "/v1/users/me"},
		{Method: "POST", Path: "/v1/users/:name"},
	}}
	beta := Domain{Name: "beta", RouteTable: []Route{
		{Method: "GET", Path: "/v1/users/:id/settings"},
		{Method: "PUT", Path: "/v1/users/:name"},
	}}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), alpha, beta)
	if err != nil {
		t.Fatalf("compatible gin shapes must compile, got: %v", err)
	}
	if len(p.Routes) != 6 {
		t.Fatalf("routes = %+v, want 6", p.Routes)
	}
}

func TestRouteShapeConflict(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"/users/:id", "/users/:id", false}, // 完全重复由 owner 键单独判
		{"/users/:id", "/users/:name", true},
		{"/users/:id", "/users/:id/profile", false},
		{"/users/:id", "/users/me", false},
		{"/files/*path", "/files/x", true},
		{"/files/x", "/files/*path", true},
		{"/files", "/files/*path", false},
		{"/files/*path", "/files", false},
		{"/files/*path", "/files/x/y", true},
		{"/a/:id/b", "/a/:id/c", false},
		{"/a/:id/b", "/a/:name/c", true},
		{"/v1/users/{id}", "/v1/users/:name", true},
		{"/a", "/a/", false},
	}
	for _, tc := range cases {
		if got := routeShapeConflict(tc.a, tc.b) != ""; got != tc.want {
			t.Errorf("routeShapeConflict(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
		if got := routeShapeConflict(tc.b, tc.a) != ""; got != tc.want {
			t.Errorf("routeShapeConflict(%q, %q) = %v, want %v (order must not matter)", tc.b, tc.a, got, tc.want)
		}
	}
}

// TestPlan_CustomRoutesWarning 钉住手写 Routes 闭包没有清单时的诚实缺省。
func TestPlan_CustomRoutesWarning(t *testing.T) {
	cfgPath := writePlanConfig(t, "routes-custom", "")
	dom := Domain{
		Name:   "greet",
		Routes: func(http.Handler) error { return nil },
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), dom)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if !hasWarning(p, "custom-routes", "greet") {
		t.Fatalf("custom-routes warning missing: %+v", p.Warnings)
	}
}

func TestPlan_DomainRoutesValidation(t *testing.T) {
	cases := []struct {
		name string
		dom  Domain
		opts []Option
		want string
	}{
		{
			name: "unknown method",
			dom:  Domain{Name: "greet", RouteTable: []Route{{Method: "FETCH", Path: "/v1/greet"}}},
			opts: []Option{WithHTTP(http.NewServeMux())},
			want: `unknown HTTP method "FETCH"`,
		},
		{
			name: "path without slash",
			dom:  Domain{Name: "greet", RouteTable: []Route{{Method: "GET", Path: "v1/greet"}}},
			opts: []Option{WithHTTP(http.NewServeMux())},
			want: `must start with "/"`,
		},
		{
			name: "no http face",
			dom:  Domain{Name: "greet", RouteTable: []Route{{Method: "GET", Path: "/v1/greet"}}},
			want: "declares RouteTable but no HTTP face",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writePlanConfig(t, "routes-bad", "")
			opts := append([]Option{ConfigFile(cfgPath)}, tc.opts...)
			_, err := Compile(context.Background(), append(opts, tc.dom)...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
