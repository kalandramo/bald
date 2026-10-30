//go:build nacos

// Command bald-demo 的 nacos 后端接线示例（建在独立 build tag 下，默认不参与编译）。
//
// 为什么要隔离：nacos SDK 较重，且默认构建用一个 nacos server 才能跑起来；
// 用 build tag 隔开后，默认 `go run ./cmd/bald-demo` 不引入这些依赖、
// 无需 nacos 进程即可演示，而需要真实 nacos 时再显式开启：
//
//	go run -tags nacos ./cmd/bald-demo            # 配置随示例自带，自动加载
//
// 注册中心（已迁移段驱动 Provider 装配）：本文件只做两件事——把 nacos 契约
// provider 包成 registry 段的 appkit.Provider（原 RegistrarRegistry 注入的
// Provider 等价形态）+ 经 Providers 传入装配；具体连接参数全部来自
// configs/bald-demo-api.yaml 的 registry 段（契约驱动，零硬编码）。
//
// 注：示例装配层始终声明 Registrar(inmemory.New())（显式实例优先于契约
// 装配——引擎跳过纯 registry 段 Provider），故本 Provider 在示例默认选项下
// 不会构建；去掉 Registrar 后本段即按 yaml registry.nacos 驱动真实注册
// （语义见 appkit.Registrar 注释）。
//
// 配置中心（仍是桥接）：config.FromKratosSource(nacosconfig.NewConfigSource(cli))
// 把 kratos contrib 的 nacos config（实现 kratos config.Source）适配成
// bald 的 config.RemoteSource，由 RemoteConfig 接入四源合并。
//
// 注意版本分裂（仅配置中心桥接涉及）：contrib config/nacos/v3 依赖
// nacos-sdk-go 旧版 v1；注册中心已走直连 SDK v2 provider，无 v1 依赖。
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

// nacosHost/nacosPort 是配置中心（桥接路径）的 nacos server 地址。
// 注册中心的地址已契约化：见 configs/bald-demo-api.yaml registry.nacos 段。
var nacosHost, nacosPort = "127.0.0.1", uint64(8848)

// nacosRegistrarProvider 是 registry 段的 nacos 后端 Provider：按契约
// registry 段构造 nacos Registrar，产物（registry.Registrar）由引擎落
// 资源容器。第三方注册中心接入的 Provider 等价形态（原「构造注册表 +
// MustRegister + WithRegistrarRegistry」三步收敛为一个 Provider 值）。
//
// 未 import 的后端（etcd/consul/kubernetes）零依赖零编译成本；本 Provider
// 只服务 type=nacos，其余 type 显式报错（单后端 Provider 的自描述边界）。
type nacosRegistrarProvider struct{}

func (nacosRegistrarProvider) Segments() []string { return []string{"registry"} }

// Apply 把自身追加进声明（Provider 即 Option）。
func (nacosRegistrarProvider) Apply(d *appkit.Decl) {
	d.Providers = append(d.Providers, nacosRegistrarProvider{})
}

// Plan 声明 registry 节点：段路径 registry、引导阶段、注册中心优先级；
// 显式 Registrar 实例接管时引擎在计划期跳过本 Provider。
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
		return nil, nil, nil // 段缺失：跳过（引擎仅在段存在时调用，防御性）
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

// newNacosConfigClient 构造配置中心用的 config client（nacos-sdk-go v1，
// contrib config/nacos/v3 消费 v1 接口）。
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

// nacosConfigSource 返回桥接后的 bald 远程配置源；失败时 panic。
func nacosConfigSource() config.RemoteSource {
	cli, err := newNacosConfigClient()
	if err != nil {
		panic("nacos config client: " + err.Error())
	}
	// dataID 带扩展名（.yaml）以便 contrib 正确识别格式；group 与上述一致。
	return config.FromKratosSource(nacosconfig.NewConfigSource(cli,
		nacosconfig.WithDataID("bald-demo.yaml"),
		nacosconfig.WithGroup("DEFAULT_GROUP"),
	))
}

// nacosBootstrapOptions 返回 nacos 的注册中心 + 配置中心装配选项。
//
// 注册中心走段驱动 Provider（registry 段驱动，显式 Registrar 实例优先）；
// 配置中心仍是运行时桥接注入（RemoteConfig，四源合并的远程基准层）。
func nacosBootstrapOptions() []appkit.Option {
	return []appkit.Option{
		nacosRegistrarProvider{},
		appkit.RemoteConfig(nacosConfigSource()),
	}
}
