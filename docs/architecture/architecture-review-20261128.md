# bald 增量架构评审（2026-11-28）

> **基线**：`main` @ `058f4756`。评审视角 = 增量：[architecture-review-20261118.md](architecture-review-20261118.md)（AR-1…31，已全部回填）、[architecture-debt-remediation.md](architecture-debt-remediation.md)（ADR-1…18）、`.dev/smell.md`（C/W/S 层，2026-11-26 回填）、risk-review §7.x 登记册已收录条目**一律不重报**；登记过而未执行的，本轮执行或核销并在此标注。
> **参照系**：消费者 `../bald-admin`（12 域生产范式）与 `_example/bald`（用户照抄模板）。
> **判据**：设计判据 J1–J9；死代码按 J1 只登记不删除。
> **证据等级**：【已复现】= 实跑命令/测试；【读码确证】= 完整推理链；本轮无【疑似】级条目。

## 总览

本轮新登记 **2 条结构性缺陷 + 2 条示范/文档失真**，核销登记存量 **2 条**，执行登记存量 **1 条**：

| # | 缺陷 | 位置 | 严重度 | 证据 | 本轮处置 |
|---|---|---|---|---|---|
| N-1 | http/grpc 双面装配缝双轨：kernel 持有 transport 工厂，分层方向倒挂 | `transport/{http,grpc}/contract`、`pkg/appkit/standard.go` | 中高 | 读码确证 | **已执行**（S3，见本节末「执行记录」；规格步骤 3 有偏离：不留 Deprecated 转发） |
| N-2 | `_example` 示范教「复制框架实现」，绕开既有装饰接缝 | `_example/bald/internal/bootstrap/providers.go` | 中 | 已复现 | **已执行**：收口到 `StandardLoggerProvider(deco...)` 一行 |
| N-3 | `Bind` 文档示例引用不存在类型 | `pkg/appkit/appkit.go`（Bind 注释） | 低 | 已复现 | **已执行**：示例改业务段形态 |
| N-4 | cobramcp 双引擎同形遍历各持一份复制（`.dev/smell.md` Suggestion 存量） | `cobramcp/config.go`、`cobramcp/server.go` | 中 | 已复现 | **已执行**：收敛 `walkToolCmds` + `cmdFilterBase` |
| N-5 | cli 非原子写（smell Suggestion 存量「14 处 os.WriteFile」） | `cli/internal/**` | — | 已复现 | **核销**：现值 0 处，fsatomic 已全覆盖写面 |
| N-6 | bconf 双折叠算法（smell Suggestion 存量） | `bconf/foldkeys.go` vs `bconf/unmarshal.go` | — | 读码确证 | **降级观察**：分工有文档、原语共用 `lookupKeyFold`、`bconf/fold_invariant_test.go` 已有一致性守卫 |
| N-7 | server 声明面 ≠ 生成接线面：白名单 10 面、模板接线 6 面，其余 4 面「Gate2 庆祝、启动报丧」 | `cli/internal/codegen/appspec_contract.go`、`appspec_gen.go`、appspec.proto `ServerSpec` | 中 | 读码确证（证据链见专节） | **登记冻结**：完全生成化需 bconf proto 加字段，被 cli 零 replace 编排挡死，留给 bconf 变更窗口 |

## N-1【中高】http/grpc 双面装配缝双轨，bootstrap kernel 反向 import transport —— **已执行**（S3，含规格偏离记录）

