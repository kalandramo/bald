# Bald 节点熔断设计：selector 的按节点硬排除桥接层

> Author(s): bald 团队
>
> Discussion at: 源码 `selector/circuitbreaker/{circuitbreaker.go,circuitbreaker_test.go}`；对照 `selector/p2c/p2c.go`（概率性降权的既有形态）、`circuitbreaker/circuitbreaker.go`（契约）、`selector/balancer/balancer.go`（桥接子 module 先例）
>
> Status: Accepted（桥接件已落地；selector 核心与 circuitbreaker 契约零改动）

## 摘要

`selector/circuitbreaker` 把熔断器接成 selector 的**按节点硬排除层**：熔断打开的节点直接不进候选集，调用结果经 `DoneFunc` 反馈进选中节点的熔断器。它补齐的是 p2c 留下的那一格——p2c 的降权是**概率性**的（采样到两个健康节点才提前收敛，坏节点仍经负载平局/forcePick 偶发被选），硬排除是独立职责。

两个导出件：

- `Registry`：按节点地址懒构造并持有熔断器，`Healthy(addr)` 由 `State() != StateOpen` 派生。
- `WithCircuitBreaker(inner, reg)`：装饰任意 `selector.Selector`，Select 前过滤不健康节点，done 时反馈 `MarkSuccess`/`MarkFailure`。

三条承诺：

1. **selector 核心零依赖不变**。`selector/go.mod` 仍只有 module + go 两行；桥接件住在子 module `selector/circuitbreaker`，同时 import selector 与 circuitbreaker（与 `selector/balancer` 同款先例）。
2. **circuitbreaker 契约零改动**。`Healthy` 不进 `CircuitBreaker` 接口——它是 `State()` 的派生查询，住在 Registry 上（见「为什么 Healthy 不进契约」）。
3. **不走 Allow 门控**。熔断器在这里是「每节点健康谕言」（State 查询）+「结果汇」（Mark* 反馈），不是请求闸门（见「为什么不走 Allow」）。

## 背景与动机

### p2c 降权与硬排除是两件事

`selector/p2c` 的 `healthy` 是私有的成功计数阈值判断，只影响两次随机采样的提前收敛——它是**降权**，不是**排除**。坏节点仍在候选集里，仍可能被 forcePick 强制选中（刚恢复的节点不被饿死）或负载平局选中。测试注释自己写明：「硬排除是 WithCircuitBreaker 的职责——本批未移植」。

硬排除需要一个独立的熔断状态来源：节点错误率跳闸后，在睡窗内**完全不进候选集**，睡窗过后经半开试探恢复。这正是 circuitbreaker 四实现已有的能力，缺的只是把它按节点地址接到 selector 上的桥。

### onexmesh 的 WithCircuitBreaker 依赖 BreakerRegistry

onexmesh 的 selector 熔断件依赖其 resilience 的 `BreakerRegistry`（按端点持熔断器）。bald 没有那个子系统，但 circuitbreaker 契约 + 四实现已就位——只需一个按地址懒构造的 Registry 即可吸收该能力，不必移植整个 resilience 子系统。

## 设计

### Registry：按地址懒构造 + State 派生 Healthy

```go
type Factory func() cb.CircuitBreaker

func NewRegistry(factory Factory) *Registry   // factory nil → panic（装配期 fail-fast）
func (r *Registry) Get(addr string) cb.CircuitBreaker  // 懒构造，热路径读锁
func (r *Registry) Healthy(addr string) bool           // State()!=StateOpen；未构造视为健康
func (r *Registry) Prune(nodes []selector.Node)        // 清理 churn 走的地址（经 Close）
func (r *Registry) Close() error                       // 释放所有
```

- **懒构造**：`Healthy` 查询不空建熔断器——尚未构造的地址视为健康（新节点乐观放行），只有被选中的节点才经 `Get` 构造。这避免为从不被选中的节点空建状态。
- **单例**：同地址返回同一实例（双重检查锁，与 `p2c.node` 同款），自适应状态不跨节点共享。
- **剪枝**：`Prune` 清理不再出现在候选集中的地址（经 `Close` 释放），防实例 churn 时 map 无界增长（与 `p2c.prune` 同款）。

### 装饰器：过滤 → 内层选 → 反馈

```go
func WithCircuitBreaker(inner selector.Selector, reg *Registry) selector.Selector
```

`Select` 三步：

1. 用 `reg.Healthy` 过滤出健康子集；子集为空 → 返回 `cb.ErrCircuitOpen`。
2. 委托 `inner.Select(ctx, healthy)` 从健康子集选一个节点。
3. `reg.Get` 取选中节点的熔断器，包裹内层 `DoneFunc`：先调内层 done（释放 inflight、更新 EWMA），再按 `DoneInfo.Err` 记 `MarkFailure`/`MarkSuccess`。

装饰器实现 `selector.Selector`，可包裹任意内层策略（p2c/round_robin/weighted/random），也可经 `selector.MustRegister` 注册成命名策略：

