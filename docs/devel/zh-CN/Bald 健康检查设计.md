# Bald 健康检查设计

> Title: health——三态健康检查：并发聚合 + K8s 双探针端点，零依赖纯标准库
>
> Author(s): bald 团队
>
> Last updated: 2026-09-15
>
> Status: Accepted（对应实现 `health` module；tag `health/v0.1.0` 已发，探针装配与就绪归属见《Bald 健康检查装配设计》）

## 摘要

health 是 bald 的健康检查 module：一个接口、三态状态机、两层出口。核心是
`Checker` 接口（`Check(ctx) Result`）与 `Health` 聚合器——并发执行全部已
注册检查器、单次全局超时、按「Down 传染 > Unknown 传染 > 全 Up」聚合；
出口层把聚合结果暴露为两个 HTTP 端点：readiness（依赖检查，Down 返回 503）
与 liveness（恒 200）。整个 module 只有一个承诺：**零第三方依赖——纯标准
库，实现 `http.Handler` 而非 gin handler，任何服务形态都能挂**。

## 背景与动机

### K8s 探针需要一个诚实的出口

服务跑在 Kubernetes 里，编排器会周期性打两个端点：`livenessProbe`（进程
该不该重启）与 `readinessProbe`（该不该摘流量）。两个问题语义不同，但简
陋实现经常把它们混成一个 `/healthz`——依赖挂了 liveness 也失败，K8s 重启
进程，而重启解决不了 Redis 断连，只制造重启风暴。

### 移植背景

本 module 是独立 module，`github.com/kalandramo/bald/health`：tag `health/v0.1.0` 已发；
2026-09-17 起由 `bootstrap`/`appkit` 依赖做探针与就绪装配（协议层已退出健康检查域），
见《Bald 健康检查装配设计》。

## 设计

### 总览：一个接口、三态状态机、两层出口

```
Checker（接口）──Register──→ Health（聚合器）──Check──→ Result
                                 │
                                 ├─ Handler（readiness）→ 200 / 503 + JSON 明细
                                 └─ livenessHandler    → 恒 200
```

五个概念各司其职：`Status` 三态枚举、`Checker` 检查接口、`Result` 单次结
果、`Health` 并发聚合、两个 handler 分别对接 K8s 双探针。

### Status：三态，零值 Unknown

```go
const (
    StatusUnknown Status = iota // 尚未检查或检查结果不确定
    StatusUp                    // 正常运行
    StatusDown                  // 不可用
)
```

三态是本设计的地基：`Down` 是「依赖确证不可用」，`Unknown` 是「检查没跑
成或结果不确定」——前者该摘流量，后者不该。零值取 `Unknown` 而非 `Up`：
零值健康会让「忘了跑检查」伪装成「检查通过」。

### Checker 与 PingFunc：函数即检查器

```go
type Checker interface {
    Check(ctx context.Context) Result
}

type PingFunc func(ctx context.Context) error // nil→Up，err→Down
```

绝大多数依赖（Redis/DB/对象存储）已有 `Ping(ctx) error` 形态的方法，
`PingFunc` 让它们零适配直接注册。注意 `PingFunc` 无法产出 `Unknown`——
error 只有二态；`Unknown` 留给「检查器自身故障」（如 nil checker）与自定
义 `Checker` 表达。

### Health：并发聚合 + 单次全局超时

```go
h := health.New(health.WithTimeout(2 * time.Second)) // 默认 5s
h.Register("redis", health.PingFunc(redisClient.Ping))
h.Register("tcp-db", health.TCP("db.internal:5432", 2*time.Second))
r := h.Check(ctx) // Result{Status, Details: {"redis": {...}, "tcp-db": {...}}}
```

聚合规则三条，按传染性排序：

1. 任一 `Down` → 整体 `Down`；
2. 否则任一 `Unknown` → 整体 `Unknown`；
3. 全 `Up` → 整体 `Up`。零个检查器 → `Up`（裸进程 readiness 直接通过）。

