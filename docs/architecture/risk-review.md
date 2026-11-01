# bald 架构风险 / 质量评审

> 产出方式：`risk-quality-reviewer`，交叉引用 `system-modeler`（`current-architecture.md`）与 `architecture-health`（本文对制品自身的校验）。
> 证据日期：2026-11-01，`main` @ `ca981b1`，工作树干净。
> 核对方式：文中每条结论要么由我本人读源码/执行 grep 得到，要么由脚本从制品中抽出 `路径:行号` 后逐条打开源文件比对。凡未做到这一点的，明确标 ⚠ 并写清依据。
> **本评审不改动任何代码**，只登记风险与消解条件。

## 严重度概览

| # | 风险 | 严重度 | 置信度 | 状态 |
|---|---|---|---|---|
| 6.5 | gRPC 的 `tls` 契约字段**未接线**，生成物硬编码明文；gateway 出站也硬编码明文 | **高** | ✅ 已核对源码 | **已修** |
| 3.1 | 根 module ↔ 适配器 **模块环**，靠互相 `replace` 才编译 | **高** | ✅ 已核对 go.mod/import | **已修**（删重复机制后实测 89 模块为 DAG；22 个适配器单向 require 根的扇入耦合仍在） |
| 6.1 | 鉴权 **fail-open**：未注入即整层消失，无 default-deny、无告警 | **高** | ✅ 已核对源码 | **已修** |
| 2 | **发布面 ≫ 使用面**：retry / circuitbreaker / Discovery 等无消费方 | 中 | ✅ 全仓 grep | 未修 |
| 7.1 | **CI 全量只跑 `-short`**，集成测试在 CI 中从不执行 | 中 | ✅ 已核对 `.github/workflows/ci.yml` | 未修 |
| 1.1 | race 作业是两处手工清单，webrtc/kafka 均缺失 | 中 | ✅ 已核对 `.github/workflows/ci.yml` / `Taskfile.yml` | 未修 |
| 1.3 | kafka / rabbitmq 适配器 **零测试文件** | 中 | ✅ 实测计数 | 未修 |
| 5 | `berrors` 分类法未在 transport/broker/appkit 落地 | 中 | ✅ 实测计数 | 未修 |
| 6.2 | gRPC 链无限流、无超时（与 gin 链不对称） | 中 | ✅ 已核对源码 | 未修 |
| 6.3 | 限流位于认证之后，认证层自身不受保护 | 中 | ✅ 已核对注释与实现 | 未修 |
| 3.2 | 21 个 module 同时 require 同一库的两个 major（`// indirect`） | 低 | ✅ 脚本实测 | 未修 |
| 4.1 | 停机期 `_ = deregister(...)` 吞错 | 低-中 | ✅ 已核对源码 | **已修** |
| 4.2 | 端点上界 5s、hook 超时 5s 为常量（Stop 超时本身可配） | 低 | ✅ 已核对源码 | **已修** |
| 8 | `docscheck` 只校验链接完整性，测不出描述过期 | 低 | ✅ 已核对测试 | 未修 |

---

## 1. 模块面 vs 构建/验证面

**1.1 race 作业是手写清单，且两处重复** ✅
`.github/workflows/ci.yml:103-107` 手工枚举 23 个模块（`. transport transport/grpc transport/http transport/http3 transport/tcp transport/websocket transport/sse transport/web retry ratelimit ratelimit/tokenbucket ratelimit/bbr ratelimit/sentinel bootstrap registry cache cache/redis broker broker/redis log metrics health`），`Taskfile.yml:368-416` 镜像同一份清单。
实测 `grep -n webrtc .github/workflows/ci.yml` → **无匹配**：`transport/webrtc` 有 15 个非测试文件 + 8 个测试文件，含 `signalSessions` 共享会话表与 PeerConnection 生命周期（正是 race 该覆盖的形态），却不在 race 清单里。`broker/kafka`、`contrib/*` 同样缺席。
- 影响：并发回归只能在本地偶发发现。
- 本地全仓 `go test -race` 实测（bconf / 根 module / contrib/authz-casbin / 示例 module）：未见数据竞争，唯一失败是 `bconf` 分配剖面守卫的阈值与 race 分配器错配（非 race 45,680 B/op → race 51,337，见 `bconf/alloc_guard_test.go` 头）。也就是说这个缺口当前漏掉的正是这类只在 -race 下才响的报警。
- 消解：webrtc/kafka 加入 race；或把 race 也改成 `find` 循环 + 一份「跳过清单」，让默认值是「跑」而不是「不跑」。

