# AppKit FromBootstrap 约定装配

> 2026-09-06 新增。`pkg/appkit/bootstrap.go`（契约约定装配入口）+ `bootstrap_test.go`。
> 纯增量：不改动 `New`/`Option` 既有语义，e2e 的 newApp+Option 覆盖机制不受影响。

## 动机

`appkit.New` 是显式 Option 装配，业务 main.go 需要手写 Bind×3、BeforeStart 装载+
校验+重建 Logger、OnConfigChange 再装载等样板（示例 main.go 曾达 500 行）。
这些样板对应的「形状」其实早已在契约 `BootstrapConfig` 里（app/server/logger/config
段），属于框架该内化的约定。

## 分工边界（配置驱动参数，代码声明能力）

| 配置驱动（契约 / flag / env / 文件） | 代码声明（配置表达不了） |
|---|---|
| app 元数据（id/name/version/env/stop_timeout） | gin 路由与业务 handler |
| server 地址 / TLS / 停机超时 | gRPC service 注册 |
| 日志级别/格式/输出（热更新即改即生效） | 拦截器链序（安全策略：ErrorInterceptor 最外层） |
| 注册中心实例 / 配置源层 | 就绪探针的下游依赖 |
| 热更新开关 | 日志装饰器（脱敏）、gateway 转码注册回调 |

原则：**能力声明在代码**。契约有 server.grpc 段但业务未 `WithGRPC` 时，对应 flag
变更不产生效果（没有 server 消费）——这是刻意的，避免「配置说开了、没人实现」
的静默失效（与 S1 能力声明 fail-fast 同哲学）。

## FromBootstrap 内化的约定

1. **App 元数据**取自契约 app 段（空值回退默认：name=bald-app、version=v0.0.0、
   stopTimeout=30s）。
2. **Bind**：`server.http`/`server.grpc` 段非 nil 即 Bind（flag 可覆盖契约）；
   `--log.*` flag 壳（`Bind("", LogOptions(nil))`）只注册定义，终值以契约为准。
3. **日志两阶段**：阶段 A（构造期，默认 Logger，保证契约装载前有日志）→
   阶段 B（BeforeStart 装载+校验后按契约 logger 段重建）→ 停机经 Effect 恢复
   原 Logger 并释放后端。业务装饰器（脱敏）阶段 A/B 统一生效。
4. **ConfigRegistry**：契约 Config 段经 `bootstrap.Registry.Build` 产出配置层
   （注册序=层优先级），cleanup 挂 Effect 停机释放。
5. **BeforeStart**：Settings→Unmarshal→Validate→rebuildLogger。
6. **服务器构造走 `bootstrap.ServerRegistry`**：能力声明（WithHTTP/WithGRPC/
   WithGatewayRegister）→ provider 注入，契约 server 段 → `BuildServers` 装配，
   与直用 bootstrap 的路径同一实现（消除双真相源）。provider cleanup 挂
   Effect（"appkit:servers"）；BuildServers 失败回滚阶段 A 与配置层。
7. **OnConfigChange（热更新）**：副本试装载（proto.Clone）+ Validate + 整契约
   原子落盘（`*cfg = *candidate`，字段是子消息指针、整体替换无别名问题）+
   重建 Logger。坏配置降级记日志、保留旧契约（与审计旁路同哲学）。
   ——Unmarshal 只做结构转换不校验取值，坏值必须由 Validate 拦在落盘之前
   （单测 TestHotReload_BadConfigKeepsOld 钉死该语义）。

## API 速查

```go
app, err := appkit.FromBootstrap(bootstrap,
    appkit.WithHTTP(router),                       // 业务 handler 必供
    appkit.WithGRPC(register, serverOpts...),      // service + 完整拦截器链
    appkit.WithReadiness(ready),                   // 缺省恒就绪
    appkit.WithRegistrar(inmemory.New()),
    appkit.WithConfigFile("configs/bald-demo.yaml"),
    appkit.WithWatchConfig(true),
    appkit.WithLogDecorators(deco...),             // 脱敏等
    appkit.WithAfterStart(fn),
    appkit.WithGatewayRegister(fn),                // gateway 转码能力（driver=grpc-gateway 选网关面）
    appkit.WithConfigRegistry(reg),                // 契约 Config 段层装配
    appkit.WithRemoteConfig(src),                  // kratos 桥远程源
    appkit.WithLoggerFactory(f),                   // 换日志后端（默认 slog）
    appkit.WithLogRegistry(reg),                   // 契约驱动日志后端（logger.type 查表；log/<backend>/contract 注册）
)
```

