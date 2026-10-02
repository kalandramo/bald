// Package appkit 是 bald 框架的组合层（App 层），负责多服务器生命周期编排。
//
// 设计融合三方之长：
//   - onexstack/pkg/app 的 Options + 配置理念（启动期由调用方注入 --config；
//     本包经配置装载器子包 github.com/kalandramo/bald/bootstrap/config 实现四源
//     合并（bconfig/file 源 + map 深合并内核），额外支持远程配置中心与统一热更新
//     回调）；
//   - Kratos 的 transport.Server 契约与 registry.Registrar 接口（可插拔复用）；
//   - go-lulu 的自研 App 层精髓：errgroup 并发启停、优雅停机防坑（Stop 传入未取消
//     ctx 使 stopTimeout 生效）、崩溃级联停止、Run 防重入、可观察通道、Endpoint 动态
//     端口注册。
//
// 启动期配置（onexstack 风格 + 远程）：
//
//	AppKit 在 Run 的最早阶段调用 loadConfig：读取 --config 本地文件（yaml/json）、
//	环境变量（前缀 NAME_）、业务 flag（仅显式传入的参与合并）、可选远程配置中心
//	（etcd/consul/nacos 等，经 config.RemoteSource 抽象接入，推荐用
//	config.FromKratosSource 桥接 kratos contrib 后端）。优先级 flag > env > 本地 >
//	远程（远程作为基准），本地文件与远程变更触发全量重合并 + 热更新（OnConfigChange）。
//	配置结果存于配置仓库（*config.Store），调用方可在 BeforeStart 钩子里通过
//	app.Settings() 取快照或 app.Config() 实时点路径读取。
//
//	业务 flag 必须经 Bind(prefix, opt) 注册（而非自行 AddFlags 到 pflag.CommandLine），
//	否则 flag 不进入配置装载的 flag 层，压不过环境变量、本地文件与远程基准。
//	prefix 用配置键前缀（如 "http"），使 --http.addr / BALD_DEMO_HTTP_ADDR /
//	配置文件 http.addr 三者键路径一致。
//
//	配置契约推荐用 Protobuf：bconf.UnmarshalMap(app.Settings(), bconf.NewBootstrap())，
//	详见 docs/devel/zh-CN/Bald 配置契约设计.md。
//
//	注意：onexstack 原 AddConfigFlag 仅支持本地文件 + 环境变量，并不支持远程配置中心；
//	bald 在同样风格上补齐了 RemoteSource 抽象与统一热更新钩子。
//
// 关键契约（见 appkit_test.go 的回归测试）：
//   - BUG-1：stopAll 传入未取消的 ctx，stopTimeout 才真正生效；
//   - BUG-3：任一服务器 Start 崩溃，其余服务器被级联停止；
//   - 防重入：重复 Run 返回 ErrAlreadyRunning；
//   - 可观察：Run 结束后 Done() 关闭，Err() 反映退出错误。
//
// # 扩展机制的职责边界（何时用哪个）
//
// AppKit 有四组「可组合性原语」（T1/S1/C1/A1+R1，见
// docs/devel/zh-CN/架构优化路线.md）。它们与既有生命周期钩子易混淆，
// 下表给出选择判据：
//
//	| 你要做的事                         | 用哪个                           | 为什么                                      |
//	|------------------------------------|----------------------------------|---------------------------------------------|
//	| 改共享全局状态（SetLogger/注册表） | Effect(T1)                       | 登记**逆操作**；停机阶段 0 逆序回放，失败亦可回滚 |
//	| 建/关一个有生命周期的实例          | Components(C1) / ComponentFunc   | 顺序 Start + 逆序 Dispose；panic 隔离、独立超时  |
//	| 声明启动前依赖的能力               | Provides / Requires(S1)          | Resolve 启动期 fail-fast，替代运行时 nil panic  |
//	| 运行期动态挂载/卸载                | MountComponent / Registry.Mount(A1) | 可逆；装配期 Register 只增不减（重名报错）  |
//	| 配置变更时对个别 key 做定点响应    | OnKeyChange(R1)                  | 仅该 key 真变化才触发；无需全量 Unmarshal       |
//	| 让实际态收敛到配置声明的期望态     | Reconcile(R1-2)                  | diff-apply，失败下次补齐（K8s controller 语义） |
//	| 纯生命周期时机（无逆操作、无实例） | BeforeStart/AfterStart/...       | 只需「在某个时刻跑一段代码」时的最轻选择         |
//
// 判据归纳：
//   - 有**逆操作**要登记 → Effect（账本管全局注册表的撤销，低级/细粒度）；
//   - 有**实例**要建关 → Component（管生命周期，高级/结构化）；
//   - 只是**时机**（不写全局、不建实例）→ 生命周期钩子；
//   - 变更来源是**配置** → R1（key 级）/ R1-2（期望态）。
//
// 钩子 vs 组件：钩子是一次性回调（运行后无状态）；组件有 name 与
// Start/Dispose 对，进入停机序列、可 ListComponents 观测、可运行期热插拔。
// Effect 与 Component 互补：前者撤销「全局注册表写入」，后者释放「实例资源」。
package appkit