- **现象**【读码确证】：14 个能力面（cron/mcp/asynq/kafka/rabbitmq/sse/websocket/gateway/client、cache/{local,redis}、registry/{etcd,nacos}、metrics/{prometheus,otel}、broker/{kafka,rabbitmq}、contrib）全部走「`X/contract` 包 = 契约段映射 + appkit 接线」单一模式；唯独 **server.http 与 server.grpc 没有 contract 包**——其工厂 `HttpServerProvider`/`GrpcServerProvider`、选项类型 `GRPCServerOption`、Provider 签名 `ServerProvider` 住在 `bootstrap` 根包（`bootstrap/server.go`），health 后端推送循环也在 `bootstrap/health.go`。
- **倒挂边**【读码确证】：`bootstrap → transport、transport/grpc、transport/http、grpc-go、health`。kernel 模块直接依赖两个具体传输实现；`pkg/appkit/standard.go` 的 stdHTTPProvider/stdGRPCProvider 再消费 bootstrap，形成「引擎 → kernel → 具体 transport」链——所有 contract 包经 appkit 传递性背上 grpc/http 编译成本。`bootstrap/` 全仓 import 面：`pkg/appkit`、`_example`、`contrib/config-nacos`。
- **根因链**【读码确证】：`appkit.Provider` 是引擎词汇 → 契约包统一 import `pkg/appkit`（`transport/cron/contract/provider.go` 即范本）；而 std 段的 Provider **实现体就住在 appkit 包内**（`pkg/appkit/std/std.go` 头部「实现落点说明」自证）。于是 http/grpc 工厂若按 cron 模式放进 `transport/{http,grpc}/contract`，需要 appkit import 自家下游 → 模块环。当年为避环落进 bootstrap（不 import pkg，方向合法）——`contrib/config-nacos/provider.go` 头注释记录的同款约束（「把 Provider 放回任一者会形成模块环」）证明这是系统性约束而非一次失手。
- **持续代价**【读码确证】：① 同一角色的装配知识两套形状（contract 的 `Type` 常量 + `Provider()/AppProvider()` vs bootstrap 的 `WithXxx` 选项工厂），CLI `providerHints` 派生规则「server.\<face\> → transport/\<face\>/contract」对 http/grpc 不成立，靠例外表特判（`pkg/appkit/segment_coverage.go` 注释「由 std 装配，同样不入表」）；② §7.40 的「本地 pkg + 已发布 bootstrap」漂移人口由这条链放大；③ ADR-10 的 contrib/一级模块判据成文了「放哪」，没覆盖「kernel 不该认识具体 transport」这条方向判据。
- **为什么本轮不动**：修复要先把 std 段实现体拆出 appkit（打破其「包内测试不经 import 环」的现状约束）才能给 `transport/{http,grpc}/contract` 让出接线位；改动跨 `pkg`、`bootstrap`、`transport/http`、`transport/grpc`、`cli`（例外表特判回收）≥5 个 module 与 owned 生成树，且每一步受 §7.40 replace 人口约束。属立项动作，按下述规格冻结，随大版本或独立分支批次执行。

### N-1 收敛规格（冻结，按序执行）

1. 新建 `transport/http/contract` 与 `transport/grpc/contract`：**只迁**契约段→server 的映射与选项类型（不 import appkit，范本 = `transport/cron/contract/contract.go` 的「不 import bootstrap、返回自有函数类型」段）；gRPC 健康推送循环随 `GrpcServerProvider` 迁入 `transport/grpc/contract`。
2. `pkg/appkit/standard.go` 改 import 两个 contract 包。module 图新增 `pkg → transport/{http,grpc}` 边——**先审计 §7.40 人口**：33 个 replace pkg 的 module 里凡缺 `transport/http`、`transport/grpc` 相对 replace 的，补 require + replace（沿用仓内相对 replace 形态），否则远端重演「undefined」类红。
3. `bootstrap/server.go` 原符号保留为 **Deprecated 薄转发**（一个 minor），大版本删转发，`bootstrap → transport` 边随之消失，kernel 收敛回「契约装载 + 配置源工厂」单职责。
4. `pkg/appkit/segment_coverage.go` 例外表删 http/grpc 特判，统一走派生规则；guide 的 Provider 接入范本与 `bald convention` catalog 卡同步为单轨。
5. 门禁：逐 module build + test -race + `task acceptance` + `bald gen verify` + docscheck；`_example/bald` owned 树若含 bootstrap 转发引用则 `bald gen app --adopt` 重派生。

不裹挟的部分：std logger/audit Provider 的拆包（它们不 import transport，不承载倒挂边）、`pkg/` 改名（ADR-16 永久挂账）——各自独立批次。

### N-1 执行记录（2026-11-28，dx-flatten S3）

