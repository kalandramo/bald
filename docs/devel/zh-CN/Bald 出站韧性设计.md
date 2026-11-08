# Bald 出站韧性设计：调用策略的显式组合层

> Author(s): bald 团队
>
> Discussion at: 源码 `transport/client/resiliency/{resiliency.go,matching.go,http.go}`；绑定钩子 `transport/client/contract/provider.go`（`WithEntryGRPCDialOptions` / `WithEntryHTTPOptions`）；对照 `retry/retry.go`、`circuitbreaker/circuitbreaker.go`（契约）、`pkg/middleware/grpc/circuitbreaker.go`（入站失败口径）、`selector/circuitbreaker`（节点级硬排除的既有桥接先例）
>
> Status: Accepted（桥接件已落地；retry / circuitbreaker 契约零改动，client.proto v1 边界不动）

## 摘要

`transport/client/resiliency` 把 `retry` 与 `circuitbreaker` 接成**出站调用的韧性层**：一个 `Policy` 产出 gRPC 一元客户端拦截器（`GRPCDialOption`）与 HTTP `RoundTripper`（`Transport`）两种形态，按命名出站条目显式组合。能力自 onexmesh 的 Dapr 风格 resiliency 吸收，语义改写为 bald 习语。

三条承诺：

1. **不做声明式 YAML 子系统**。调用策略（重试/熔断）由业务域在调用侧显式组合，不进 `client.proto` 契约段——这是 v1 刻意的边界（判据 [J8](./设计判据.md)：契约不超承诺）。
2. **retry / circuitbreaker 契约零改动**。本包只 import 二者 + grpc，产出原生 `grpc.DialOption` / `http.RoundTripper`，**不 import `transport/client`**——组合由调用方经 `client.WithDialOptions` / `client.WithTransport` 或契约绑定钩子完成。
3. **组合顺序固定为「熔断在外、重试在内」**。一次逻辑调用放行一次（`Allow`），重试在内部消化瞬时失败，最终结果恰好上报一次（`Mark*`）。

## 背景与动机

### 为什么是显式组合，不是 resiliency YAML 契约段

onexmesh 的 resiliency 是 Dapr 风格的声明式子系统：一份 YAML 描述 policy，按 target 匹配出站调用。把它照搬进 bald 会撞上两堵墙：

- **`client.proto` v1 刻意只覆盖拓扑**。`Client.Entry` 只有 `name` / `addr` / `tls` / `timeout`，注释明写「重试、熔断、限流属于调用策略，由业务域在调用侧组合，不塞进本段」。加 policy 字段是契约超承诺（[J8](./设计判据.md)），且把「调用怎么重试」与「连到哪个地址」两件正交的事耦进同一段。
- **bald 的三库已是可组合件**。`retry.Retrier.Do` 是传输无关的 `Doer` 端口，`circuitbreaker` 是 `Allow`/`Mark*` 契约。缺的只是把它们接到出站传输面上的**桥**，不是一个新子系统。与 `selector/circuitbreaker`（节点级硬排除桥）同款思路：能力吸收靠薄桥接件，不靠移植整个子系统。

所以最优形态是 mech 式的显式注册：调用方持有 `Policy`，经绑定钩子挂到命名条目上。声明文件描述「连到哪」，代码描述「怎么韧性调用」，各守其界。

### 三库此前没有出站形态

`retry` 有 `Doer` 端口但零出站消费者；`circuitbreaker` 的入站中间件（`pkg/middleware/grpc`）已就位，出站面空缺。本包补齐出站面，让「出站默认超时（[J3](./设计判据.md)）+ 重试 + 熔断」成为一条可显式装配的链。

## 设计

### Policy：闸门 + 重试循环 + 失败口径

```go
type Policy struct {
    Breaker     circuitbreaker.CircuitBreaker        // nil = 不熔断
    Retrier     retry.Doer                           // nil = 单次尝试
    GRPCFailure func(error) bool                     // nil = DefaultGRPCFailure
    HTTPFailure func(*http.Response, error) bool     // nil = DefaultHTTPFailure
    HTTPRetry   func(*http.Response, error) bool     // nil = DefaultHTTPRetry
}
```

