# Bald 健康检查

面向使用者的操作手册：怎么挂双探针端点、怎么注册检查器、聚合规则是什么、
什么时候 503。设计论证（为什么三态、为什么 liveness 恒 200、决策①~⑦）见
`docs/devel/zh-CN/Bald 健康检查设计.md`，本文只讲用法。

## 心智模型：一句话版

**readiness 回答「该不该摘流量」（依赖检查，Down → 503）；liveness 回答
「该不该重启」（恒 200，绝不检查依赖）。两个 handler 都是标准
`http.Handler`——gin、`net/http`、grpc-gateway 都能直接挂。**

```go
h := health.New()                                          // 默认 5s 全局超时
h.Register("mysql", health.PingFunc(sqlDB.PingContext))    // Ping(ctx) error 形态零适配
h.Register("kafka", health.TCP("kafka:9092", 2*time.Second))

srv.GET("/readyz",  health.NewHandler(h).ServeHTTP)        // 依赖检查，Down → 503
srv.GET("/healthz", health.NewLivenessHandler().ServeHTTP) // 进程活着即 200
```

## 0. 引依赖

health 是独立 module，纯标准库零依赖——import 它不拉任何第三方包：

```bash
go get github.com/kalandramo/bald/health
```

## 1. 挂路由：两个探针端点

gin（`ServeHTTP` 方法值与 `gin.HandlerFunc` 同签名，直接传）：

```go
srv.GET("/healthz", health.NewLivenessHandler().ServeHTTP)
srv.GET("/readyz",  health.NewHandler(h).ServeHTTP)
```

标准库 `net/http`：

```go
mux := http.NewServeMux()
mux.Handle("/healthz", health.NewLivenessHandler())
mux.Handle("/readyz",  health.NewHandler(h))
```

readiness 的响应映射只有一条：**Down → 503，Up/Unknown → 200**（宁可乐观：
误摘流量的代价高于故障实例多活一个周期）。响应体带全量明细，排障不用进
容器看日志：

```json
{ "status": "down",
  "checks": { "redis": { "status": "down", "message": "connection refused" } } }
```

配套的 K8s 探针声明：

```yaml
livenessProbe:
  httpGet: { path: /healthz, port: 8080 }
  periodSeconds: 10
readinessProbe:
  httpGet: { path: /readyz, port: 8080 }
  periodSeconds: 5
  timeoutSeconds: 3        # 见第 3 节：与 WithTimeout 对齐
```

## 2. 注册检查器：五种形态

| 形态 | 语义 | 适用 |
| --- | --- | --- |
| `PingFunc(f)` | nil → Up，err → Down | 已有 `Ping(ctx) error` 形态方法（如 `*sql.DB.PingContext`） |
| `TCP(addr, timeout)` | 拨号成功即 Up | DB/缓存/队列等 TCP 依赖 |
| `HTTP(url, timeout)` | GET 响应 2xx/3xx 即 Up | 下游服务、外部 API |
| `AllCheckers(cs...)` | 全过才 Up，遇 Down 短路 | 一个名字下聚合多项子检查 |
| `AnyCheckers(cs...)` | 任一过即 Up | 高可用多副本（一活即可） |

```go
h.Register("mysql", health.PingFunc(sqlDB.PingContext))    // *sql.DB 真零适配
h.Register("tcp-db",  health.TCP("db.internal:5432", 2*time.Second))
h.Register("payment", health.HTTP("http://payment-svc:8080/healthz", 2*time.Second))
h.Register("redis-ha", health.AnyCheckers(                  // 挂一个副本不算故障
    health.TCP("redis-1:6379", 2*time.Second),
    health.TCP("redis-2:6379", 2*time.Second),
))

// go-redis 的 Ping 返回 *redis.Cmd 而非 error——一行闭包适配：
h.Register("redis", health.PingFunc(func(ctx context.Context) error {
    return rdb.Ping(ctx).Err()
}))
```

要点：

- **timeout 传 0 时回退默认 3 秒**（TCP 与 HTTP 同口径）；HTTP 的实际上限
  是 5 秒（默认客户端 `Timeout: 5s` 硬顶，见第 5 节）。
