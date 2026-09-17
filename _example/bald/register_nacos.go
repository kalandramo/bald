//go:build nacos

// Command bald 的 nacos 后端接线示例（建在独立 build tag 下，默认不参与编译）。
//
// 为什么要隔离：nacos SDK 较重，且默认构建用一个 nacos server 才能跑起来；
// 用 build tag 隔开后，默认 `go run ./_example/bald` 不引入这些依赖、
// 无需 nacos 进程即可演示，而需要真实 nacos 时再显式开启：
//
//	go run -tags nacos ./_example/bald            # 配置随示例自带，自动加载
//
// 注册中心（已迁移契约装配）：本文件只做两件事——把 nacos 契约 provider
// 显式注册进 RegistrarRegistry + 交 WithRegistrarRegistry；具体连接参数
// 全部来自 configs/bald-demo.yaml 的 registry 段（契约驱动，零硬编码）。
//
// 配置中心（仍是桥接）：config.FromKratosSource(nacosconfig.NewConfigSource(cli))
// 把 kratos contrib 的 nacos config（实现 kratos config.Source）适配成
// bald 的 config.RemoteSource，由 WithRemoteConfig 接入四源合并。
//
// 注意版本分裂（仅配置中心桥接涉及）：contrib config/nacos/v3 依赖
// nacos-sdk-go 旧版 v1；注册中心已走直连 SDK v2 provider，无 v1 依赖。
package main

import (
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
// 注册中心的地址已契约化：见 configs/bald-demo.yaml registry.nacos 段。
var nacosHost, nacosPort = "127.0.0.1", uint64(8848)

// registrarRegistry 显式注册 nacos 契约 provider。
//
// 未 import 的后端（etcd/consul/kubernetes）零依赖零编译成本；
// 契约 registry.type 指向未注册后端时构造期 fail-fast。
func registrarRegistry() *appkit.RegistrarRegistry {
	rr := appkit.NewRegistrarRegistry()
	rr.MustRegister(nacoscontract.Type, nacoscontract.Provider)
	return rr
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
// 注册中心走契约装配（registry 段驱动）；配置中心仍是运行时桥接注入。
func nacosBootstrapOptions() []appkit.BootstrapOption {
	return []appkit.BootstrapOption{
		appkit.WithRegistrarRegistry(registrarRegistry()),
		appkit.WithRemoteConfig(nacosConfigSource()),
	}
}