**1.2 CI 的模块发现是健壮的，本地 `task verify` 不是** ✅
CI 用 `find . -name go.mod ... | xargs -n1 dirname | sort`（`.github/workflows/ci.yml:61`，lint/vuln 作业各复用同一循环 `:132,:165`）零维护地发现全部模块（**注意循环排除了 `./_example*`**），并逐模块 `go mod download` + `go mod tidy -diff` 闸门 + build + vet + test + bench 编译。
但 `Taskfile.yml:328-331` 的 `test` 只跑根 module，`verify`（`:466-470`）手写依赖清单只覆盖 bconf/cobramcp/retry/ratelimit/broker/circuitbreaker/encoding/example —— `contrib/*`、`registry/*`、`transport/http|grpc|web`、`oss/*`、`ai/*`、log/metrics 后端**不在任何本地任务里**。只跑 `task verify` 会静默跳过约 40 个模块。
- 严重度低（CI 兜底），但作者等待时间被拉长到 CI 才发现问题。
- 消解：本地 `test/lint/vuln` 复用同一条 `find` 循环。

**1.3 零测试的网络适配器** ✅ 实测

| 模块 | 非测试 .go | 测试 .go |
|---|---|---|
| `broker/kafka` | 9 | **0** |
| `broker/rabbitmq` | 11 | **0** |
| `transport/subscribe` | 1 | **0** |
| `retry` | 1 | 1 |

kafka 适配器核心文件 639 行，含 writer 重建重试（`broker/kafka/kafka.go:419-428`）与订阅退避逻辑（`broker/kafka/subscriber.go:312-331`）与致命错误判定（`:334-355`）——这些分支从未被执行过。CI 对它只做到「能编译、vet 干净、空测试通过」。
- 消解：优先给 `Publish` 的 writer 失败重建路径与 `Subscribe` 的退避/致命错误规则补测试（可用 kafka 的 in-memory 接口或拆出纯函数）。

---

## 2. 发布面 ≫ 使用面（已核实的未接线能力）✅

| 能力 | 模块数 | 消费方 | 证据 |
|---|---|---|---|
| `retry/` | 1 | **无** | 全仓 `grep kalandramo/bald/retry` 唯一命中是注释 `ratelimit/bbr/bbr.go:68` |
| `circuitbreaker/` | 1 + 4 后端 | 仅自己的后端 | `circuitbreaker/{hystrix,vegas,sres,sentinel}/*.go`；无 transport/middleware/appkit 引入 |
| `registry.Discovery` | 接口 + 4 实现 | **无外部调用者** | `GetService` 调用点仅出现在 registry 后端自身（`registry/kubernetes/registry.go:206` 自调用、`registry/nacos/watcher.go:68` 是 nacos SDK 同名方法）与测试 |
| `transport/graphql`、`workflow/argo`、`ai/{openai,eino,langchaingo}`、`oss/{s3,minio}` | 7 | 仓内 0 | 全仓 grep 仅 `transport/mcp/options.go:14` 的注释提到 graphql；其余无命中。有 `*/contract` Provider 但无消费者或示例 |

**为什么这是风险而不是「可扩展性好」**：这些模块进发布 tag、进 vuln/lint CI、进 `CHANGELOG.md`，但只有构造函数级测试（或没有测试）在触碰它们，**会静默腐烂**。Discovery 尤其值得单列——它让「服务发现」在 README 与契约里存在，而实际服务间调用走配置地址直连（`transport/gateway/gateway_server.go:93` 用 `s.backend.GetAddr()` 拨号，地址来自契约 `Grpc.addr`，见 `transport/gateway/contract/provider.go:167`），使用者容易误判。

消解路径（按成本排序，任选其一，不要默认「留着以后用」）：
1. 接进 `pkg/appkit/std` 的标准角色（retry/circuitbreaker 作为可选 bundle 层，Discovery 作为 gRPC resolver）；
2. 显式标注为 reference-only，移出主线发布批次，在 README 的 SPI 表里标「未接线」；
3. 归档删除。

---

