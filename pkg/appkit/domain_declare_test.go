// domain_declare_test.go 钉住 Domain v2（设计 §4.6）的声明面语义：
// Declare 返回的节点进入计划且非 Opaque；兼容槽位（Wire/Routes/Services）
// 在声明 Declare 后塌缩成单个 domain.<name>.wire Opaque 节点；未声明
// Declare 的域保持逐槽位投影；声明节点校验 fail-fast。
package appkit

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func nodeByKey(p *Plan, key ResourceKey) *Node {
	for i := range p.Nodes {
		if p.Nodes[i].Key == key {
			return &p.Nodes[i]
		}
	}
	return nil
}

func TestDomain_DeclareProjectsNonOpaqueNodes(t *testing.T) {
	cfgPath := writePlanConfig(t, "declare-app", "")
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{
				Key:      "domain.greet.routes",
				Segments: []string{"server.http"},
				Phase:    PhaseServe,
				Priority: 10,
			}}, nil
		},
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), dom)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	n := nodeByKey(p, "domain.greet.routes")
	if n == nil {
		t.Fatalf("declared node missing: %+v", p.Nodes)
	}
	if n.Opaque {
		t.Fatal("declared node must not be Opaque")
	}
	if n.Kind != "domain" || n.Origin != OriginDomain {
		t.Fatalf("normalized node = %+v", n)
	}
	if n.Reload != ReloadStatic {
		t.Fatalf("reload default = %v", n.Reload)
	}
	if nodeByKey(p, "domain.greet") != nil || nodeByKey(p, "domain.greet.wire") != nil {
		t.Fatal("pure-declare domain must not synthesize compat nodes")
	}
}

func TestDomain_DeclareCollapsesCompatIntoOneOpaqueNode(t *testing.T) {
	cfgPath := writePlanConfig(t, "declare-compat", "")
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{
				Key:       "domain.greet.routes",
				Segments:  []string{"server.http"},
				Phase:     PhaseServe,
				DependsOn: []ResourceKey{"domain.greet.wire"},
			}}, nil
		},
		Wire:   func(context.Context, *App) error { return nil },
		Routes: func(http.Handler) error { return nil },
	}
	p, err := Compile(context.Background(), ConfigFile(cfgPath), WithHTTP(http.NewServeMux()), dom)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	wire := nodeByKey(p, "domain.greet.wire")
	if wire == nil {
		t.Fatalf("compat wire node missing: %+v", p.Nodes)
	}
	if !wire.Opaque {
		t.Fatal("compat wire node must stay Opaque")
	}
	if len(wire.Segments) != 1 || wire.Segments[0] != "server.http" {
		t.Fatalf("wire segments = %v, want [server.http]", wire.Segments)
	}
	// 旧逐槽位投影（domain.greet / .routes / .services）不得同时出现。
	for _, legacy := range []ResourceKey{"domain.greet", "domain.greet.services"} {
		if nodeByKey(p, legacy) != nil {
			t.Fatalf("legacy node %s must not be synthesized when Declare is present", legacy)
		}
	}
	if n := nodeByKey(p, "domain.greet.routes"); n == nil || n.Opaque {
		t.Fatalf("declared routes node = %+v, want non-opaque", n)
	}
}

func TestDomain_DeclareRejectsBadNodes(t *testing.T) {
	cases := []struct {
		name    string
		declare func(context.Context, PlanScope) ([]Node, error)
		want    string
	}{
		{
			name: "empty key",
			declare: func(context.Context, PlanScope) ([]Node, error) {
				return []Node{{Phase: PhaseWire}}, nil
			},
			want: "empty key",
		},
		{
			name: "framework phase",
			declare: func(context.Context, PlanScope) ([]Node, error) {
				return []Node{{Key: "domain.x.node", Phase: PhaseBootstrap}}, nil
			},
			want: "framework-owned",
		},
		{
			name: "declare error wrapped with domain name",
			declare: func(context.Context, PlanScope) ([]Node, error) {
				return nil, errors.New("no proto")
			},
			want: `domain "greet": declare: no proto`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := writePlanConfig(t, "declare-bad", "")
			_, err := Compile(context.Background(), ConfigFile(cfgPath),
				Domain{Name: "greet", Declare: tc.declare})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestDomain_DeclareDuplicateKeyNeedsOverride(t *testing.T) {
	cfgPath := writePlanConfig(t, "declare-dup",
		"database:\n  sql:\n    driver: \"sqlite\"\n    source: \"file::memory:?cache=shared&_fk=1\"\n")
	dom := Domain{
		Name: "greet",
		Declare: func(context.Context, PlanScope) ([]Node, error) {
			return []Node{{Key: "database.sql", Phase: PhaseWire}}, nil
		},
	}
	_, err := Compile(context.Background(), ConfigFile(cfgPath),
		fixedNodeProvider([]string{"database.sql"},
			Node{Key: "database.sql", Kind: "database-impl", Phase: PhaseBootstrap, Priority: PriorityDatabase}),
		dom)
	if err == nil || !strings.Contains(err.Error(), "Override") {
		t.Fatalf("err = %v, want duplicate-key override guidance", err)
	}
}
