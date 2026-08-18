// Package config 文档
//
// # 配置加载优先级（高 -> 低，viper 默认语义，面向 K8s/容器部署）
//
//  1. 命令行 flag（--config 之外的业务 flag，通过 BindPFlags 绑定，最高优先级）
//  2. 环境变量（前缀 NAME_，如 BALD_HTTP_ADDR，容器化部署下运维最常用来覆盖配置）
//  3. 本地配置文件（--config 显式指定，或 ./{name}.yaml / ./{name}-{env}.yaml）
//  4. 远程配置中心（远程作为最底层基准）
//
// viper 的 override 层（flag / env）压过底层 config（远程 + 本地），
// 其中 flag 优先级高于 env；底层内部远程先写、本地后写（后写赢），
// 因此本地覆盖远程。最终顺序：flag > env > 本地文件 > 远程。
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