## 3. 依赖治理

**3.1 根 module ↔ 适配器的模块环** ✅ —— 本次评审的最高风险之一。（环已断，见本节末「修复」；剩下的是适配器单向 require 根的扇入耦合）

修复前的双向拓扑（评审当时的实测，现已变化）：

```
根 module ──require──▶ 适配器
  go.mod:45      bootstrap v0.13.3
  go.mod:51-53   transport v0.4.0 / transport/gateway v0.4.1 / transport/grpc v0.1.1
  bootstrap/server.go:15-17   import transport/{gateway,grpc,http}
  bootstrap/health.go:13      import transport/grpc
  pkg/middleware/gin/authn.go:15  import transport/web

适配器 ──require──▶ 根 module
  transport/gateway/go.mod:28   require github.com/kalandramo/bald v0.34.0
  transport/gateway/contract/provider.go:24  import github.com/kalandramo/bald/pkg/appkit
  registry/{etcd,nacos,consul,kubernetes}/contract/provider.go  import pkg/appkit
  cache/redis、oss/{minio,s3}、contrib/*、broker/redis、transport/{sse,asynq,cron,tcp,webrtc}  同样
```

实测：**22 个非示例模块直接 require 根 module**（另有 `_example/bald`，合计 23），根 module 自身有 19 条本地 `replace`（`go.mod` 中 17 条在 `:5-37`，另有 `:228,:230` 两条 ratelimit 相关），每个 require 根的适配器也都写了 `replace github.com/kalandramo/bald => ../..`。Go 允许 module 图成环（按包级加载），所以修复前它**能编译**——但代价是：

- 回边被本地 `replace` 遮蔽：改 `pkg/appkit` 的导出 API，根的 `go mod tidy` 与根自己的测试都看不出下游会断；断裂只在**给下游模块 bump require 到不存在的 tag 之前**都不显现。
- 发版顺序变成人工约束。近期提交正是这条链的症状：`ca981b1`「tag 分批推送，避免触发流水线漏跑」、`ec4223f`「归位 bconf 的消费者 require」、`89a6a94`「未发版模块也跟随归位内部依赖」。
- `*/contract` 子包是回边的主要载体（30 个目录），**这也是唯一可下刀的地方**。

- 消解（方向，非承诺）：把 v2 Provider 所需的最小面（`Provider`/`Node`/`PlanScope`/`ApplyScope`/`Cleanup`/`Option`）抽到一个叶子 module（例如 `pkg/appkit` 独立成 `github.com/kalandramo/bald/appkit`，或新建中立的契约 module），让适配器只依赖叶子而非聚合根；收益是 `replace` 数量与版本追赶下降、下游对根 API 变更解耦。**注意：gateway 那条断掉后图里已无环，这一步是解耦而非断环。**
- 消解条件：任何适配器不再 `require github.com/kalandramo/bald`（仅剩 `pkg/` 内不参与 Provider 的包可以例外）。

**3.1 修复（gateway 这一条环）✅ 已修**

原评审把这条环写成「22 个模块互相环」是夸大了。按 `go list` 解析每个模块的真实
import（模板字符串里的 import 字面量不算）后实测：环只有一个 3-模块 SCC
`{. , bootstrap, transport/gateway}`，由两条边撑起——

| 边 | 载体 | 现状 |
|---|---|---|
| A `transport/gateway → 根` | `transport/gateway/contract/provider.go` import `pkg/appkit`（Provider 接入面，方向正确） | 保留 |
| B `bootstrap → transport/gateway` | `bootstrap/server.go` 的 `HttpServerProvider` 内建 gateway 分支 + `WithGatewayRegister` + `DriverGrpcGateway` | **已删** |
| C `根 → transport/gateway` | `pkg/appkit` 的 `Decl.GatewayRegister` / `WithGatewayRegister` / `StandardServerProvider` 的网关分支，及其包内测试 import gateway | **已删** |

B、C 是同一套机制的两份实现（命令式能力声明 vs 段驱动 Provider），删掉的是**重复**
而不是能力：转码面改由 `transport/gateway/contract` 的两个段 Provider 表达——
`HTTPProvider(register)` 接管 `server.http`（同端口单面），`AppProvider(register)`
消费 `server.gateway`（独立端口并存）。为让「接管 HTTP」不误伤 gRPC，
`std` 的 server Provider 拆成 `StandardHTTPProvider` + `StandardGRPCProvider`
（段路径末者胜是按 Provider 整体驱逐的，合并成一个节点就会两面一起被顶掉）。