零值 `Policy` 是直通（不熔断、不重试），可安全用作「只想要默认失败口径」的起点。熔断器实例由调用方持有——**每个命名条目一个**，自适应状态不跨条目共享（与 `selector/circuitbreaker.Registry` 同一纪律）。`Retrier` 取 `retry.Doer` 端口而非具体类型，调用方可注入 `retry.New(...)` 或测试替身。

### 为什么熔断在外、重试在内

组合骨架 `guard` 固定为：熔断 `Allow` 前置放行一次 → 重试循环在内部跑 → 最终结果（含 panic）恰好上报一次 `Mark*`。

```go
func (p Policy) guard(ctx, failed, call) (err error) {
    if p.Breaker != nil {
        if allowErr := p.Breaker.Allow(); allowErr != nil {
            return allowErr // 闸门没开：不欠 Mark*，也不进重试
        }
        defer func() {
            if r := recover(); r != nil { p.Breaker.MarkFailure(); panic(r) }
            if failed(err) { p.Breaker.MarkFailure() } else { p.Breaker.MarkSuccess() }
        }()
    }
    if p.Retrier != nil { return p.Retrier.Do(ctx, call) }
    return call(ctx)
}
```

两个关键性质：

1. **一次「重试后成功」的逻辑请求记为成功**。下游只是瞬时抖动，重试已经消化——只有最终失败才喂失败率。若反过来（重试在外、熔断在内），每次 attempt 都 `Allow`/`Mark*`，瞬时抖动会被记成多次失败，熔断器替一次逻辑调用过度惩罚下游。
2. **`ErrCircuitOpen` 永远不会被重试**。它在重试循环之前返回，调用方无需把 `ErrCircuitOpen` 排除出重试分类器——两层不耦合。熔断打开时本就该换实例或等睡窗，重试同一实例无意义。

### panic 路径必须上报失败

`guard` 的 defer 里 `recover` 到 panic 时先 `MarkFailure` 再原样抛出。漏掉这一次上报不是少统计一条——hystrix 的 `halfOpenIn` 只由 `Allow` 置位、只由 `Mark*` 复位，半开探针悬着会让熔断从此永久拒绝（与入站中间件、《[Bald 熔断设计](./Bald%20熔断设计.md)》「边界与不变量」同一不变量）。

### 失败口径：只算下游的错

gRPC 面 `DefaultGRPCFailure` 与入站中间件同一集合：`Internal` / `Unknown` / `Unavailable` / `DeadlineExceeded` / `DataLoss`。客户端侧错误码（`InvalidArgument` / `NotFound` / `PermissionDenied` 等）不计——一个坏客户端不该替全体用户开窗；`ResourceExhausted` 也不计——那是下游限流层自己的答案，把它计入失败等于让熔断消费下游的拒流，配额打满时两层互相放大。

`DefaultGRPCFailure` 沿 error 树走（`errors.Join` 的 `Unwrap() []error` 与单链 `Unwrap() error` 都处理），只在 `status.FromError` 成功的叶子上按码判定——`retry` 耗尽返回的是 `errors.Join(ErrMaxAttempts, lastErr)`，真实 status 藏在树里，不 walks 会漏判。

HTTP 面两个口径分开：

- `DefaultHTTPFailure`（喂熔断）：5xx 或传输错误。4xx 不计——坏请求不该替全体开窗。
- `DefaultHTTPRetry`（喂重试）：429 / 500 / 502 / 503 / 504 或传输错误。429 值得重试（退避到下个窗口）但不算下游故障，故只进重试口径、不进熔断口径。

### HTTP RoundTripper：缓冲被重试的响应体

`Transport(p, base)` 包裹 base（nil → `http.DefaultTransport`）。两个传输面特有的坑：

- **请求体重放**：`req.Body` 在首次 attempt 被读空。每次 attempt 经 `req.GetBody()` 重开（POST/PUT 可重试的前提）；`GetBody` 为 nil 时不重试带体请求。
- **响应体缓冲与哨兵不外泄**：判定为可重试的响应，其 body 被 `io.ReadAll` 缓冲后关闭，返回内部哨兵 `errRetryableStatus` 驱动重试。耗尽或单次尝试时，用缓冲重建 `finalResp.Body` 原样交还调用方——**哨兵永远不泄到调用方**（重建条件覆盖「无重试」与「重试耗尽」两条路径）。

