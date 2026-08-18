// Package config 文档
//
// # 配置加载优先级（高 -> 低）
//
//  1. 命令行 flag（--config 之外的业务 flag，通过 BindPFlags 绑定）
//  2. 本地配置文件（--config 显式指定，或 ./{name}.yaml / ./{name}-{env}.yaml）
//  3. 环境变量（前缀 NAME_，如 BALD_HTTP_ADDR）
//  4. 远程配置中心（远程作为基准，本地文件覆盖远程）
//
// 注意：viper 的合并策略是“后读取的覆盖先读取的”，而 BindPFlags 绑定的是
// 引用，flag 一旦被设置就永远最高。因此上面优先级成立。远程先注入（基准），
// 本地文件后注入（覆盖），从而实现“远程基准 + 本地覆盖”。
//
// # 远程配置中心接入
//
// 通过 config.RemoteSource 接口指定后端。推荐用 config.FromKratosSource 桥接
// kratos contrib 的 config.Source（etcd/consul/nacos/apollo 等），直接复用其实现：
//
//	import etcdconfig "github.com/go-kratos/kratos/v3/contrib/config/etcd/v3"
//
//	src := config.FromKratosSource(etcdconfig.New(client, etcdconfig.WithPath("/config/demo/prod.yaml")))
//	appkit.RemoteConfig(src)
//
// 也可自行实现 config.RemoteSource（Read + Watch），格式由后端自声明，
// 远程字节经 v.SetConfigType + v.ReadConfig 注入 viper，绕开 viper 标准
// remote 的「强制 JSON / watch 不可靠 / 无鉴权」缺陷。
//
// # 为什么不用 viper 标准 remote
//
// viper 的 AddRemoteProvider / WatchRemoteConfigOnChannel 强制 JSON、watch 不可靠、
// 无鉴权透传，生产不可用。因此本包改用自研 RemoteSource 抽象 + 手动注入 viper，
// 详见 docs/config-center-design.md。
package config