- 配套：`driver` 的准入判据从 bootstrap 移到转码面自己（构造期 fail-fast 不变）；
  `WithHTTP` 与转码 Provider 同时声明且 `driver` 留空仍构造期报错（显式 > 隐式）；
  探针包装随实现迁到 gateway；`--plan` 的 `serverCapability` 不再投影 gateway
  （它现在是真实节点）；codegen 模板与 `_example` 的 `grpcgw` 变体改出新写法。
- 计划节点键随之变化（breaking）：`server` → `server.http` / `server.grpc`。
- 验活：`go list` 图脚本在修复前的同一提交上抓到 SCC（168 条内部边），修复后
  非测试与含测试两遍均为 `NONE (DAG)`（168 / 177 条边）；`_example` 的 `grpcgw`
  变体真实起进程，`GET /v1/greet/bald` 经转码面返回 200，gin 专属路由 `/v1/ping`
  在该面上 404，`/healthz` 200 且挂 Down 检查器后 `/readyz` 503。
- 刻意没做：删掉重复机制就够断环了，没有为其余适配器动刀。`registry/*`、`contrib/*`、
  `oss/*` 等 22 个模块仍然 require 根 module（单向回边，实测图已是 DAG，不再成环），
  它们的版本追赶/编辑期不可见要靠抽 `appkit` 叶子 module 才能消——独立 breaking
  动作，仍待 owner 决策。根与 `bootstrap` 的 `go.mod` 里指向 `transport/gateway` 的
  本地 `replace` 一并保留（已无对应 require，留着是为将来复用不必重新接线）。
- 还不能证明：CI 的 Linux 全量与 race 清单未因本次改动扩展（§1.1 的缺口照旧）。

**3.2 依赖卫生：多 major 并存（低）** ✅ 脚本实测

此前写成「未见问题」是错的。逐模块解析 `go.mod` 的 `require` 块后实测：**21 个 module 同时 require 同一库的两个 major**，全部带 `// indirect`，主要是三组：

| 库 | 并存的路径 | 出现在 |
|---|---|---|
| `go.yaml.in/yaml` | `/v2 v2.4.4` + `/v3 v3.0.5` | 21 个 module（含 `_example/bald`） |
| `github.com/alibabacloud-go/tea-utils` | `v1.4.4` + `/v2 v2.0.7` | 20 个 module |
| `github.com/pelletier/go-toml` | `v1.9.x` + `/v2 v2.2.4` | 根 module、`oss/minio`、`_example/bald` |
| `github.com/nacos-group/nacos-sdk-go` | `v1.1.6` + `/v2 v2.3.5` | 仅 `_example/bald` |
| `github.com/cenkalti/backoff` | `/v4 v4.3.0` + `/v5 v5.0.3` | 仅 `contrib/observability-otlp` |

- 判断：Go 按 module path 区分 major，两个 major 在同一个 module 图里合法，且这里绝大多数来自上游 SDK 的传递依赖。它不构成正确性问题，代价是同一进程可能加载两套实现（二进制体积、行为差异、类型互不兼容）。
- 未测：本结论只覆盖各 `go.mod` 的 `require` 行，未展开 `go mod graph` 的全量图，也未测「同一服务的最终二进制里是否真的同时链接两个 major」。
- 消解：不值得为此加 `exclude`/`replace`（会与上游 SDK 冲突）。可加一条只读报告（如 `task deps-report`）让新增共存可见；真正要跟的是「同一调用链上同时出现两个 major」。

---

## 4. 并发与生命周期

**先说好的部分**：`appkit.Run` 是全仓成熟度最高的代码——CAS 重入保护、`Resolve` fail-fast、errgroup 启动失败级联取消、`:0` 端口轮询带 5s 上界、`stopAll` 五阶段带逐阶段独立超时、`runHook` recover panic（`pkg/appkit/appkit.go:561-703,715-750,753-761`）。停机用未取消的新 ctx（`:692-693` 注释明确解释了为什么不能用被取消的信号 ctx）。

