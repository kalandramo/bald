# Provider 接入 - contract.AppProvider 与段 Provider

面向要把一个能力（自研或第三方）接进 bald 装配层的人：标准能力怎么一行接入、整段自持的 Provider 怎么写、段覆盖与停机回收的语义、报错了怎么修。设计论证（装配引擎为什么从 12 段手工流收敛为段驱动 Provider）见 `docs/devel/zh-CN/装配层评估.md` §4.2，本文只讲操作。主线一句话：**能力进装配层的唯一形态是段驱动 Provider——声明消费哪个契约段，引擎在既定阶段调 Build，产物按段路径落统一资源面，cleanup 进停机 Effect 账本。**

## 心智模型：一句话版

**Provider 即 Option：`Segments()` 声明契约段路径（顶层段名或子段路径），`Build` 构建段产物，`Apply` 把自身追加进声明——Provider 值直接作为 `MustServe` 变参项。段缺失 = 跳过（合法）；同段精确路径末者胜；契约里出现而无消费者的顶层段 = 启动期报错（L3，fail-closed）。**

## 1. Provider 接口：两方法 + 一个嵌入

`pkg/appkit/provider.go:93`：

```go
type Provider interface {
    Option                        // Apply 把自身追加进声明——Provider 值可直接作为变参项
    Segments() []string           // 声明消费的契约段路径（非空；空路径装配期报错）
    Build(ctx context.Context, s BuildScope) (any, Cleanup, error)
}
```

- **Segments：段路径即契约段**。顶层段名（"database"、"audit"）或子段路径（"database.sql"、"server.http"），精确匹配；段存在性由 protobuf 反射逐级判定（消息型字段非 nil 即 present）。契约共 15 个顶层段。
- **Option 同款 Apply**：Provider 嵌入 Option——与 `std.Standard()`、业务域声明值并列传入 MustServe（[新增业务域](./新增业务域%20-%20Domain%20与%20gen%20domain.md) §3 同一手法）。`appkit.Providers(ps ...Provider)` 可把多个 Provider 打包为一个 Option（`provider.go:106`）。
- **Build 输入 BuildScope**：Config（阶段 B / server 阶段为已装载定型的完整契约；config 段构造期与 logger 阶段 A 可为 nil）、Decl（只读约定）、App（阶段 B 起非 nil，**前序段产物已填充**——audit 后端经 `App.Resource("database.sql")` 取连接即依赖此序）。
- **Build 产物形状按顶层段约定**：registry 段为 registry.Registrar；六资源域（database/cache/storage/ai/workflow/broker）为 map[string]any（键=子段名）；audit 段为 audit.Auditor；server 段为 []transport.Server；config 段为 []config.Layer；无产物可返回 nil。

## 2. 三种接入粒度

### 2.1 标准能力：直接用 contract.AppProvider()

标准能力包已导出现成 Provider（逐一核对 `func AppProvider` 共 11 处，段路径逐个对过 `Segments()`）：

| 能力 | 构造 | 消费段 |
| --- | --- | --- |
| cache/redis | `rediscontract.AppProvider()` | cache.redis |
| broker/redis | `rediscontract.AppProvider()` | broker.redis |
| registry/nacos、registry/etcd | `contract.AppProvider()` | registry.nacos / registry.etcd |
| contrib/store-gorm | `storegorm.AppProvider(opts ...Option)` | database.sql（Option 透传） |
| transport/{sse,cron,asynq} | `contract.AppProvider(opts ...Option)` | server.sse / server.cron / server.asynq |
| transport/gateway | `contract.AppProvider(register RegisterFunc)` | server.gateway |
| oss/{s3,minio} | `contract.AppProvider()` | storage.s3 / storage.minio |

接入三步（以 redis 缓存为例，与 [Bald 缓存使用](./Bald%20缓存使用.md) 同构）：引依赖 → MustServe 变参接一行 → yaml 声明段：

```go
appkit.MustServe(ctx,
    std.Standard(),
    rediscontract.AppProvider(), // 契约 cache.redis 段 → 资源面
    // ...业务域与其余 Option
)
```

```yaml
cache:
  redis:
    addr: "localhost:6379"
```

以 cache/redis 为例的内部语义：段存在 → 校验 / 建连 / Ping fail-fast（不产半成品实例）→ 产物落资源面 → cleanup 关闭底层连接池。落 contract 包的原因：标准能力包被 root 反向依赖会成环（导出范式：落 contract 包、导出名 AppProvider——既有标识符占用）。

### 2.2 整段自持：自己实现 Provider

范本 `_example/bald/internal/bootstrap/providers.go` 的 loggerProvider（覆盖 std 默认日志通道，行为测试钉住掩码语义）——三件套逐条对账：