- `PingFunc` 无法产出 `Unknown`（error 只有二态）——需要时写自定义
  `Checker`（第 4 节）。
- `AllCheckers` 的价值在**命名粒度**：`Health` 聚合本身是检查器之间的全
  AND，`All` 让「storage」一个名字内部组合「DB + Redis」，失败消息定位到
  `checker[i]`。

注册即替换（同名覆盖），`Deregister(name)` 缺省空操作，运行期可用。
注册必须显式出现在你的代码里——没有 `init()` 自注册，blank import 不会接线。

## 3. 聚合规则与超时

三态按传染性聚合：

1. 任一 `Down` → 整体 `Down`；
2. 否则任一 `Unknown` → 整体 `Unknown`；
3. 全 `Up` → 整体 `Up`。**零个检查器 → `Up`**（裸进程 readiness 直接通过，
   不强制注册依赖才配拥有 readiness）。

每次探针实时执行、并发跑全部检查器、单次全局超时约束：

```go
h := health.New(health.WithTimeout(2 * time.Second)) // 默认 5s
```

双层超时兜底：检查器实现不受我们控制——`Check` 若无视 ctx，外层 `select`
到期即记 `checker "xxx" timed out` 的 `Down`，聚合照常返回，一个坏检查器
拖不死探针端点。

**与 K8s 对齐**：K8s `timeoutSeconds` 默认 1 秒，health 默认 5 秒——探针侧
先到期的结果是「K8s 判失败但端点没返回」，白摘流量。建议
`WithTimeout` < 探针 `timeoutSeconds`（如 2s / 3s）。

## 4. 自定义检查器

实现 `Checker` 接口，可产出 `Unknown` 与结构化 `Details`（如连接池水位）：

```go
type poolChecker struct{ pool *sql.DB }

func (p *poolChecker) Check(ctx context.Context) health.Result {
    stats := p.pool.Stats()
    if err := p.pool.PingContext(ctx); err != nil {
        return health.Result{Status: health.StatusDown, Message: err.Error()}
    }
    return health.Result{
        Status:  health.StatusUp,
        Details: map[string]any{"open": stats.OpenConnections,
            "in_use": stats.InUse},   // 出现在 checks.<name> 下
    }
}

h.Register("db-pool", &poolChecker{pool: db})
```

`PingFunc` 是它的二态特化；`nil` 检查器返回 `Unknown`（"checker is nil"）。

## 5. 边界与注意事项

- **HTTPChecker 单请求超时上限 5 秒**：默认客户端 `Timeout: 5s` 与请求
  ctx 双重约束取小——`timeout` 参数只能缩短不能放大（默认客户端无注入
  API，设计预留）。探测慢端点先确认 5s 够用。
- **检查器 `Details` 展平合并**：若含 `status`/`message` 键会覆盖结构化
  字段（同名冲突）——自定义 Details 避开这两个键名。
- **组合子构造函数名**是 `AllCheckers/AnyCheckers`（类型是 `All/Any`），
  命名不对称是刻意的（避开 `All` 既是类型又是构造的歧义）。
- **不缓存、不后台轮询**：每次探针实时执行，K8s `periodSeconds` 本身控制
  频率，缓存只会引入陈旧窗口。

## 6. FAQ

**为什么 liveness 不检查依赖？** liveness 失败的后果是重启进程，而重启
解决不了外部依赖故障，只会制造重启风暴。依赖故障的正确出口是 readiness
摘流量、等依赖恢复。

**什么时候用 `AnyCheckers`？** 多副本依赖（redis-1/redis-2）挂一个不算
故障——OR 是 `Health` 聚合表达不了的语义。

**`PingFunc` 什么时候不够用？** 需要产出 `Unknown`、或需要附带 Details
（如连接池水位）时，实现自定义 `Checker` 返回完整 `Result`。

**HTTP 检查器为什么 3xx 也算健康？** 探测目标通常是内部服务域名，重定向
（如 trailing slash）在健康语境下不是故障信号；4xx/5xx 才是——与负载均衡
器的健康判定惯例一致。

**readiness 为什么不配依赖也返回 200？** 不强制业务注册依赖才配拥有
readiness——裸进程（纯计算/无外部依赖的服务）readiness 直接通过是合理
语义。