```go
reg := circuitbreaker.NewRegistry(func() cb.CircuitBreaker { return hystrix.New() })
selector.MustRegister("p2c_cb", func() selector.Selector {
    return circuitbreaker.WithCircuitBreaker(p2c.New(), reg)
})
```

### 为什么不走 Allow

契约的 `Allow`/`Mark*` 配对是为**请求闸门**语境设计的（入口链中间件：Allow 放行即欠一次 Mark*）。本桥接件刻意不走 Allow，理由有二：

1. **负载均衡反馈语境里每个调用都是探针**。节点级熔断不需要 Allow 的单探针门控——多个并发请求打到半开节点，成功的 Mark* 让它 heal、失败的 Mark* 让它 reopen，这是「竞争愈合」，对 LB 反而比单探针更合宜（流量一恢复就能继续）。
2. **hystrix 的 `halfOpenIn` 只由 Allow 置位、只由 Mark* 复位**。不调 Allow，`halfOpenIn` 恒 false——Mark* 在半开态直接驱动状态转移，**没有「探针悬着永久锁死」的风险**（那条风险是 Allow/Mark 配对被违反时才出现，见《Bald 熔断设计》「边界与不变量」）。

反过来，若走 Allow 门控会引入真实的拒绝路径（sres 概率拒绝、hystrix 半开探针在途拒绝），而拒绝发生在 `inner.Select` 之后——内层 p2c 已经递增了 inflight，必须调 done 释放，但无论喂成功还是失败都会污染 EWMA（熔断器拒绝不是节点的错）。State 过滤 + Mark* 反馈绕开了这整片耦合。

### 为什么 Healthy 不进契约

`Healthy` 是 `State() != StateOpen` 的派生信息。往 `CircuitBreaker` 接口加 `Healthy()` 是 breaking change——四个实现 + 任何外部实现者都要改，而 `State()` 已经在契约里、信息完备。按《[设计判据](./设计判据.md)》J8（契约只为已实现的东西承诺形状）与 J6（协议只承诺协议形状），派生查询住在消费侧的 Registry 上，契约不超承诺。

## 边界与不变量

### State 语义因实现而异，选实现是有讲究的

`Healthy` 读 `State()`，而四实现的 State 语义不同（详见《[Bald 熔断设计](./Bald%20熔断设计.md)》）：

| 实现 | 适合驱动本过滤？ | 原因 |
|---|---|---|
| `hystrix` | ✅ 首选 | 真三态 + 睡窗；State 带懒转移（Open 过睡窗自动转 HalfOpen），**恢复由 Healthy 查询自身驱动** |
| `sres` | ✅ | Open 对应接受率 ≤0（错误率跳闸）；平滑降级 |
| `vegas` | ⚠️ 需喂 RecordLatency | 看延迟不看错误；MarkSuccess 喂零延迟被 minRTT 过滤，须显式 RecordLatency |
| `sentinel` | ❌ 不宜 | State() 每次发探针 Entry 会污染 Sentinel 统计窗 |

### 恢复经接口可达

hystrix 的 `State()` 在 Open 过睡窗时懒转移到 HalfOpen——`Healthy` 查询本身就把恢复后的节点重新放进候选集，不需要调用方持有具体类型。半开节点的 Mark* 直接 heal/reopen，槽位不泄漏（因为根本没用 Allow 置槽）。

### 剪枝与在途 done

`Prune` 只清理**不在当前候选集**中的地址。被剪枝的熔断器经 Close 释放；若其地址仍有上一轮 Select 的在途 done 未触发，该 done 持有熔断器指针（不会被 GC），Mark* 落在已 Close 的熔断器上是无害的（hystrix/sres 的 closed 标志只影响 Allow，Mark* 照常计数）。

## 实现与验证

`selector/circuitbreaker/` 独立 module（require selector + circuitbreaker + circuitbreaker/hystrix，三条本地 replace 跟踪同仓兄弟，与 selector/balancer 同款）。测试 13 个：

- 确定性 fake 熔断器：nil 工厂 panic、Healthy 未知即健康且不空建、Get 懒构造单例、**硬排除 100%**（round_robin 内层跑 100 次不选坏节点——区别于 p2c 降权的宽界断言）、全 Open 回 ErrCircuitOpen、空候选回 ErrCircuitOpen、done 反馈计数、State 查询驱动恢复、Prune 释放陈旧、稳态剪枝不误删、Close 释放所有。
- 真 hystrix 集成：done→MarkFailure 喂失败跳闸 → 硬排除选 good → 睡窗后恢复 HalfOpen 重新可选。
- 并发：16 goroutine × 200 次 Select+done，`-race` 下钉住 Registry 与装饰器并发安全。

### 验证方式

```bash
cd selector/circuitbreaker && go vet ./... && go test -race -count=1 ./...
```

门禁清单由 `scripts/list-modules.sh` 自动派生，新 module 无需手改 Taskfile；`task race`/`task lint`/`task vuln` 与 docscheck 的 module-list 闸门自动覆盖。