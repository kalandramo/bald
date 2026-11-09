package codegen

import (
	"reflect"
	"testing"

	appspecv1 "github.com/kalandramo/bald/bconf/gen/go/bald/appspec/v1"
)

// 纯函数行级补全：这些字符串/切片折算 helper 是生成期的确定性口径（与
// pkg/authz、protoc-gen-go 命名同源），用表驱动锁住每条分支，无需落盘夹具。

func TestHelpers_GRPCActionOf(t *testing.T) {
	cases := map[string]string{
		"DeleteOrder": "delete",
		"ListOrders":  "list",
		"CreateOrder": "write",
		"UpdateOrder": "write",
		"SetOrder":    "write",
		"PutOrder":    "write",
		"GetOrder":    "get",
		"Order":       "get",
		"":            "get",
	}
	for in, want := range cases {
		if got := grpcActionOf(in); got != want {
			t.Errorf("grpcActionOf(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_HTTPObjectOf(t *testing.T) {
	cases := map[string]string{
		"/v1/orders/{id}":  "orders",
		"/orders":          "orders",
		"/v1/":             "",
		"":                 "",
		"///":              "",
		"/v1/v1/x":         "x",
		"/v1/orders/items": "orders",
	}
	for in, want := range cases {
		if got := httpObjectOf(in); got != want {
			t.Errorf("httpObjectOf(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_GRPCObjectOf(t *testing.T) {
	cases := map[string]string{
		"greet.v1.GreetService": "greet",
		"NoDotService":          "nodot",
		"pkg.Plain":             "plain",
		"":                      "",
	}
	for in, want := range cases {
		if got := grpcObjectOf(in); got != want {
			t.Errorf("grpcObjectOf(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_DomainDir(t *testing.T) {
	got, err := domainDir("example.com/m", "example.com/m/foo/v1;foo")
	if err != nil || got != "foo/v1" {
		t.Errorf("under module: got=%q err=%v, want foo/v1", got, err)
	}
	if _, err := domainDir("example.com/m", "other/x"); err == nil {
		t.Error("foreign go_package must error")
	}
	if _, err := domainDir("", "x"); err == nil {
		t.Error("empty module must error")
	}
	if _, err := domainDir("example.com/m", "example.com/m"); err == nil {
		t.Error("go_package equal to module (no subpath) must error")
	}
}

func TestHelpers_ParseProtoGoPackage(t *testing.T) {
	cases := []struct {
		in, path, alias string
	}{
		{"a/b/v1;foo", "a/b/v1", "foo"},
		{"a/b/v1", "a/b/v1", "v1"},
		{"a/b/my-pkg", "a/b/my-pkg", "my_pkg"},
		{"", "", "pb"},
		{"a/123", "a/123", "_123"},
		{"single;alias", "single", "alias"},
	}
	for _, c := range cases {
		path, alias := parseProtoGoPackage(c.in)
		if path != c.path || alias != c.alias {
			t.Errorf("parseProtoGoPackage(%q)=(%q,%q), want (%q,%q)", c.in, path, alias, c.path, c.alias)
		}
	}
}

func TestHelpers_SanitizeGoIdent(t *testing.T) {
	cases := map[string]string{
		"abc":   "abc",
		"1ab":   "_1ab",
		"a-b.c": "a_b_c",
		"a1":    "a1",
		"":      "",
		"_x":    "_x",
	}
	for in, want := range cases {
		if got := sanitizeGoIdent(in); got != want {
			t.Errorf("sanitizeGoIdent(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_GoName(t *testing.T) {
	cases := map[string]string{
		"":       "Role",
		"greet":  "Greet",
		"a-b":    "A_b",
		"worker": "Worker",
		"123":    "123",
	}
	for in, want := range cases {
		if got := goName(in); got != want {
			t.Errorf("goName(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_DomainAlias(t *testing.T) {
	cases := map[string]string{
		"a/b/v1":     "v1",
		"a/b/my-pkg": "my_pkg",
		"a/b/123":    "_123",
		"":           "_",
		"a/b/":       "_",
		"noslash":    "noslash",
	}
	for in, want := range cases {
		if got := domainAlias(in); got != want {
			t.Errorf("domainAlias(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_Title(t *testing.T) {
	cases := map[string]string{
		"":       "",
		"greet":  "Greet",
		"Greet":  "Greet",
		"123":    "123",
		"worker": "Worker",
	}
	for in, want := range cases {
		if got := title(in); got != want {
			t.Errorf("title(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_ServiceShortName(t *testing.T) {
	cases := map[string]string{
		"greet.v1.GreetService": "GreetService",
		"NoDot":                 "NoDot",
		"":                      "",
		"a.":                    "",
	}
	for in, want := range cases {
		if got := serviceShortName(in); got != want {
			t.Errorf("serviceShortName(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_DomainServices(t *testing.T) {
	cases := []struct {
		name string
		d    *appspecv1.DomainSpec
		want []string
	}{
		{"http_only", &appspecv1.DomainSpec{HttpOnly: true}, nil},
		{"http_only wins over services", &appspecv1.DomainSpec{HttpOnly: true, Services: []string{"x"}}, nil},
		{"services list", &appspecv1.DomainSpec{Services: []string{"a.b.C"}}, []string{"a.b.C"}},
		{"single service", &appspecv1.DomainSpec{Service: "a.b.C"}, []string{"a.b.C"}},
		{"empty", &appspecv1.DomainSpec{}, nil},
	}
	for _, c := range cases {
		if got := domainServices(c.d); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: domainServices=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestHelpers_DedupeSorted(t *testing.T) {
	if got := dedupeSorted(nil); got != nil {
		t.Errorf("nil input: got %v, want nil", got)
	}
	if got := dedupeSorted([]string{"b", "a", "b"}); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("dupes: got %v, want [a b]", got)
	}
	if got := dedupeSorted([]string{"a"}); !reflect.DeepEqual(got, []string{"a"}) {
		t.Errorf("single: got %v, want [a]", got)
	}
	if got := dedupeSorted([]string{"c", "a", "b"}); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("sorted: got %v, want [a b c]", got)
	}
}

func TestHelpers_BraceBlock(t *testing.T) {
	if body, end := braceBlock("{abc}", 0); body != "abc" || end != 4 {
		t.Errorf("simple: got (%q,%d), want (abc,4)", body, end)
	}
	if body, end := braceBlock("a{b{c}d}e", 1); body != "b{c}d" || end != 7 {
		t.Errorf("nested: got (%q,%d), want (b{c}d,7)", body, end)
	}
	if body, end := braceBlock("{unclosed", 0); body != "" || end != -1 {
		t.Errorf("unclosed: got (%q,%d), want (\"\",-1)", body, end)
	}
	if body, end := braceBlock("no braces", 0); body != "" || end != -1 {
		t.Errorf("no brace: got (%q,%d), want (\"\",-1)", body, end)
	}
}

func TestHelpers_NormalizeProtoType(t *testing.T) {
	cases := []struct{ pkg, in, want string }{
		{"greet.v1", "greet.v1.Msg", "Msg"},
		{"greet.v1", "Msg.Nested", "Msg_Nested"},
		{"", "X", "X"},
	}
	for _, c := range cases {
		if got := normalizeProtoType(c.pkg, c.in); got != c.want {
			t.Errorf("normalizeProtoType(%q,%q)=%q, want %q", c.pkg, c.in, got, c.want)
		}
	}
}

func TestHelpers_ManifestPackage(t *testing.T) {
	cases := map[string]string{
		"internal/bootstrap/appspec.manifest.json": "bootstrap",
		"appspec.manifest.json":                    "main",
		"a/b/c.json":                               "b",
	}
	for in, want := range cases {
		if got := manifestPackage(in); got != want {
			t.Errorf("manifestPackage(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestHelpers_MethodRules(t *testing.T) {
	rules := []httpRule{{Verb: "get", Path: "/x"}}
	if got := methodRules(protoMethod{Rules: rules}); !reflect.DeepEqual(got, rules) {
		t.Errorf("explicit rules: got %v, want %v", got, rules)
	}
	want := []httpRule{{Verb: "post", Path: "/y", Body: "*"}}
	if got := methodRules(protoMethod{Verb: "post", Path: "/y", Body: "*"}); !reflect.DeepEqual(got, want) {
		t.Errorf("verb fallback: got %v, want %v", got, want)
	}
	if got := methodRules(protoMethod{Name: "Foo"}); got != nil {
		t.Errorf("no verb no rules: got %v, want nil", got)
	}
}

func TestHelpers_DerivedActions(t *testing.T) {
	svc := protoService{
		Package: "greet.v1", Name: "GreetService",
		Methods: []protoMethod{{Name: "GetOrder"}, {Name: "DeleteOrder"}},
	}
	want := map[string]string{
		"greet.v1.GreetService/GetOrder":    "get",
		"greet.v1.GreetService/DeleteOrder": "delete",
	}
	if got := derivedActions(svc); !reflect.DeepEqual(got, want) {
		t.Errorf("derivedActions=%v, want %v", got, want)
	}
	if got := derivedActionsOf([]protoService{svc}); !reflect.DeepEqual(got, want) {
		t.Errorf("derivedActionsOf=%v, want %v", got, want)
	}
}

func TestHelpers_ServiceIndex(t *testing.T) {
	svc := protoService{Package: "p", Name: "S"}
	idx := serviceIndex(&protoScan{Services: []protoService{svc}})
	if got, ok := idx["p.S"]; !ok || !reflect.DeepEqual(got, svc) {
		t.Errorf("serviceIndex[p.S]=%v ok=%v, want %v", got, ok, svc)
	}
}

// TestHelpers_DerivedAuthz_AmbiguousAlias 锁住授权 object 别名歧义闸门：同一
// grpc object 派生出两个不同 http object 时必须报错（bald-admin 教训：冗余/
// 歧义映射会掩盖真实命名问题）。
func TestHelpers_DerivedAuthz_AmbiguousAlias(t *testing.T) {
	as := &appspecv1.AppSpec{Domains: []*appspecv1.DomainSpec{
		{Service: "p.v1.OrderService"},
		{Service: "q.v1.OrderService"},
	}}
	byService := map[string]protoService{
		"p.v1.OrderService": {Package: "p.v1", Name: "OrderService", Methods: []protoMethod{{Name: "Get", Path: "/v1/orders"}}},
		"q.v1.OrderService": {Package: "q.v1", Name: "OrderService", Methods: []protoMethod{{Name: "Get", Path: "/v1/items"}}},
	}
	if _, _, err := derivedAuthz(as, byService); err == nil {
		t.Fatal("ambiguous object alias (order→orders vs order→items) must error")
	}

	// 一致别名（两侧 grpc/http object 相同）不产生映射，merged 仍聚合动作。
	as2 := &appspecv1.AppSpec{Domains: []*appspecv1.DomainSpec{{Service: "p.v1.OrderService"}}}
	by2 := map[string]protoService{
		"p.v1.OrderService": {Package: "p.v1", Name: "OrderService", Methods: []protoMethod{{Name: "Get", Path: "/v1/order"}}},
	}
	aliases, merged, err := derivedAuthz(as2, by2)
	if err != nil || len(aliases) != 0 || merged["p.v1.OrderService/Get"] != "get" {
		t.Errorf("consistent alias: aliases=%v merged=%v err=%v", aliases, merged, err)
	}
}