**4.1 停机期吞掉反注册错误** ✅ 已修 原状：`pkg/appkit/appkit.go` 两处 `_ = a.deregister(context.Background())`（主停机路径与 afterStart 失败路径），失败时注册中心的残留条目不可见，后果是流量继续打到已停实例——恰是它想避免的事。
- 消解：`logDeregister(ctx, err)`（`pkg/appkit/appkit.go:868-874`）在两个调用点记录 `appkit deregister failed` + 底层错误，返回值语义不变（停机期没有可返回错误的调用方）。回归由 `pkg/appkit/deregister_log_test.go` 固化：经真实装配路径（`StandardLoggerProvider` + buffer handler）断言两条路径各出一条含底层错误的日志，且 `Run` 仍返回 `nil` / afterStart 原错误。

**4.2 端点上界与 hook 超时不可配** ✅ 已修 原状：`waitForEndpoints` 的 5s 是函数内常量；`defaultHookTimeout = 5 * time.Second` 封顶 before/afterStop 钩子，而 `appkit.StopTimeout(d)`（→ `Decl.StopTimeout` → `stopTimeout`，契约默认 30s）本身是可配的——于是唯一的可调入口管不到这些阶段。
- 影响：慢监听（如需要初始化大量连接的 broker transport）会中止启动而无调整入口；慢钩子同理。
- 消解（已落地，未新增 Option）：删掉 `defaultHookTimeout`，阶段预算统一由 `phaseBudgetFor(stopTimeout) = stopTimeout/2` 派生（`pkg/appkit/types.go:13-22`）——钩子（`appkit.go:715-750`）、单条 undo、组件 Start/Dispose（`decl.go:221-240` 缺省即派生，显式声明优先）、端点等待四处同缩放；端点等待另优先取调用方 ctx 的截止期（`appkit.go:810-822`），框架不缩短调用方给的预算。回归见 `pkg/appkit/phase_budget_test.go`（含「stopTimeout 200ms → 100ms 上界报错」这条对旧实现的反证）。

**4.3 registry 心跳** ✅ 未见泄漏：`registry/etcd/registry.go:245-324` 的 `heartBeat` 有 `maxRetry` 上界（`:258`）、重试循环与各等待点都检查 `ctx.Done`（`:259,277,294`）、`stopCtx` 也参与 select（`:320`），cancel handle 在 mutex 下跟踪并在 `Deregister` 关闭（`:163-181`）。残余风险仅在 clientv3 `KeepAlive` 阻塞超过 cancel，属推测。

---

## 5. 错误分类法未统一 ✅

`berrors` 存在且设计良好，但采用度极低：
- 全仓非测试文件 import `berrors`：**13 个**（`berrors/` 自身除外）。
- `transport/http`：**0**　`broker/kafka`：**0**　`pkg/appkit`：**0**
- 同期 `transport` + `broker` + `pkg` 三个目录下非测试文件里 `fmt.Errorf` **278** 处、`errors.New` **102** 处。
- appkit 直接返回裸包装错误，如 `appkit.go:829` `fmt.Errorf("appkit: servers not ready within %s …")`。

例外（说明能力是真的）：`transport/web` 统一经 `berrors.FromError` + `httperr.CodeToHTTP` 映射并兜底 INTERNAL（`transport/web/response.go:56-93`）；gRPC 有集中式 `grpcmw.ErrorInterceptor`（链首，`pkg/middleware/bundle/bundle.go:225`）。

- 影响：**错误语义只在「泛型绑定面」成立**，在 server 构造面（transport/http、broker）不成立。跨语言/跨协议消费者拿到的是无码错误。
- 消解：二选一并停止摇摆——(a) 在 transport/broker 边界强制 `berrors`；(b) 承认 `berrors` 只服务 `transport/web` + gRPC 拦截器，从 README 的能力叙述里降级。

---

## 6. 安全