- **步骤 1**【落地】：新建 `transport/http/contract`（`Option`/`WithHandler`/`Provider`，形状照搬 `transport/gateway/contract`）与 `transport/grpc/contract`（`Option`/`WithUnaryFunc`/`WithRegister`/`WithHealth`/`Provider` + reflection 契约消费；`bootstrap/health.go` 的推送循环逐字搬入 `health.go` 更名 `NewHealthServer`）。两包均不 import appkit/bootstrap。
- **步骤 2**【落地简化】：`pkg/appkit/standard.go` 两个 std Provider 改引 contract 包。§7.40 人口审计整步作废——S2 扁平化后各族与 appkit 同 module，无 per-module require 可补。
- **步骤 3**【偏离规格：不留 Deprecated 薄转发】。实测 `bootstrap.HttpServerProvider`/`GrpcServerProvider`/`WithGRPC*`/`WithHTTPHandler`/`NewGRPCHealthServer`/`ServerProvider` 在 cli 模板、`_example/bald`、bald-admin 三处消费面**零引用**（外部全部走 `appkit.WithHTTP/WithGRPC/WithGRPCOptions/WithGRPCOptionsContract`，唯一仓内消费者是 appkit 自身）。留转发 = `bootstrap→transport/{http,grpc}` 边整轮存活，forbid 判据无法翻绿、变异测试无法证明墙存在；v0.x minor 允许破坏性变更，旧引用方冻结在已发布 tag 线上不受影响。`bootstrap/server.go`、`bootstrap/health.go` 及对应测试直接删除。
- **步骤 4**【落地】：`segment_coverage.go` 的 `providerHint` http/grpc 特判改为派生自 `builtinServerSegments` 唯一真相源（名单不在判据函数里重复）；guide《Provider 接入》§2.1 表与 §6 内置说明、`bald convention` 卡（bootstrap-r09 改写、observability-r06 落点更名）同步单轨口径。规则总数不变（205 条，改文本不新增 ID）。
- **闸门**【落地】：`.arch.json` 翻绿——S2 存量 25 条违规归零（forbid 2 条随文件删除；allow 23 条被新六层矩阵按真实角色判合法：families→kernel=contrib/config-nacos 注册模式、engine/kit→families=SPI 消费）；`--verify` 144/144 归层 100% 零 FAIL 零 WARN、`--blind-spots` 无 source 规则 0 个；新增原生门禁 `internal/archgate`（读同一份 `.arch.json`，镜像 check 语义 + 防空转 + **变异注入**：bootstrap→transport/http、transport/grpc 合成边判红并点名 forbid 规则，bootstrap→pkg/appkit 判 allow 矩阵越界）——步骤 3 的「现值 0」由此变成「判据真实存在」。
- **acceptance 表**新增 2 条 ci 腿（transport/http、transport/grpc 的 contract 行为判据），scope=ci 实测 30 腿 / 229 判据全过。
- **文档核销**：《Bald Bootstrap 设计》协议服务器/health 两节重写、《框架契约总览》能力地图「bootstrap 两类工厂」、《Bald 健康检查设计》归属表、current-architecture 关键接缝、flows.md/flows.dot 证据行、《应用框架设计》职责表、《Bald 传输层设计》头注。`module-deps.dot`/`bald-c4.dsl` 的 bootstrap 节点文字与 svg 重渲染留 S4（既有挂账）。
- **发版注意（挂 S4）**：本批属破坏性 v0 minor——`bald release` 前须确认 cli 生成模板零发射旧符号（已实测），并在 CHANGELOG 写明「v0.57.0 起 server 工厂在 `transport/{http,grpc}/contract`，旧 `bootstrap.*` 服务器工厂符号删除不留转发」。

## N-2【中】`_example` logger 示范漂移：教复制而非用接缝 —— **已执行**

- **现象**【已复现】：`_example/bald/internal/bootstrap/providers.go` 原 73 行手抄 `stdLoggerProvider` 全套四件（Segments/Apply/Plan/Build），只为给日志加脱敏装饰。而 `pkg/appkit/standard.go` 的 `StandardLoggerProvider(deco ...bslog.Option)` 注记明说「装饰只需替换默认 Provider：std.Standard() 后追传本构造器即可」；消费者 bald-admin（`internal/bootstrap/assembly.go:272`）正是这么写的。**接缝存在，示范却教复制**——`_example` 是用户照抄的模板，等价于把 J2 重复税批发给每个新应用。
- **已漂移证据**：复制版报错文案 `"logger: ... builtin path supports only \"slog\""` 与框架现行 `"appkit: ... default path supports only \"slog\";\n  remote/custom backends need a logger provider:\n    appkit.MustServe(...)"`（带教学指引）已经分叉；复制版也没有 std 的「单项直通不包 MultiLogger」注释语义锚点。框架侧协议再演进（如阶段 A 行为、EffectName 改名），复制版静默腐烂，docscheck/gen verify 都拦不住非 owned 手写文件。
- **修正**：providers.go 收敛为一行 `appkit.StandardLoggerProvider(bslog.WithFilter(...), bslog.WithAttrs(...))`；providers_test.go 从断言私有类型改为直接回归接缝产物（脱敏/service.name 判据不变）。
- **验证**【已复现】：`_example/bald` 无 tag `go build ./...` + `go test ./...` 全绿；`-tags nacos` 变体红项为本机缺 nacos/etcd 服务的环境依赖（HEAD 原样同红，已用 stash 复核），非本轮回归。