### 流面刻意不接

gRPC 只接一元拦截器。一条流的「失败」没有一致答案（半条流算成功还是失败？），与入站熔断拦截器同一取舍——不强行给流面一个语义。

### 为什么不含限流

出站限流与熔断/节点选择职责重叠：熔断已经在下游不可用时拒流，selector 已经在实例间分摊。再加一层出站 ratelimit 是重复税（[J2](./设计判据.md)），且「配额」语义在出站侧没有清晰的归属（限的是本进程对下游的发起速率，还是下游给我的配额？后者是下游 `ResourceExhausted` 的事）。故本包只组合 retry + circuitbreaker，限流留给入站面与下游契约。

## 绑定到命名条目

桥接件产出原生选项，经契约钩子按名字挂到出站条目：

```go
clientcontract.AppProvider(
    clientcontract.WithEntryGRPCDialOptions("user",
        resiliency.GRPCDialOption(resiliency.Policy{Breaker: cb, Retrier: r})),
    clientcontract.WithEntryHTTPOptions("billing",
        client.WithTransport(resiliency.Transport(policy, http.DefaultTransport))),
)
```

- **追加在全局选项之后**：`WithGRPCDialOptions` / `WithHTTPOptions` 的全局选项在前，条目绑定在后，可覆盖全局默认。契约的默认超时拦截器仍在链最外层（每个 attempt 拿完整超时窗）。
- **名字必须真的出现在契约里**：拼错（或条目已删/改名）在装配期 fail-fast——静默失效会让韧性策略悄悄不生效，调用侧看不到任何征兆。
- **两个面各自成表**：同名条目可以合法地同时出现在 `client.grpc` 与 `client.http` 面上，绑定各落其面，互不误伤。

## 边界与不变量

- **Allow/Mark* 配对**：`Allow` 返回 nil 即欠恰好一次 `Mark*`（含 panic 路径）；`Allow` 返回 `ErrCircuitOpen` 即不欠 `Mark*`、不进重试。违反配对会让 hystrix 半开探针悬死。
- **熔断器不跨条目共享**：每个命名条目一个实例，自适应状态隔离。共享会让一个下游的故障替另一个下游开窗。
- **reference-only 落地**：本包是薄桥接件，消费者是契约绑定钩子 + `_example` 出站 e2e。三库本身（retry/circuitbreaker）契约零改动，删改前先对照《[设计判据](./设计判据.md)》J1（零调用不是删除依据）。

## 实现与验证

`transport/client/resiliency/` 独立 module（require circuitbreaker + circuitbreaker/hystrix + retry + grpc，本地 replace 跟踪同仓兄弟）。测试覆盖：

- 确定性 fake 熔断器 + fake invoker：成功记一次成功、服务侧失败记失败、客户端错误码与 `ResourceExhausted` 记成功、开态跳过 invoker、重试后成功记一次成功、重试耗尽记一次失败、不可重试立即返回、panic 记失败并重抛、零值 Policy 直通、`DefaultGRPCFailure` 走 `errors.Join` 树。
- HTTP `httptest`：成功记成功、单次 503 原样交还响应（哨兵不泄）、重试耗尽交还末次响应、重试后成功、429 可重试但不算失败、404 不重试不算失败、传输错误耗尽记失败、开态跳过请求、POST 请求体重放、panic 记失败并重抛。
- 真 hystrix + 真 TCP gRPC 集成：喂 `Unavailable` 跳闸 → `ErrCircuitOpen` 快速失败 → 翻转为成功 + 过睡窗 → 半开探针 → Closed。
- 契约钩子（`transport/client/contract`）：条目绑定只落命名条目、追加在全局之后、拼错 fail-fast、同名两面各落其面。

### 验证方式

```bash
cd transport/client/resiliency && go vet ./... && go test -race -count=1 ./...
cd transport/client && go test -race ./contract/
```

门禁清单由 `scripts/list-modules.sh` 自动派生，新 module 无需手改 Taskfile；`task race` / `task lint` / `task vuln` 与 docscheck 的 module-list 闸门自动覆盖。