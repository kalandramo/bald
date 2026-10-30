//go:build nacos

package bootstrap

import (
	"context"
	"fmt"

	"github.com/nacos-group/nacos-sdk-go/clients"
	config_client "github.com/nacos-group/nacos-sdk-go/clients/config_client"
	v1constant "github.com/nacos-group/nacos-sdk-go/common/constant"
	v1vo "github.com/nacos-group/nacos-sdk-go/vo"

	nacosconfig "github.com/go-kratos/kratos/v3/contrib/config/nacos/v3"
	"github.com/kalandramo/bald/bootstrap/config"
	"github.com/kalandramo/bald/pkg/appkit"

	nacoscontract "github.com/kalandramo/bald/registry/nacos/contract"
)

// 配置中心（桥接路径）的 nacos server 地址；注册中心地址走契约 registry.nacos 段。
var nacosHost, nacosPort = "127.0.0.1", uint64(8848)

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

// newNacosConfigClient 构造配置中心 client（contrib config/nacos/v3 消费 v1 接口）。
func newNacosConfigClient() (config_client.IConfigClient, error) {
	return clients.NewConfigClient(v1vo.NacosClientParam{
		ClientConfig: &v1constant.ClientConfig{
			NamespaceId: "public",
			TimeoutMs:   5000,
			LogDir:      "/tmp/nacos/log",
			CacheDir:    "/tmp/nacos/cache",
			LogLevel:    "info",
		},
		ServerConfigs: []v1constant.ServerConfig{*v1constant.NewServerConfig(nacosHost, nacosPort)},
	})
}

func nacosConfigSource() config.RemoteSource {
	cli, err := newNacosConfigClient()
	if err != nil {
		panic("nacos config client: " + err.Error())
	}
	return config.FromKratosSource(nacosconfig.NewConfigSource(cli,
		nacosconfig.WithDataID("bald-demo.yaml"),
		nacosconfig.WithGroup("DEFAULT_GROUP"),
	))
}

func nacosBootstrapOptions() []appkit.Option {
	return []appkit.Option{
		nacosRegistrarProvider{},
		appkit.RemoteConfig(nacosConfigSource()),
	}
}