**6.1 鉴权 fail-open（高）** ✅ 已修
原状：`pkg/middleware/bundle/bundle.go` 的注释把设计说得很清楚——「未注入的依赖对应层直接省略（零开销），如未设 Authn 则链中无认证层」，实现即条件 `append`。后果：**一个没配 authenticator 的 bundle 就是一个无认证服务器，且没有任何启动告警**。零开销是优点，默认开放是风险，两者被同一个 `if` 绑在一起。
- 消解（已落地，零开销设计不变）：认证姿态必须被决定——`bundle.Authn(...)` 或 `bundle.Public()` 二者之一。`Bundle.Verify()`（`pkg/middleware/bundle/bundle.go:185-212`）在两者都没给时报 `AUTHN_UNDECLARED`，`Gin()`/`GRPCInterceptors()` 出链时对未决定的姿态告警一次；业务想要硬失败就在装配期调 `Verify()`。生成脚手架写出 `bundle.Public()` + TODO，使「本服务无认证层」成为一行显式声明而不是疏漏。
- 为什么不是构造期 panic：链是零开销省略的载体，`New()` 直接失败会让「先起链、再决定」的既有形态无处安放，也会让新 `gen app` 出的最小示例跑不起来（最小路径必须可复制即跑）。
- 回归：`pkg/middleware/bundle/authn_posture_test.go`（Authn/Public/两者都不给/只给 Authz 四种判定；两个出链点对未决定出声、对已决定不出声）。
- 未修：契约侧仍没有 authn 段（`server.*.middleware` 只有 recovery/cors/logging/request_id/tracing/rate_limit/timeout），认证器实例仍只能代码注入；`Server.Http.Middleware` 整体是「业务自取」的参考段，无框架消费者。

**6.2 gRPC 链缺限流与超时（中）** ✅ `pkg/middleware/bundle/bundle.go:224-241` 到 Authz 即结束；gin 链另有 RateLimit + Timeout（`:199-209`）。gRPC 面上的暴力尝试与慢 handler 无框架级保护。

**6.3 限流在认证之后（中，作者已知）** ✅ `pkg/middleware/bundle/bundle.go:199-204` 明确记录了这个取舍（为了认证后的 subject 可见性，代价是不保护认证层自身）。风险真实但**已被文档化**，属于业务需要自行在链首补 `ginmw.RateLimit` 的情形。

**6.4 CORS / Secure 默认关闭（低-中）** ✅ `b.corsCfg != nil` 才挂 CORS、`b.secure` 默认 false（`pkg/middleware/bundle/bundle.go:184-189`；`New()` 只默认 `logging`/`normalized`，`:159`）。

**6.5 gRPC TLS 契约字段未接线（高）** ✅ 已修

原状：契约声明了 TLS，但没有任何代码消费它——

| 环节 | 事实（修复前） |
|---|---|
| 契约字段 | `Server.Grpc` 声明 `TLS tls = 3;`（`bconf/proto/bootstrap/v1/server.proto:138`） |
| 装配层 | `GrpcServerProvider` 只读 `GetReflection()`，从不读 `GetTls()` |
| 服务端构造 | `grpc.NewServer(unary...)` + 裸 `net.Listen`，无 `grpc.Creds` |
| 生成物默认 | 硬编码 `grpc.Creds(insecure.NewCredentials())`，且在 **owned** 的 `assemble.gen.go` 里，注释却写「业务可扩展」 |
| gateway 出站 | 硬编码 `insecure.NewCredentials()` |

- 影响：在契约里写 `server.grpc.tls` **不会生效**，gRPC 仍是明文；与 HTTP 行为不一致，按 HTTP 心智配置的人会误判「我已开 TLS」。

修复后的消费面（同一 `tlsconf` 语义，四处生效）：

| 环节 | 现在的消费者 | 证据 |
|---|---|---|
| TLS 材料→`*tls.Config` | `tlsconf.Server`（cert+key 必需；给 `ca` 即 `ClientCAs`+`RequireAndVerifyClientCert`；MinVersion TLS1.2；忽略 `insecure_skip_verify`） | `bconf/tlsconf/tlsconf.go:26-59` |
| gRPC 服务端 | `Start` 读 `s.cfg.GetTls()` 构造 `tls.Config`，监听器套 `tls.NewListener`，并补 `NextProtos=["h2"]`（grpc-go ≥1.67 强制 ALPN，否则客户端报 missing selected ALPN property） | `transport/grpc/grpc_server.go:88-101` |
| HTTP 服务端 | 同一入口，`net.Listen` 前先构 TLS 配置（失败 fail-fast，端点保持空） | `transport/http/http_server.go:66-79` |
| gateway 出站 | `dialOpts(backend.GetTls())`：无段 → insecure，有段 → `credentials.NewTLS(tlsconf.Client(..))`，错误上抛 | `transport/gateway/gateway_server.go:68-86` |
| 生成物 | `newGRPCServerOptions` 的 insecure stub 与 `insecure` import 一并删除，改为 `appkit.WithGRPC(registerGRPCService)`；TLS 不再是业务可选项 | `internal/codegen/appspec_tmpl_app.go:144,194`、`_example/bald/internal/bootstrap/assemble.gen.go` 已按 `--adopt` 重生成 |