```go
func (p loggerProvider) Segments() []string { return []string{"logger"} }              // 段路径

func (p loggerProvider) Apply(d *appkit.Decl) { d.Providers = append(d.Providers, p) } // Option 同款 Apply

func (p loggerProvider) Build(_ context.Context, s appkit.BuildScope) (any, appkit.Cleanup, error) {
    l := s.Config.GetLogger()
    if l == nil { // 阶段 A（契约装载前）：引导 bslog——启动期日志仍带脱敏
        return bslog.New(baldbootstrap.LogOptions(nil), p.deco...), nil, nil
    }
    // 阶段 B/热更：按 logger.backends 逐项构造（非 slog 项 fail-fast 教学报错），
    // 多项经 MultiLogger 合并、契约 filter_keys 外包 FilterLogger。
    ...
    return log.NewFilterLogger(out, l.GetFilterKeys()...),
        appkit.Cleanup(func(context.Context) error { merged(); return nil }), nil // cleanup 聚合
}
```

声明序在 std.Standard() 之后 → 段路径末者胜，std 默认被整体接管。没有 init() 自注册——接入必须显式出现在你的代码里。

### 2.3 组合：多后端组装用 audit.Compose

Provider 的 Build 里要组装多个后端时，用 `audit.Compose`（`pkg/audit/compose.go:59`）：按声明序构造（顺序即广播序）；任一单元失败（重复名或构造出错）逆序回滚已构造单元的 cleanup，返回 `*ComposeError` 定位失败单元；多后端经 NewMultiAuditor 合并且 cleanup 聚合为逆序回放；单后端直接返回（类型断言友好）；空列表返回 NopAuditor。失败回滚与引擎账本同构（§5）。

## 3. 段语义：末者胜、缺段跳过、无消费者报错

- **同段末者胜**：引擎按精确段路径解析声明，后声明的 Provider 接管同一路径（std 默认后端由此可被覆盖）；不同路径并存（server.http 与 server.asynq 可由不同 Provider 同时供给）。
- **缺段 = skip**：Build 仅在至少一条声明路径实际出现于契约时被调用——段缺失是合法语义（不产半成品）；段在而 Build 失败 = fail-fast，启动中止。config 段例外：无条件构建（其源可独立于契约 config 段存在，如本地文件）。
- **构建时机按顶层段归类**：config 段构造期；server 段迟后阶段（业务 BeforeStart 之后——服务器构造消费业务建好的运行期资源）；logger 段走专用通道（不在 Provider 循环里——两阶段重建 + 热更协议，见 §6）；其余段在阶段 B 框架钩子内按 canonical 序：logger → registry → tracer → metrics → database → cache → storage → audit → ai → workflow → broker。
- **声明无消费者 = L3 fail-fast**：契约里实际出现的顶层段若没有任何 Provider 消费，启动期直接报错（检查实现在 `pkg/appkit/segment_coverage.go:50`）：

```text
appkit: bootstrap config declares sections with no consumer:
  top-level sections with no consumer: script
  hint: 该段无消费者，配置不会生效；引入对应 Provider、经 appkit.WithUnimplementedSegments 显式豁免，或从契约移除该段
```

  server 段双层判定：非内置子段（http/grpc 之外）无 Provider 接管同样报错；用户改过 server 子字段却零能力声明也报错（与 bconf 预填默认深相等则豁免——框架自填非用户意图）。段名拼错由同一次检查覆盖：拼错 ⇒ 该段无人覆盖。这条检查的由来是 script 段教训——全仓零消费者，用户配置静默失效。
- **白名单出口**：`appkit.WithUnimplementedSegments("script", "server.asynq")`——契约先行的工位显式承认「该段暂不生效」，多次调用追加；条目为顶层段名或 server 子段路径。它是唯一豁免出口。

## 4. 产物落资源面

引擎把 Build 产物按段路径落进统一资源面（`bootstrap.go:681`）：map[string]any 产物按 `<顶层段>.<子段>` 逐键展开（nil 值跳过），非 map 产物整体落 `<顶层段>`；仅阶段 B 单线程写入，Run 后只读。取用走 `App.Resource(segment)`（`pkg/appkit/resource.go:37`）：

- **子段精确路径直返实例**：`app.Resource("cache.redis")` → 该实例本身（类型断言在消费侧恢复）；
- 顶层段（"cache"）→ map[string]any 副本快照（键=子段名，可安全遍历/留存）；
- registry/audit 等整产物段 → 产物本身；
- 未装配或段缺失 → `(nil, false)`；装配构造期恒 `(nil, false)`。