执行模型：每次 `Check` 先对注册表做快照（读锁内复制，持锁不执行检查器），
然后每个检查器一个 goroutine 并发执行，全部受同一次 `WithTimeout(ctx,
timeout)` 约束。每个检查器再被一层 `select { done, checkCtx.Done() }` 兜
底——**检查器不遵守 ctx 也不会拖死聚合**，超时者记为
`checker %q timed out` 的 `Down`。

注册表是 `sync.RWMutex` 保护的 map，`Register`（同名替换）/`Deregister`
（缺省空操作）运行期可用。

### 内置检查器与组合子

| 检查器 | 语义 | 适用 |
|---|---|---|
| `TCP(addr, timeout)` | TCP 拨号成功即 Up | DB/缓存/队列等 TCP 依赖 |
| `HTTP(url, timeout)` | GET 响应 2xx/3xx 即 Up | 下游服务、外部 API |
| `AllCheckers(cs...)` | 全过才 Up，遇 Down 短路 | 一个名字下聚合多项子检查 |
| `AnyCheckers(cs...)` | 任一过即 Up | 高可用多副本（一活即可） |

`All` 的价值在**命名粒度**：`Health` 聚合本身就是全 AND，但那是「检查器
之间」的关系；`All` 让「storage」这一个名字内部再组合「DB + Redis」两项，
失败消息定位到 `checker[i]`。`Any` 是聚合器表达不了的 OR——多副本依赖
（redis-1/redis-2）挂一个不算故障。

### Handler：readiness 与 liveness 是两个问题

```go
srv.GET("/healthz", health.NewLivenessHandler().ServeHTTP) // 进程活着即 200
srv.GET("/readyz",  health.NewHandler(h).ServeHTTP)        // 依赖检查，Down → 503
```

readiness 的响应映射只有一条：`Down → 503`，`Up/Unknown → 200`。响应体
JSON 带全量明细：

```json
{ "status": "down",
  "checks": { "redis": { "status": "down", "message": "connection refused" } } }
```

liveness 是独立类型、恒 200、不持有 `Health`——它回答的唯一问题是「HTTP
服务器还在响应吗」，与任何依赖无关。

## 理由与取舍

**决策①：三态而非二态。** 多数健康检查库只有 up/down 二态，`error` 恰好
也是二态。我们保留 `Unknown`，因为「检查器自己没跑成」与「依赖确证挂了」
需要不同出口——前者摘流量过于激进（可能是检查器 bug、配置漏了、启动竞
态）。被放弃：二态 + `error` 返回值——省一个枚举，但表达力不够，且零值
语义失控。

**决策②：Down → 503，Unknown/空表 → 200——宁可乐观。** readiness 探针
失败即摘流量，误报的代价（无流量服务被摘、级联影响）高于漏报（故障实例
多活一个探针周期）。零检查器 = `Up` 同理：不强制业务注册依赖才配拥有
readiness。被放弃：Unknown/空表也 503——严格但脆弱，启动窗口期服务会被
误摘。

**决策③：liveness 恒 200，绝不检查依赖。** liveness 失败的后果是重启进
程，而重启解决不了外部依赖故障，只会制造重启风暴与雪崩。依赖故障的正确
出口是 readiness 摘流量、等依赖恢复。被放弃：liveness 复用 `Health.Check`
——实现省一个类型，语义灾难。

**决策④：双层超时兜底。** `Health.timeout` 约束整体，但检查器实现不受
我们控制——`c.Check(checkCtx)` 若无视 ctx，外层 `select` 到期即记超时
`Down`，聚合照常返回。诚实代价：无视 ctx 的检查器 goroutine 会继续跑到
自然结束（`done` channel 带 1 缓冲防阻塞）——有界泄漏，等价于该检查器自
身 bug，不该由聚合器陪葬。被放弃：只靠 ctx 传递、不做 select 兜底——
一个坏检查器拖死整个探针端点，K8s 超时判死。

