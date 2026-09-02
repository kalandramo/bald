//go:build nacos

// Command bald 的 nacos 后端接线示例（建在独立 build tag 下，默认不参与编译）。
//
// 为什么要隔离：nacos SDK（nacos-sdk-go v2 / v1）较重，且默认构建用一个 nacos
// server 才能跑起来；用 build tag 隔开后，默认 `go run ./_example/bald` 不引入
// 这些依赖、无需 nacos 进程即可演示，而需要真实 nacos 时再显式开启：
//
//	go run -tags nacos ./_example/bald            # 配置随示例自带，自动加载
//
// 设计关键（保持桥接、不移植代码）：
//   - 注册中心：registry.FromKratos(nacos.New(cli)) 把 kratos contrib 的 nacos
//     后端（实现 kratos registry.Registrar）适配成 bald 的 registry.Registrar，
//     appkit.Registrar 直接消费，零核心改动。
//   - 配置中心：config.FromKratosSource(nacosconfig.NewConfigSource(cli)) 同理，
//     把 kratos contrib 的 nacos config（实现 kratos config.Source）适配成
//     bald 的 config.RemoteSource，由 appkit.RemoteConfig 接入四源合并。
//
// 注意版本分裂（移植到核心才需要担心，桥接模式下由 example 各自 go get 解决）：
//   - 注册中心后端：github.com/go-kratos/kratos/v3/contrib/registry/nacos/v3
//     依赖 nacos-sdk-go/v2（naming_client.INamingClient）。
//   - 配置中心后端：github.com/go-kratos/kratos/v3/contrib/config/nacos/v3
//     依赖 nacos-sdk-go（旧版 v1，config_client.IConfigClient）。
//   两个 SDK 共存于 example module 的 go.sum，互不冲突。
package main

import (
	"github.com/nacos-group/nacos-sdk-go/clients"
	"github.com/nacos-group/nacos-sdk-go/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/clients/naming_client"
	"github.com/nacos-group/nacos-sdk-go/common/constant"
	"github.com/nacos-group/nacos-sdk-go/vo"

	"github.com/kalandramo/bald/pkg/appkit"
	"github.com/kalandramo/bald/pkg/config"
	"github.com/kalandramo/bald/pkg/registry"
	nacosreg "github.com/go-kratos/kratos/v3/contrib/registry/nacos/v3"
	nacosconfig "github.com/go-kratos/kratos/v3/contrib/config/nacos/v3"
)

// nacosServerConfigs 是 nacos server 地址（按需改成本地/生产地址）。
var nacosServerConfigs = []constant.ServerConfig{
	*constant.NewServerConfig("127.0.0.1", 8848),
}

// nacosClientConfig 是 nacos 客户端配置（NamespaceId 区分环境，如 prod/dev）。
var nacosClientConfig = constant.ClientConfig{
	NamespaceId: "public", // 生产通常改成具体命名空间 ID
	TimeoutMs:   5000,
	LogDir:      "/tmp/nacos/log",
	CacheDir:    "/tmp/nacos/cache",
	LogLevel:    "info",
}

// newNacosNamingClient 构造注册中心用的 naming client（nacos-sdk-go/v2）。
func newNacosNamingClient() (naming_client.INamingClient, error) {
	return clients.NewNamingClient(vo.NacosClientParam{
		ClientConfig:  &nacosClientConfig,
		ServerConfigs: nacosServerConfigs,
	})
}

// newNacosConfigClient 构造配置中心用的 config client（nacos-sdk-go v1）。
func newNacosConfigClient() (config_client.IConfigClient, error) {
	return clients.NewConfigClient(vo.NacosClientParam{
		ClientConfig:  &nacosClientConfig,
		ServerConfigs: nacosServerConfigs,
	})
}

// nacosRegistrar 返回桥接后的 bald 注册中心；失败时 panic（fail-fast，启动期暴露）。
func nacosRegistrar() registry.Registrar {
	cli, err := newNacosNamingClient()
	if err != nil {
		panic("nacos naming client: " + err.Error())
	}
	// nacosreg.New 接收 naming_client.INamingClient；WithGroup/WithCluster 等
	// 选项对齐 kratos nacos 语义（默认 group=DEFAULT_GROUP，cluster=DEFAULT）。
	return registry.FromKratos(nacosreg.New(cli,
		nacosreg.WithGroup("DEFAULT_GROUP"),
		nacosreg.WithCluster("DEFAULT"),
	))
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

// applyNacosBackends 把 nacos 的注册中心 + 配置中心注入 AppKit。
//
// AppKit 的 Registrar 与 RemoteConfig 都接受（经桥接的）bald 抽象，
// 因此这里只是把具体后端交进去，核心无需感知 nacos 的存在。
func applyNacosBackends(app *appkit.AppKit) {
	appkit.Registrar(nacosRegistrar())(app)
	appkit.RemoteConfig(nacosConfigSource())(app)
}