失败语义：`cfg=nil` / 能力声明与契约段缺失不匹配 → 构造期 fail-fast 返回 error；
`WithGatewayRegister` 与 `server.http.driver` 冲突（非 grpc-gateway 值，或与
WithHTTP 同时声明且留空）→ 构造期 fail-fast（显式 > 隐式）；ConfigRegistry
Build / BuildServers 失败 → 回滚阶段 A Logger 与配置层后返回 error。

### 网关转码面（gateway as driver 模式）

gateway 不是独立服务器，而是 `server.http` 段的一种模式，由契约
`server.http.driver` 驱动装配（复用 `HttpServerProvider` 的模式分支）：

- 声明 `WithGatewayRegister(fn)` + driver=`grpc-gateway`（或留空）→ HTTP
  端口即网关转码面（REST→gRPC），业务 handler 被替代；
- 与 `WithHTTP` 同时声明时 driver 必须显式为 `grpc-gateway`（留空会让两个
  能力静默竞争同一端口，fail-fast）；driver 为其他值 → 转码能力无消费面，fail-fast；
- 业务要混合路由时在 fn 里返回组合 handler（如 gin 主面 + NoRoute 落转码 mux）；
- `WithExtraServers` 保留为通用逃生舱（契约形状表达不了的服务器），
  gateway 不再走它。

## 示例改造结果（_example/bald）

main.go 从 ~560 行降至 ~340 行（剩余几乎全是业务路由/handler 与教学注释），
装配段收缩为 newApp 内一组 BootstrapOption。HTTP 面按构建分叉：默认构建走
gin 演示路由，grpcgw 构建走网关转码面（`WithGatewayRegister` + yaml
`server.http.driver: grpc-gateway`）。e2e 为 `newApp(bootstrap, ready)`，
与生产共用同一构造函数（「测的=跑的」由函数复用保证，不再靠注释自觉）。

## 已知耦合与坑

- **env 前缀耦合 app.name**：bconfig Store 的 env 层用 `environMap(Name)` 生成
  前缀（bald-demo → BALD_DEMO_*）。Name 来自契约 app.name（构造期可见值），
  业务自定义 env 前缀必须**在 FromBootstrap 之前**设好契约 name
  （示例在 newApp 开头设 `bootstrap.GetApp().Name = "bald-demo"`）。
  漏设的症状：env 覆盖静默失效、文件值压过测试注入值（e2e 曾因此挂）。
- **example go.mod**：`pkg/middleware/gin` 传递依赖 `bald-crud/viewer`（嵌套
  module），example 需 `replace => ../../../bald-crud/viewer`。
- **nacos tag 的 SDK 版本分裂**：contrib registry/nacos/v3 用 v2 SDK（naming），
  contrib config/nacos/v3 用 v1 SDK（config）。两个 SDK 共存，server/client
  配置各用各的类型；register_nacos.go 已按此修正（原文件 naming client 误用
  v1 import，`-tags nacos` 本就编译不过）。

## 验证

- 单测 10 个（appkit/bootstrap_test.go）：元数据契约驱动、nil/段缺失 fail-fast、
  服务器构造数量、gateway driver 语义（段缺失/单声明默认转码面/双声明留空
  fail-fast/显式 grpc-gateway/其他值 fail-fast）、日志两阶段生命周期+停机恢复、
  ConfigRegistry 层装载+cleanup、双服务器动态端口 Run、热更新重建/坏配置保留。
- e2e（grpcgw tag）全绿：REST 面即 server.http 端口（网关转码面），与 gRPC
  共享同一份校验规则；默认/grpcgw/nacos 三种 tag 组合编译通过。