## N-3【低】`Bind` 文档示例引用不存在的类型 —— **已执行**

- **现象**【已复现】：`pkg/appkit/appkit.go` 的 `Bind` 注释示例 `appkit.Bind("http", &bootstrap.Http)` / `&bootstrap.Grpc`——`bootstrap` 模块无 `Http`/`Grpc` 类型（`rg 'type (Http|Grpc)' bootstrap/` 零命中），照抄即 `undefined`（正是 §7.40 红过的报错形状）；且 `server.http`/`server.grpc` 引擎装配时已代为 Bind（`pkg/appkit/bootstrap_assemble.go:102`、`:105`），业务再登记属重复注册源。
- **修正**：示例改为业务段形态（`Bind("biz.notify", &notify.Conf)` + 键路径/flag/env/yaml 四者一致说明与 PlainBinder 空前缀形态），并写明「契约 server 段已由引擎绑定，业务无需重复登记」。与 bald-admin 实际用法（`Bind("", svrOpts)`，PlainBinder）对齐。

## N-4【中】cobramcp 双引擎遍历复制 —— 登记存量，**本轮执行**

- **来源**：`.dev/smell.md` Suggestion 层「cobramcp 双引擎（config.go vs server.go 两套 registerToolsRecursive/cmdFilter）」，11-16 登记、未列入后续批次处理，本轮核查仍在（两份同形遍历）。
- **修正**：新增 `cobramcp/toolwalk.go`——`walkToolCmds`（叶子先序递归、过滤点、selector 按登记序求值、首配即停的**单一遍历骨架**）+ `cmdFilterBase`（hidden/deprecated/不可运行三判据）。两引擎差异全部收进 visit 回调：Config 面首配即停（返回 `stop=true`）、建工具失败短路遍历；MCPServer 面 `RegisterHandler` 失败**保留原语义**继续试后续 selector（`stop=false`），含「失败时 toolMetas/toolPaths 已写入」的既成 quirk 也不变——注册面行为零改动，只去重形状知识。
- **验证**【已复现】：`cobramcp` module `go vet` + 全量 `go test`（含 TestCmdFilter、TestNewMCPServer_HiddenDeprecatedFiltered、determinism、REST/serve 覆盖组）+ `golangci-lint run` 0 issues。

## N-7【中】server 声明面宽于生成接线面 4 面 —— S1b 侦查发现，登记冻结

- **来源**：dx-flatten S1b（变体接线生成化）侦查 `cli/internal/codegen` 面派生逻辑时确证（2026-11-28），非重报：ADR-6 登记的是 http3/graphql/tcp/redis_server/webrtc 五面的下线残留，本条是**残留之后仍开放的 4 面**。
- **证据链**【读码确证】：
  1. **契约承诺面**：`bconf/proto/bootstrap/v1/server.proto` `message Server` 含 `sse=5`（:471）、`websocket=6`（:472）、`kafka=21`（:574）、rabbitmq 子段——四面是合法契约段。
  2. **Gate2 白名单**：`cli/internal/codegen/appspec_contract.go:201` `implementedServerFaces` 共 10 面（含上述 4 面），声明即放行。
  3. **生成器接线面**：`cli/internal/codegen/appspec_gen.go:177-184` 只派生 6 个面 flag（HasHTTP/HasGRPC/HasGateway/HasCron/HasAsynq/HasMcp，`AnyFace` 同口径），`appspec_tmpl_app.go` 的接线分支也只覆盖这 6 面——sse/websocket/kafka/rabbitmq **零生成接线**。
  4. **运行期后果**：声明这 4 面且无人手工接线时，引擎计划期 L3 报 `appkit: bootstrap config declares sections with no consumer:`（`pkg/appkit/segment_coverage.go:101`）——正是白名单注释（`appspec_contract.go:190`）自称要止血的 ADR-6 同族「gen 庆祝、启动报丧」，只是止血口径选在了「transport/ 有实现目录」而非「生成器有接线」。
  5. **多角色放大**：`ServerSpec`（`bconf/proto/bald/appspec/v1/appspec.proto:221-246`）只有 http/grpc/gateway/cron/asynq/mcp 开关，无这 4 面字段；`projectServer` 是整段覆盖投影（:233 注释），未声明的开关段在角色投影里**被裁掉**——多角色树下连手工接线的声明面都表达不了。
