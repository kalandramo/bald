# bald 传输统一抽象重构评估

> 评估日期：2026-11-08。动因：owner 的痛点——「bald 的 transport 不统一」，对照对象是 onexmesh 的
> 统一抽象（`Transporter` + `middleware.Handler` 一份签名服务双协议，见 [onexmesh-comparison.md](onexmesh-comparison.md)）。
> 本文先把「不统一」拆到层（有两层其实已经统一了），再给三档重构方案的代价与风险。
>
> **状态：方案 S 已全部落地**（批次 1–4 见 §3 实施进展，`pkg/v0.7.0`→`v0.10.0`、`bconf/v0.24.0` 已发）。
> 本文按设计决策记录保留：痛点拆解与三档取舍是当时的裁决依据；人日估算与批次路径已执行完毕，
> 仅作历史口径。旧链 `Gin()`/`GRPCInterceptors()` 保留至下一 major（§3 批次 4 的登记口径）。

---

## 1. 痛点拆解：哪些已经统一，哪些真的没有

### 1.1 业务 handler 契约——**已经统一**（很多人会低估这一点）

证据：`_example/bald/internal/apiserver/handler/gin/greet.bind.gen.go`——

```go
// GreetService 是从 bald.v1.GreetService 的 rpc 签名生成的 Go 接口，业务实现后传入 BindGreet。
type GreetService interface {
    Greet(ctx context.Context, req *baldv1.GreetRequest) (*baldv1.GreetResponse, error)
}
```

一个接口、一份实现，同时被 REST 绑定（`BindGreet`，按 `google.api.http` 注解生成）与 gRPC 注册消费。
**这与 onexmesh `NewMethod[Req,Resp]` 的目标完全一致**，达成路径不同：onexmesh 用运行时泛型擦除
（`Handler func(ctx, any) (any, error)`，强类型在桥上保住），bald 用 IDL 代码生成（强类型天生在接口上，
代价是改 proto 要重跑 gen domain）。业务签名层面 bald 不欠账。

### 1.2 错误面——**已经统一**

berrors 传输中立 + `grpcerr`/`httperr` 边界投影 + HTTP 响应体 = google.rpc.Status 三面一份
（errors-r08/r09）。onexmesh 用 errorsx 自带 `GRPCStatus()` + `RenderError` 达到同一语义。

### 1.3 真正不统一的三层（痛点本体）

| # | 痛点 | 现状 | 量化 |
|---|---|---|---|
| A | **中间件双链**：每个横切件写两遍 | `pkg/middleware` 共 9,364 行；`bundle.Gin() []gin.HandlerFunc` 与 `bundle.GRPCInterceptors() []grpc.UnaryServerInterceptor` 两套类型（bundle.go:334/428）；J2 只收敛了 tracing 一件（shared） | 双链是最大头 |
| B | **手写路由双轨**：非 CRUD/自定义路由走手写 `Domain.Routes`（gin 形态），与 gen domain 生成绑定并存；gRPC 侧自定义方法又是另一条注册路径 | `handler/grpc` 目录在手写场景才出现 | 两套写法、两处挂接 |
| C | **无路由级中间件**：`server.{http,grpc}.middleware` 段是 server 级，无法按接口差异化（「这个接口单独限流」做不到） | onexmesh 的 `middleware/matcher` 按 operation（gRPC 方法名 / HTTP `METHOD 路由模板`）挂接 | 能力缺位 |
| D | （边角）biz 里访问原始 header/metadata 的形态 | contextx 扁平键已覆盖 90% 场景；raw header 只剩文件上传等边角 | 不构成主痛 |

## 2. 三档方案与代价

### 方案 S：统一中间件契约 + 三桥（推荐，痛点 A/C 一次闭）

**形状**：引入 bald 自己的 `middleware.Handler func(ctx, req any) (any, error)` 与
`Middleware func(Handler) Handler`（与 `grpc.UnaryHandler` 签名对齐——onexmesh 已验证该签名选型），
配三座桥：`UnaryServerInterceptor` / `GinHandler` / `StreamServerInterceptor`。
内置件逐个改写为统一形态，bundle 门面保兼容。**可从 onexmesh 直接搬的部分**（MIT + 自有 repo）：
Handler/Middleware 契约 + 五桥核心（~400 行）+ `matcher` 路由级挂接（~300 行）。

| 工作包 | 内容 | 人日 |
|---|---|---|
| 契约与三桥 | 从 onexmesh 移植 + 适配 berrors/log 取值 | 1–2 |
| 内置件改写 | recovery/requestid/tracing/超时/限流/认证/授权/审计/CORS/bodylimit ≈ 10 件 × 0.5–1 | 6–9 |
| matcher 路由级 | 移植 + 契约段演进：`server.middleware` 统一段（按 matcher 声明）替代/并存 `server.{http,grpc}.middleware`——bootstrap/v1 **minor 演进**（bconf-r02 口径） | 2–3 |
| bundle 兼容期 | `Gin()`/`GRPCInterceptors()` 改为内部走统一链的薄壳，旧 API 保留一个版本期（deprecated） | 1–2 |
| 装配接线 | bootstrap 适配器（契约段→统一链翻译）+ transport/grpc、web 挂接点切换 | 2–3 |
| 测试与文档 | middleware 测试迁移、_example e2e 回归、设计文档一篇、transport/logging 卡更新 | 2–3 |
| **合计** | | **14–22 人日（2–3 周）** |

**收益**：横切件从此写一遍（J2 重复税的最终清偿）；路由级中间件能力进契约；`server.{http,grpc}.middleware`
双段归一。**J6 无冲突**：桥在传输实现之上，各后端仍只承诺协议形状。
**风险**：streaming 桥是难点（onexmesh stream.go 有现成设计可搬）；契约段演进要走 proto-contract 门。