- 三处语义变化，接手者需要知道：① 服务端 `tls.ca` 从「写了没用」变成「写了就强制校验客户端证书」；② `tls` 段存在但材料不全 → 启动期 fail-fast（旧行为是静默明文）；③ gRPC TLS 依赖 h2 ALPN，业务自持 `*tls.Config` 时必须自己带上（框架只在字段为空时补）。
- 未修的范围（刻意保留）：契约 `tls` 字段在 Http3/Graphql/Sse/Websocket/Tcp/Thrift/Webtransport/Signalr/Socketio/Webrtc 仍无消费者（`bconf/gen` 都生成了 `GetTls()`）。SSE contract 明确写了「不加载证书，需业务注入 `*tls.Config`」（`transport/sse/contract/contract.go:71`）；HTTP3 的 `generateTLSConfig` 无条件自签（`transport/http3/server.go:169`）。这些适配器没有 `contract` 之外的框架装配路径，属 §2「发布面 ≫ 使用面」的同一议题。
- 回归：`bconf/tlsconf/tlsconf_test.go`（文件/内联 PEM 等价、材料不全逐条报错、真实 mTLS 握手、服务端拒绝无客户端证书、客户端拒绝不可信服务端）+ `transport/http/http_tls_test.go`、`transport/grpc/grpc_tls_test.go`、`transport/gateway/gateway_tls_test.go`、`bootstrap/grpc_tls_test.go`（装配层端到端）。证书由 `bconf/tlsconf/tlstest` 现发（一次性 CA + 叶子证书，产出即契约 `TLS` 段），因此每条断言走的都是真实契约路径而非 mock。

**6.6 机密** ✅ 未发现机密被写日志的证据；脱敏有文档化缺口：不覆盖 `slog.Attr` 形态（如 `slog.String("password", v)`），见 `bconf/gen/go/bootstrap/v1/log.pb.go:36` 的说明。**govulncheck 是 fail-closed 且覆盖全模块**（`.github/workflows/ci.yml:159` 固定 v1.9.0，vuln 作业用 `find` 循环）——这一项没有问题。

---

## 7. 测试与验证策略

**7.1 CI 全量只跑 `-short`（中）** ✅ `.github/workflows/ci.yml:75,77,108` 的每模块命令都是 `go test -short`。按 CI 自己的注释（`:8`），`-short` 的语义是**跳过集成测试**。因此 kafka/redis/nacos 之类的真实后端交互在 CI 里从不执行；唯一非 `-short` 的是 `.github/workflows/ci.yml:82` 的 `internal/codegen` 两条 Tree 端到端用例。
叠加既有事实：asynq 集成测试需要 `BALD_TEST_REDIS_URI`，Redis 不可达时自动跳过——**注入与否决定这批用例是否真的跑过**。
- 消解：明确哪些接缝必须有集成覆盖（至少 store/registry/broker 各一条），为它们开一个按需注入依赖的 CI job，而不是继续在 `-short` 里假定绿。

**7.2 无覆盖率闸门** ✅ 所有 workflow grep `coverage|codecov` → 无匹配。根 module 有 365 个测试文件（绝对量可观），但没有任何比例约束，且 §1.3 的零测试模块不会因此暴露。

**7.3 已具备的成熟度**（避免只报负面）：`-timeout 5m/10m`、codegen 树的端到端用例拆成独立步骤单独给 15m 预算（`.github/workflows/ci.yml:82`）、依赖下载 retry×3（`.github/workflows/ci.yml:64-71`）、golangci-lint/govulncheck 版本 pin（`.github/workflows/ci.yml:126,159`）、基准冒烟编译每模块跑（`.github/workflows/ci.yml:75,77`）。核心包（`pkg/appkit`、`bconf`、`bconfig`、`transport/http`、`pkg/middleware`）非测试代码里 **TODO/FIXME/HACK = 0 命中**——干净到更像是「债务存在但没标记」，而非没有债务。