- **守卫缺口**：`appspec_contract_transport_test.go:26` 只钉「白名单 ⊆ `transport/` 目录」一条腿；「白名单 ⊆ 模板接线面」「白名单 ⊆ `ServerSpec` 可投影面」两条不变量无任何 gate——纯靠注释维持，与 J2（重复税抽单一闸门）相悖。
- **处置（冻结理由）**：完全生成化需三件套同改——① `ServerSpec` 加开关字段（bconf proto 变更）、② 模板接线分支、③ 白名单口径改写 + 上述两条不变量的同源测试。①被 AGENTS.md 的 cli 零 replace 纪律编排挡死（cli 只能依赖已发布 bconf tag），S1 不是该变更窗口。本轮不动代码；bconf 窗口到来时按 ①②③ 一并落地，测试与实现同批。

## 登记核销与状态回填

| 条目 | 登记处 | 本轮实测 | 处置 |
|---|---|---|---|
| cli 非原子写（14 处 os.WriteFile） | `.dev/smell.md` Suggestion | `rg 'os\.WriteFile' cli -g '!*_test.go' -g '!cli/internal/fsatomic/*'` = **0 命中**；写面已全走 `cli/internal/fsatomic`（prd/compose/ide/codegen） | 核销，登记不再挂账 |
| bconf 双折叠（foldkeys vs unmarshal 读侧兜底） | `.dev/smell.md` Suggestion「口径漂移已现端倪」 | 两实现分工成文（落树前折叠 vs 读侧兜底），共用 `lookupKeyFold` 原语，`bconf/fold_invariant_test.go` 存在一致性守卫 | 降级为观察项；再出现单侧修序时要求共享 fixture 升级 |
| transport/subscribe 0 测试 | `.dev/smell.md` 测试缺口 | `transport/subscribe/*_test.go` 不存在，仍属实 | 维持立项挂账 |
| ADR-8 assemble 余量 / ADR-6 远期 / `pkg/` 改名 | debt-remediation | 前轮已收口/已冻结 | 不重开 |

## 检查过但排除（本轮）

- `transport` 根包 import broker：实为 `transport/internal/brokertype` 适配器内部包引 broker/metrics，接口中枢自身干净——不成立。
- 依赖图 DFS 无环、越层边复核（本轮边集与 `module-deps.dot` 一致）：新增观察只有 `bootstrap→transport` 一条，已归入 N-1。
- 包级可变状态、`sync.` 原语使用、docs 层 `bootstrap.Http` 扩散（rg docs/ 零命中）：未发现新缺陷。
- admin 侧 `appkit.Bind("", svrOpts)` PlainBinder 用法与 `WithGRPCOptions/WithGRPCOptionsContract`：均走既有接缝，无框架缺口。

## 本轮改动清单

| 文件 | 改动 | 验证 |
|---|---|---|
| `_example/bald/internal/bootstrap/providers.go` | 手抄 loggerProvider → `StandardLoggerProvider(deco...)` 一行 | build + 全量 test 绿 |
| `_example/bald/internal/bootstrap/providers_test.go` | 断言从私有类型改为接缝产物 | 脱敏/service.name 判据原样通过 |
| `pkg/appkit/appkit.go` | Bind 注释失真示例修正（仅注释） | — |
| `cobramcp/toolwalk.go`（新增）、`cobramcp/config.go`、`cobramcp/server.go` | 双引擎遍历/基础过滤收敛单源 | vet + test + lint 全绿 |
| `docs/architecture/README.md` | 导航表补本评审行 | docscheck |
