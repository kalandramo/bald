//go:build nacos

package bootstrap

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/bconfig/nacos"
	"github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/pkg/appkit"

	nacoscontract "github.com/kalandramo/bald/registry/nacos/contract"
)

// 配置中心（远程源）的坐标：整文档 dataID，与注册中心同一台 nacos。
// 注册中心地址走契约 registry.nacos 段；这一段留在代码里是刻意的——见下。
const (
	nacosConfigAddr   = "127.0.0.1:8848"
	nacosConfigDataID = "bald-demo.yaml"
	nacosConfigGroup  = "DEFAULT_GROUP"
	nacosConfigNS     = "public"
)

// nacos 变体：注册中心段驱动 Provider + 配置中心远程源。两角色都生效。
func init() {
	variantOpts["nacos"] = nacosBootstrapOptions()
}

// nacosRegistrarProvider 是 registry 段的 nacos 后端 Provider；显式 Registrar
// 实例接管时引擎在计划期跳过本 Provider。
type nacosRegistrarProvider struct{}

func (nacosRegistrarProvider) Segments() []string { return []string{"registry"} }

func (nacosRegistrarProvider) Apply(d *appkit.Decl) {
	d.Providers = append(d.Providers, nacosRegistrarProvider{})
}

func (nacosRegistrarProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
	return []appkit.Node{{
		Key:        "registry",
		Kind:       "registry",
		Segments:   []string{"registry"},
		Phase:      appkit.PhaseBootstrap,
		Priority:   appkit.PriorityRegistry,
		Reload:     appkit.ReloadRestart,
		EffectName: "appkit:registrar-client",
	}}, nil
}

func (nacosRegistrarProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
	rcfg := s.Config.GetRegistry()
	if rcfg == nil {
		return nil, nil, nil
	}
	if rcfg.GetType() != nacoscontract.Type {
		return nil, nil, fmt.Errorf("nacos: registry.type = %q, want %q (this provider only assembles nacos)", rcfg.GetType(), nacoscontract.Type)
	}
	reg, cleanup, err := nacoscontract.Provider(ctx, rcfg)
	if err != nil {
		return nil, nil, err
	}
	return reg, func(context.Context) error {
		if cleanup != nil {
			cleanup()
		}
		return nil
	}, nil
}

// nacosConfigLayer 构造配置中心的整文档源层（仓内 bconfig/nacos，自建 client
// 惰性建连；nacos-sdk-go/v2 无显式关闭接口，层不需要 cleanup）。
//
// 为什么不走契约 config.nacos 段（那半条路是 contrib/config-nacos 的
// AppProvider）：本示例的两个构建形态共用同一份 configs/*.yaml，而契约的
// config 段只有 config 段 Provider 才有消费者——实测默认（无 tag）构建带着
// config.nacos 直接起不来：
//
//	appkit: bootstrap config declares sections with no consumer: config
//
// 把 AppProvider 无条件接进默认构建又能消掉这条错误，但那时默认构建就非得
// 有一个活的 nacos 才装载得了配置，与「克隆即可跑」的最小路径冲突。所以示例
// 只在变体内自己造层；契约段留给真有部署（那里的 config 段本来就有消费者）。
func nacosConfigLayer() config.Layer {
	src, err := nacos.New(
		nacos.WithServerAddrs(nacosConfigAddr),
		nacos.WithNamespace(nacosConfigNS),
		nacos.WithGroup(nacosConfigGroup),
		nacos.WithDataID(nacosConfigDataID),
	)
	if err != nil {
		panic("nacos config source: " + err.Error())
	}
	// Watch=true：nacos 的 ListenConfig 推送参与热更新（变更 → 全量重合并）。
	// 层优先级在本地文件/env/flag 之下，与原先的远程桥（最低基准层）同向。
	return config.Layer{Name: "nacos", Reader: src, Format: "yaml", Watch: true}
}

func nacosBootstrapOptions() []appkit.Option {
	return []appkit.Option{
		nacosRegistrarProvider{},
		appkit.ConfigLayers(nacosConfigLayer()),
	}
}
