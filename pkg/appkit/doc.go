// Package appkit 是 bald 框架的组合层（App 层），负责多服务器生命周期编排。
//
// 设计融合三方之长：
//   - onexstack/pkg/app 的 Options + 配置理念（启动期由调用方注入 --config/viper；
//     本包通过 pkg/config 扩展，额外支持远程配置中心与统一热更新回调）；
//   - Kratos 的 transport.Server 契约与 registry.Registrar 接口（可插拔复用）；
//   - go-lulu 的自研 App 层精髓：errgroup 并发启停、优雅停机防坑（Stop 传入未取消
//     ctx 使 stopTimeout 生效）、崩溃级联停止、Run 防重入、可观察通道、Endpoint 动态
//     端口注册。
//
// 启动期配置（onexstack 风格 + 远程）：
//   AppKit 在 Run 的最早阶段调用 loadConfig：读取 --config 本地文件（yaml/json/toml
//   等）、环境变量（前缀 NAME_）、可选远程配置中心（etcd/consul/nacos 等，经
//   config.RemoteSource 抽象接入，推荐用 config.FromKratosSource 桥接 kratos contrib
//   后端）。远程作为基准、本地文件覆盖远程，并支持本地文件与远程热更新（OnConfigChange）。
//   配置结果存于 a.v，调用方可在 BeforeStart 钩子里通过 app.Viper() 读取。
//
//   业务 flag 必须经 Bind(prefix, opt) 注册（而非自行 AddFlags 到 pflag.CommandLine），
//   否则 flag 不进入 viper override 层，压不过环境变量、本地文件与远程基准。
//   prefix 用配置键前缀（如 "http"），使 --http.addr / BALD_DEMO_HTTP_ADDR /
//   配置文件 http.addr 三者键路径一致。
//
//   配置契约推荐用 Protobuf：config.Unmarshal(app.Viper(), conf.NewBootstrap())，
//   详见 docs/devel/zh-CN/proto 配置契约设计.md。
//
//   注意：onexstack 原 AddConfigFlag 仅支持本地文件 + 环境变量，并不支持远程配置中心；
//   bald 在同样风格上补齐了 RemoteSource 抽象与统一热更新钩子。
//
// 关键契约（见 appkit_test.go 的回归测试）：
//   - BUG-1：stopAll 传入未取消的 ctx，stopTimeout 才真正生效；
//   - BUG-3：任一服务器 Start 崩溃，其余服务器被级联停止；
//   - 防重入：重复 Run 返回 ErrAlreadyRunning；
//   - 可观察：Run 结束后 Done() 关闭，Err() 反映退出错误。
package appkit