### 方案 M：S + 手写路由统一（`NewMethod` 式泛型注册，痛点 B 闭）

在 S 之上移植 onexmesh 的 `server.NewMethod[Req,Resp]` + `RouteGroup`（泛型擦除 + gin 风格组注册），
把手写 `Domain.Routes`/http_only 域统一到同一 Handler 面；生成绑定面**不动**（两轨并一轨仅限手写面）。

- 增量代价：**3–5 人日**（契约/桥已就位，只剩泛型注册器与 RouteGroup 移植 + 文档）。
- 收益：自定义路由一个写法；与生成路由可在同一 RouteGroup 树里共存。
- 风险：低——纯新增面，不碰生成物。

### 方案 L：Transporter 全运行时统一（**不推荐现在做**）

`Transporter` 接口进 ctx、业务按需取 header/metadata、后端改造成桥（onexmesh transport.go 283 行 + 各后端适配）。

- 增量代价：**5–8 人日**。
- 收益存疑：bald 的业务通过 proto message + contextx 已拿到 90% 所需（context-r01~r04），raw header
只剩文件上传等边角（1.4-D）；换取的是「换 web 框架」的理论自由——而 web 流水线强绑 gin 是既定取舍。
- 判据冲突检查：J6 可保（Transporter 是往 ctx 注入，不改后端形状），但 L 会把 onexmesh 的核心
运行时抽象整体引入，维护两套传输心智。

### 不做清单

- streaming 的 HTTP 面（onexmesh 同样不支持，IDL 注解表达不了流式）；
- 换 gin / 抹掉 web 流水线（transport 卡 r05 是既定取舍）；
- 把 gen domain 的生成绑定改成运行时擦除（强类型接口是 bald 对 onexmesh 的优势，不是债）。

## 3. 迁移路径（S 可拆四批，每批独立可发）

```
批次 1：契约 + 三桥 + matcher 进仓（纯新增，旧链不动）          → cli 无关，transport/pkg minor
批次 2：内置件逐个改写（每件一个 PR，bundle 双链并行供血）      → 每件可独立回滚
批次 3：契约段 server.middleware 统一段 + bootstrap 适配器切换  → bconf minor + proto-contract 门
批次 4：旧 Gin()/GRPCInterceptors() 标记 deprecated → 下一个 major 移除
```

回归防线：`_example/bald` e2e（真实 HTTP+gRPC 双面）+ acceptance 42 条腿——迁移期间任何一批红都当场可见。

### 实施进展（2026-11-08）

- 批次 1（契约 + 三桥 + matcher）：已进仓（`pkg/v0.7.0`）。
- 批次 2（内置件改写）：十件统一内置件齐备——Recovery/RequestID/Timeout/Authn/Authz/Audit/RateLimit/Validator/Defaulter（`pkg/v0.8.0`/`v0.9.0`）+ Observability（semconv 收拢 `098f5ee` 后解锁， gin/grpc 经三桥委托 `middleware.Observability`）。`pkg/middleware/shared` 的实现（InjectTrace/MatchWildcard/trace 头常量）退场并入 root `traceinject.go`，shared 降为零消费者 deprecated 别名壳（J2 final 清偿；full 删除随批次 4）。附带修复统一内置件 gRPC unary 面 `next(ctx, nil)` 丢 req 的正确性 bug（改 `next(ctx, req)`，回归测试钉住），及 stream 桥用链传播 ctx 重包流（中间件 enrich 的 span/trace_id 到达业务流 handler）。
- 批次 3（契约统一段 + bootstrap 适配器）：`server.unified_middleware` 段 + Bundle 适配已进仓（`bconf/v0.24.0`/`pkg/v0.10.0`）；`web.ErrorResponse` 委托 middleware 渲染原语（`8bdd45c`）。
- 批次 4（旧链移除）：`Gin()`/`GRPCInterceptors()` 已标 deprecated（`8bdd45c`）；旧 authn/authz/audit/recovery/timeout 等链与 shared 壳保留至下一 major 移除——bridge 错误闭包（grpcerr.ToStatus）与 nil info 约束使部分旧链无法薄委托而不破坏钉住行为，故不强改。
- 文档联动复核：transport/logging/catalog 卡（r05/r06/r02/r12）不含双链实现的陈旧断言，无需改；living doc `current-architecture.md` 横切层条目已更新为统一链口径。

## 4. 判据与文档联动（实施时同步）

- J2 重复税：统一链落地即 final 清偿——pkg/middleware/shared 退场并入统一链；
- transport 卡 r05/r06、logging 卡、catalog 卡 r02 的表述更新；
- 中间件相关判据若受影响（bundle 门面语义）进 risk-review 登记册走一轮。

## 附：证据

- 双链证据：`pkg/middleware/bundle/bundle.go:334/428/537/546`；规模 9,364 行（wc 全包）。
- handler 已统一证据：`_example/bald/internal/apiserver/handler/gin/greet.bind.gen.go`（生成注释明言
  「手写 REST handler 归零」「一个域可声明多个 service」）；`handler/grpc` 空目录佐证 gRPC 面走生成注册。
- onexmesh 侧形状与可搬性：`pkg/middleware/middleware.go`（Handler 签名与 grpc.UnaryHandler 全同）、
  `pkg/middleware/stream.go`、`pkg/middleware/matcher/`、`pkg/server/service.go`（NewMethod）——
  均为 MIT 且属同一 owner（`kalandramo/onexmesh`）。
- 局限：代价按本仓现状估（16 卡纪律、42 条 acceptance 腿），若 middleware 内置件在迁移期间继续增删，
  工作包 2 的人日随实际件数浮动。