---

## 8. 文档漂移

`README.md`（81 行）**确实**描述了 v2 Provider（「段驱动的 Provider 契约」`:5`，appkit 一行含「段驱动 Provider」`:58`）——我原先的怀疑不成立。
`internal/docscheck/docscheck_test.go` 随 `go test ./...` 执行，校验两件事：每条相对 Markdown 链接的目标存在（`:21`）；Go 注释里引用的每个 `docs/*.md` 路径可解析（`:65`）。
- 缺口：**它只测链接完整性**，测不出「文字在描述已删除的 API」或「新增模块没进文档」。§2 那些未接线模块正是这类漂移的候选。
- 消解：加一条模块清单断言（`find` 结果 vs docs 中列举的模块集合），成本极低且能自动失效。

---

## 9. 本次评审自身的局限（architecture-health）

| 局限 | 说明 |
|---|---|
| 无运行时证据 | 未接触任何真实部署、日志、trace。所有「如何运行」均来自代码静态阅读，运行时拓扑标 unknown。 |
| 图的校验深度 | 已装工具并真实校验：`module-deps.dot`、`flows.dot` 用 graphviz 16.1.0 `dot -Tsvg` 渲染（退出码 0、stderr 为空）；`bald-c4.dsl` 用官方 `structurizr-dsl` 6.2.4 解析器 parse + `DOTExporter` 导出 3 张视图，导出物再渲染通过。渲染与解析确实抓到了纯结构检查看不见的缺陷：`module-deps.dot` 里 `{ rank=same; retry; cb; }` 与 cluster 冲突（graphviz 报 "already in a rankset, deleted from cluster"）；`bald-c4.dsl` 用了 6.x 已不存在的 `system` 关键字、`workspace` 位置参数多写版本号、container 的第 4 个位置串会被解析成 **tags**（证据文本落错字段），且描述里的裸 `>` 会让导出的 HTML label 语法报错——均已修正。**仍未做的**：无人工判读「图是否画得对」以外的语义，即渲染通过 ≠ 建模正确。 |
| 引用核对方式 | `路径:行号` 由脚本从三份制品中抽出后逐条打开源文件核对（含 `.dot` 中被 `\n` 拆行的标签）。散文结论（如「零 importer」「13 个 importer」）靠全仓 grep 计数。行号会随代码演进漂移，`README.md` 列了重核锚点。 |
| 依赖结论的边界 | §3.2 只解析各 `go.mod` 的 `require` 行，未展开 `go mod graph`，也未验证最终二进制是否同时链接两个 major。 |
| 外部消费方未纳入 | bald-admin 在另一仓库。§2/§3 的「无消费方」仅指 bald 仓库内；若 bald-admin 使用了某能力，结论需下调。 |
| 建议≠授权 | §3.1 的抽叶子 module 是 breaking 动作（涉及 22 个模块的 require 与发版批次），按既定规则需 owner 决策后才动。 |

## 10. 如果要动手，按这个顺序

1. **接上 gRPC TLS**（§6.5）——静默明文是本轮最该先修的项，改动集中在 `GrpcServerProvider` + 生成模板 + gateway 出站三处。
2. **`bundle` 加未配 Authn 的显式警告/契约开关**（§6.1）——改动小，风险面最高。
3. **`_ = deregister` 补日志**（§4.1）——一行。
4. **webrtc + kafka 进 race 清单**（§1.1），本地 `task verify` 改用 `find` 循环（§1.2）——消除双份人工清单的开始。
5. **kafka 的 writer 重建 + 订阅退避补测试**（§1.3）——为第 4 步的 race 覆盖提供可跑内容。
6. **给 §2 的未接线面做决策**（接线 / 标注 reference-only / 删除）——需要 owner 口径。
7. **`berrors` 边界口径**（§5）与 **Discovery 消费侧**（§2）——设计级，先出方案再动码。
8. **抽离 appkit 契约面，把适配器从「require 聚合根」解耦**（§3.1）——breaking，放在最后，且只在 §5/§6 的口径定清之后做。模块环已由 gateway 那一条的重复机制删除而断开（实测图为 DAG），这条剩下的收益是 `replace` 数量与版本追赶，不再是断环。