**决策⑤：零第三方依赖，实现 `http.Handler`。** 只 import 标准库；出口
是 `http.Handler` 而非 `gin.HandlerFunc`——gin、标准库 `http.Server`、
grpc-gateway 都能直接挂，module 不反向依赖任何 web 框架。被放弃：
gRPC 健康检查协议（`grpc.health.v1`）——只覆盖 gRPC 面，且把零依赖
module 绑上 grpc；gin 专属 handler——省一个适配，绑定框架。

**决策⑥：显式 Register，不用 init() 自注册。** 对齐 bald 全家决策（见
《Bald Bootstrap 设计》）：注册了什么，代码里看得见。

**决策⑦：每次探针实时执行，不缓存、不后台轮询。** K8s `periodSeconds`
本身控制探测频率，缓存只会引入陈旧窗口（依赖恢复/故障晚一个周期被发
现）。被放弃：后台定时刷新 + 读缓存——省探针时的执行开销，换来一致性
复杂度；探针频率本来就不高，实时开销可忽略。

## 兼容性

独立 module、无消费者，纯增量，无破坏性变更。测试 24 例全绿（状态/三态
适配/注册表/聚合/超时/组合子/双 handler/TCP/HTTP/并发安全）。

已知代价与遗留（诚实列出）：

- `httpClient()` 导出函数名是「未来可注入自定义客户端」的预留，但当前
  无注入 API——HTTPChecker 只能用默认 5s 客户端（自身 timeout 字段已可
  覆盖单请求超时）。
- `Check` 聚合时把检查器 `Details` 展平合并进同一 map——检查器 Details
  若含 `status`/`message` 键会覆盖结构化字段（同名冲突）。当前无内置检
  查器产出这两个键，属约定内边界。
- 组合子构造函数名 `AllCheckers/AnyCheckers` 与类型名 `All/Any` 不对称
  （避开 `All` 既是类型又是构造的歧义），略拗口但可用。

## 实现与过渡

**已落地**，4 个源文件 + 1 个测试文件（24 例）：

| 文件 | 内容 |
|---|---|
| `health.go` | `Status`/`Result`/`Checker`/`PingFunc`/`Health`（聚合器全套） |
| `checkers.go` | `TCPChecker`/`HTTPChecker`/`All`/`Any` 组合子 |
| `handler.go` | readiness `Handler` + liveness `Handler` |
| `http_helper.go` | 默认 HTTP 客户端与请求构造（预留注入位） |

依赖图定位：与底层叶子模块（berrors/bconf/log/bconfig）同级——甚至更
纯，连间接第三方 require 都没有（berrors module 因 grpcerr 带 grpc）。
接入方式（当前设计为纯库，用户两行挂路由）：

```go
h := health.New()
h.Register("db", health.TCP(cfg.DB.Addr, 2*time.Second))
srv.GET("/readyz", health.NewHandler(h).ServeHTTP)
```

**尚未做**：发 tag、根模块 require、`bootstrapv1` 契约段与 Registry 装配
（若未来要配置化接入，按六域归位判别规则——「契约段 → 实例」构造期归
bootstrap——新增 health Registry 即可，本包无需改动）。

## 附录：FAQ

**为什么 readiness 不返回 503 时也带明细？** 排障需要：`checks` 里每个
检查器的状态与失败消息一眼可见，不用进容器看日志。探针端点对外暴露依赖
拓扑是可接受的（K8s NetworkPolicy 通常已限制探针来源）。

**`PingFunc` 什么时候不够用？** 需要产出 `Unknown`、或需要附带 Details
（如连接池水位）时，实现自定义 `Checker` 返回完整 `Result`。

**HTTP 检查器为什么 3xx 也算健康？** 探测目标通常是内部服务域名，重定向
（如 trailing slash）在健康语境下不是故障信号；4xx/5xx 才是——与负载均
衡器的健康判定惯例一致。
