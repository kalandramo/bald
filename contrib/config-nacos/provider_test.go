package confignacos

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	baldconfig "github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/pkg/appkit"
)

func TestAppProvider_Plan(t *testing.T) {
	var d appkit.Decl
	AppProvider().Apply(&d)
	if len(d.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(d.Providers))
	}
	nodes, err := d.Providers[0].Plan(context.Background(), appkit.PlanScope{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("nodes = %d, want 1", len(nodes))
	}
	n := nodes[0]
	if n.Phase != appkit.PhaseConfigure || n.Reload != appkit.ReloadRestart || n.Key != "config" {
		t.Fatalf("node = {key:%q phase:%s reload:%s}, want config/configure/restart", n.Key, n.Phase, n.Reload)
	}
}

func TestAppProvider_Build(t *testing.T) {
	p := AppProvider()

	// 未配置 config.nacos：空层列表，不报错（config 段无条件构建）。
	product, cleanup, err := p.Build(context.Background(),
		appkit.ApplyScope{Config: &bootstrapv1.BootstrapConfig{}, Decl: &appkit.Decl{}}, appkit.Node{})
	if err != nil {
		t.Fatalf("absent section: %v", err)
	}
	if layers, ok := product.([]baldconfig.Layer); !ok || len(layers) != 0 {
		t.Fatalf("product = %#v, want empty []config.Layer", product)
	}
	if cleanup != nil {
		t.Fatalf("cleanup = %v, want nil", cleanup)
	}

	// 配置齐全：一个 Watch 层，format 透传（构造不建连）。
	product, cleanup, err = p.Build(context.Background(), appkit.ApplyScope{
		Config: &bootstrapv1.BootstrapConfig{Config: &bootstrapv1.Config{Nacos: &bootstrapv1.Config_Nacos{
			ServerAddrs: []string{"127.0.0.1:8848"},
			Namespace:   "ns",
			Group:       "DEFAULT_GROUP",
			DataId:      "app.yaml",
			Format:      "yaml",
		}}},
		Decl: &appkit.Decl{},
	}, appkit.Node{})
	if err != nil {
		t.Fatalf("nacos section: %v", err)
	}
	layers, ok := product.([]baldconfig.Layer)
	if !ok || len(layers) != 1 {
		t.Fatalf("product = %#v, want one layer", product)
	}
	if !layers[0].Watch || layers[0].Format != "yaml" || layers[0].Reader == nil {
		t.Fatalf("layer = %+v, want watch/yaml/reader", layers[0])
	}
	if cleanup != nil {
		if err := cleanup(context.Background()); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
}