域 Wire、业务钩子都从这一个入口取（[新增业务域](./新增业务域%20-%20Domain%20与%20gen%20domain.md) §6：基础设施经资源面迟到取用）。tracer/metrics 产物刻意不落资源面（shutdown/暴露端由 Provider 自持，cleanup 槽位承担停机回收）；六资源域段（database/cache/storage/ai/workflow/broker）不支持热更新。

## 5. cleanup 与停机：Effect 账本

- Cleanup 签名统一为 `func(context.Context) error`（旧无参 func() 经 noopCleanup 适配——Provider 实现侧的便利件）。
- 每个 Provider 的 cleanup 在构造期按 canonical 停机序**预登记 Effect**（槽位模式，Build 成功后填充）：audit-restore 注册最早 → 逆序回放时最后执行（stream 尾批审计事件在指标/span flush 之后收集）；表外第三方 Provider 的 cleanup 追加在 canonical 序之后——回放更早，业务资源先收、框架底座后收。
- **Run 的回滚账本语义**：构建一个、入库一个（逐项即时落账——阶段 B 后段 Provider 依赖前段产物）；任一钩子/构建失败，Run 失败路径 UndoEffects 逆序回放已登记效应（幂等、清空账本），构建失败不在此重复回滚；优雅停机 stopAll 走同一账本逆序回放。

## 6. std.Standard() 的三个内置与顶替

`std.Standard()` 打包框架自有后端的默认 Provider（`pkg/appkit/std/std.go`，实现在 `pkg/appkit/standard.go`）：

| 内置 | 段路径 | 说明 |
| --- | --- | --- |
| StandardServerProvider | server.http / server.grpc | 能力声明在代码（WithHTTP/WithGatewayRegister/WithGRPC），契约段驱动地址/TLS/driver；迟后阶段构建 |
| StandardAuditProvider | audit | audit.backends 的 "log" 项：结构化日志审计后端，核心包零依赖开箱即用 |
| StandardLoggerProvider | logger | bslog（标准库 slog）——阶段 A 引导 logger 的持有者；两阶段重建 + 热更协议仍在引擎 |

其余各段（registry/database/cache/storage/ai/workflow/broker/tracer/metrics/config）无默认接线：段存在而无 Provider = L3 启动期报错（§3）。

**顶替方式即末者胜**：声明同段路径的 Provider 写在 std.Standard() 之后即整体接管——audit 段被用户 audit Provider 接管后，store/stream 等第三方后端按自己的语义装配；logger 段特殊：不在 Provider 循环里、由引擎按两阶段 + 热更协议调用，但覆盖仍是同一条末者胜规则（§2.2 的 loggerProvider 即范本——阶段 A 引导日志也带你的装饰）。server.http/server.grpc 按能力声明装配，行为细节见各能力使用页。

## 报错速查

| 报错 | 根因与修复 |
| --- | --- |
| `appkit: provider main.loggerProvider declares no segments (Segments must not be empty)` | Segments() 返回空——至少声明一条段路径 |
| `appkit: provider … declares an empty segment path` | 段路径列表里混入空串 |
| `appkit: provider [logger server.http] declares segments across build phases … — split it into one Provider per phase` | 同一 Provider 声明了跨构建时机的段路径——按阶段拆成多个 Provider |
| `appkit: 2 providers declare logger segments ([logger logger]); only one logger provider may be declared` | logger 段至多一个 Provider（专用通道） |
| `appkit: bootstrap config declares sections with no consumer: …` | 顶层段无消费者——引 Provider / 白名单 / 移除段（§3） |
| `server.asynq is configured but has no implementation in this repo (remove the section, provide a Provider for "server.asynq", or whitelist it via appkit.WithUnimplementedSegments)` | server 非内置子段无接管 |
| `appkit: provider [cache.redis] returned *T, want map[string]any` | 六资源域段产物必须是 map[string]any（键=子段名） |

## 边界与注意事项

- 没有 init() 自注册——未 import 的后端零编译成本、依赖图可见；第三方后端一律不进 std（std 只打包框架自有后端，对账基准「不多不少」）。
- 声明解析期 fail-fast 的三类错误见报错速查前三行——都在启动早期，不产半成品实例。
- 段覆盖检查在契约装载定型后的 BeforeStart 装配链末尾执行；热更路径不重跑该检查。

## FAQ

- **Provider 和 Option 什么关系？** Provider 嵌入 Option，Apply 把自身追加进声明——Provider 值就是 Option，可直接作为 MustServe 变参项。
- **同一段两个 Provider 谁生效？** 精确路径末者胜，后声明者接管；被顶替者不参与构建。
- **怎么临时豁免框架还没实现的段？** WithUnimplementedSegments 白名单（§3）。
- **表外（第三方）段产物怎么取？** 同一约定：map 产物按 `<顶层段>.<子段>` 展开、非 map 产物落 `<顶层段>`；`app.Resource` 按路径取（§4）。

