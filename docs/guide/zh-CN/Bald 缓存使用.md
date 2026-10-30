# Bald 缓存使用

面向使用者的操作手册：默认行为是什么、启用 redis 后端要做哪几步、local
怎么接入、实例怎么取、契约操作语义怎么用、回源击穿怎么交给 loadable 组
合器、报错了怎么修。设计论证（为什么契约只认 []byte、为什么回源必须合
并并发、为什么组合器不进装配）见 `docs/devel/zh-CN/Bald 缓存设计.md`，
本文只讲操作。

## 心智模型：一句话版

**缓存生效 = 后端装配 + 按需组合两件事。后端以段驱动 Provider 接入：
`rediscontract.AppProvider()` 直接作为 `MustServe` 变参项，消费契约
`cache.redis` 段，实例落资源面、经 `rediscontract.Redis.Resource(app)`
取用；回源组合（miss → 查库 → 回填）不进装配，用 `loadable.New` 显式
构造——loader 是业务函数，配置变不出来。**

| | 契约装配（段驱动 Provider） | 直用构造（New + Option） |
| --- | --- | --- |
| 时机 | 启动期一次性（阶段 B，业务钩子之前） | 业务代码任意时点 |
| 参数来源 | yaml `cache.redis` 段 | Option 链代码声明 |
| 生命周期 | 框架管（停机 Effect：缓存先关、数据库最后关） | 业务管（Close 自理） |
| redis client | contract 自建 + Ping 校验 + 停机关闭 | 注入已有 client，Close 不关它 |
| 实例取用 | `rediscontract.Redis.Resource(app)`（域 Wire 内） | 自己持有返回值 |
| 适合 | 大多数项目：启动即正确 | 测试 / 复用已有连接 / 细粒度控参 |

## 0. 什么都不做：默认行为

`main.go` 不接任何缓存 Provider、yaml 不配 `cache` 段：

- 装配 no-op，资源面无 cache 产物（`rediscontract.Redis.Resource(app)` 恒
  `(nil, false)`）——缓存零成本可关闭。
- 与审计不同，**没有零注册的内置后端**：local 虽是纯内存实现，同样要
  引 module + 接 Provider（`cache` 契约与各后端是独立 module，用的人付
  依赖代价）。
- `cache` 段不支持热更新（与 database 段同理）。

## 1. 启用 redis：装配三步

**步骤 1：引依赖**（后端是独立 module）：

```bash
go get github.com/kalandramo/bald/cache/redis
```

注：`cache`、`cache/loadable`、`cache/local`、`cache/redis` 均已发 tag，
`go get` 即用。

**步骤 2：`MustServe` 变参里接一行 Provider**（Provider 即 Option，直接
作为变参项；`cache.redis` 段缺失即被引擎跳过，不接也合法）：

```go
import (
    rediscontract "github.com/kalandramo/bald/cache/redis/contract"
    "github.com/kalandramo/bald/pkg/appkit"
    "github.com/kalandramo/bald/pkg/appkit/std"
)

appkit.MustServe(ctx,
    std.Standard(),
    rediscontract.AppProvider(), // 契约 cache.redis 段 → 资源面
    // ...业务域与其余 Option
)
```

`AppProvider()` 复用同包 `Provider` 的构造逻辑：段存在 → 校验 / 建连 /
Ping fail-fast（不产半成品实例）→ 产物 `map{"redis": 实例}` 落引擎 cache
资源面 → cleanup 关闭底层连接池（停机逆序回放：缓存先关、数据库最后关）。
没有 `init()` 自注册——接入必须显式出现在你的代码里。

**步骤 3：yaml 声明**：

```yaml
cache:
  local:                     # 进程内缓存，可选
    size: 536870912          # 预分配大小（字节），缺省 256MB
    default_ttl_seconds: 300 # Set(ttl=0) 的兜底 TTL，0=永不过期
  redis:                     # 分布式缓存，可选（addr / cluster / sentinel 三选一）
    addr: "localhost:6379"   # 单节点模式必填；cluster/sentinel 模式须留空
    password: ""             # 密码，三种模式通用
    db: 0                    # 数据库编号，单节点/哨兵有效（集群不支持）
    key_prefix: "myapp:"     # 命名空间隔离，可选
    # cluster:               # 集群模式：段存在即启用（与 sentinel 互斥）
    #   addrs: ["10.0.0.1:6379", "10.0.0.2:6379"] # 种子节点，至少一项
    #   max_redirects: 3    # MOVED/ASK 跟随次数，缺省 go-redis 内置默认
    # sentinel:              # 哨兵模式：段存在即启用（与 cluster 互斥）
    #   master_name: "mymaster" # Sentinel 监视的主节点名，必填
    #   addrs: ["10.0.0.1:26379"] # Sentinel 节点，至少一项
```

**取实例（时机关键）**：装配发生在 Run 期阶段 B（框架 BeforeStart 链内、
契约装载校验之后），而 `Serve` / `MustServe` 不返回 app 句柄——业务的
官方取用窗口是**域 Wire**（执行时资源面已就绪、服务器尚未构建）：

```go
var myCache cache.Cache

appkit.MustServe(ctx,
    std.Standard(),
    rediscontract.AppProvider(),
    appkit.Domain{
        Name: "myapp",
        Wire: func(ctx context.Context, app *appkit.App) error {
            if c, ok := rediscontract.Redis.Resource(app); ok {
                myCache = c // 桥接给业务层
            }
            return nil
        },
    },
    // ...
)
```

`appkit.Key[T]` 按契约段路径取阶段 B 产物，三种形态（见
`pkg/appkit/resource.go`）：

- **子段精确路径**（`NewKey[*rediscache.Cache]("cache.redis")`）→ 实例本身
  （`cache.Cache` 实现）；
- **顶层段**（`NewKey[map[string]any]("cache")`）→ `map[string]any` 副本快照
  （键 = 子段名）——可安全遍历 / 留存，不暴露内部 map，内部后续写入不影响
  已取出的快照；
- **表外（第三方）段**：map 产物按 `<顶层段>.<子段>` 展开、非 map 产物
  落 `<顶层段>`（引擎统一约定）。

未装配或段缺失一律 `(nil, false)`；域 Wire 执行时阶段 B 已完成，cache
段存在则实例必已就位。

**local 侧的现状**：`cache/local/contract.Provider(ctx, cfg)` 可直接复用，
但 local 没有现成的 `AppProvider()`——要经契约装配启用 local，照第 5 节
自写一个薄 Provider（Build 里复用 `localcontract.Provider`），或直接直用
构造。注意 L3 覆盖检查只看顶层段：只接 redis Provider 时，yaml 里的
`cache.local` 不报错、也**静默不生效**。

**原生 client 访问器**：适配器暴露 `(*rediscache.Cache).Client()`，返回
`goredis.UniversalClient`——与适配器持有的是**同一实例**（非副本、非代
理）：适配器方法与业务经该句柄的操作共用同一连接池、同一键空间。典型
用途是 TokenStore / 验证码这类需要原生能力的场景（INCR 原子计数、SCAN、
SUBSCRIBE、定制 Pipeline 等）：

```go
rc := myCache.(*rediscache.Cache) // 第 1 节取回的实例
rdb := rc.Client()                // goredis.UniversalClient，与适配器同一实例
n, err := rdb.Incr(ctx, "captcha:attempts").Result()
```

- **所有权不转移**：契约轨（AppProvider / Provider）的 client 归构造期
  注册的 cleanup（停机关闭连接池），业务持句柄时勿再 Close；直用轨（`New`
  注入）归调用方，`Cache.Close` 不关它。
- **keyPrefix 只对适配器方法生效**：经本句柄读写的裸键不经过前缀——同
  一键空间里两套键名规则，跨轨排查时留意。
- **集群 / 哨兵部署**下动态类型是 `*goredis.ClusterClient` /
  `*goredis.FailoverClient`，需要具体形态时自行断言。

## 2. 双后端速查

| | `local`（进程内） | `redis`（分布式） |
| --- | --- | --- |
| module | `cache/local` | `cache/redis` |
| 底层 | freecache 零 GC 环形缓冲 | go-redis v9 |
| 接入 | 自写 Provider（第 5 节）或直用构造 | `rediscontract.AppProvider()` |
| 参数子段 | `local.size` / `local.default_ttl_seconds` | `redis.addr` / `redis.password` / `redis.db` / `redis.key_prefix` + `redis.cluster{addrs,max_redirects}` / `redis.sentinel{master_name,addrs}` |
| GetMulti / SetMulti | 进程内顺序迭代 | MGET / Pipeline 单次网络往返 |
| SetNX | check-then-set（非严格原子） | 原生 SET NX（原子） |
| Close | 清空内存 | contract 轨关闭自建 client；直用轨不关注入 client |
| 指标 | EntryCount / HitCount / MissCount / EvacuateCount / ExpiredCount | —（走 Redis 侧监控） |
| 直用构造 | `local.New(local.WithSize(n), local.WithDefaultTTL(t))` | `rediscache.New(rdb, rediscache.WithKeyPrefix("myapp:"))` |

直用 Option：local 的 `WithSize`（默认 256MB）、`WithDefaultTTL`（缺省
永不过期；TTL 秒级精度，亚秒向上取整）；redis 的 `WithKeyPrefix`。

**集群 / 哨兵**：契约轨与直用轨均支持。契约轨在 `cache.redis` 下加
`cluster`（种子 `addrs`）或 `sentinel`（`master_name` + `addrs`）段即
启用——互斥 fail-fast，`addr` 须留空（防配置漂移），集群模式不支持
`db`；直用轨 `New` 接受 `goredis.UniversalClient` 接口——自建
`redis.NewClusterClient` / `redis.NewFailoverClient`（或
`NewUniversalClient`）注入即可，生命周期归业务（Close 不关它，这对集群
client 正是合理语义）。cluster 模式下 MGET / Pipeline 由 go-redis 按
slot 自动拆分组执行，「单次往返」退化为按 slot 分组的若干次；SetNX 是
单 key 命令，原子性不受影响。

## 3. 契约操作语义

8 方法接口（`cache.Cache`）：

```go
Get(ctx, key) ([]byte, error)            // miss 返回 ErrNotFound
Set(ctx, key, value, ttl) error          // ttl=0：local 走默认 TTL，redis 永不过期
SetNX(ctx, key, value, ttl) (bool, error)
Delete(ctx, key) error                   // key 不存在为 no-op
Has(ctx, key) (bool, error)
GetMulti(ctx, keys) ([][]byte, error)
SetMulti(ctx, items []Item) error        // TTL 语义同 Set
Close() error
```

- **miss 判定**：`errors.Is(err, cache.ErrNotFound)`——哨兵错误，勿比较
  字符串。miss 与故障（网络错误等）由此可区分。
- **序列化责任在调用方**：value 是 `[]byte`，JSON / protobuf 自选，框架
  不替业务做编解码决策。key 是 `string`——redis-cli 可直接按 key 排查。
- **GetMulti 对齐规则**：返回切片恒 `len(keys)` 长、与输入顺序对齐；任一
  key 缺失则对应位 `nil` 且整体错误为 `ErrNotFound`——部分命中无需分支。
- **SetNX 是锁原语不是锁语义**：跨进程协调（租约、续期、活锁规避）由
  业务在原语之上组合。严格互斥必须用 redis 后端（local 是 check-then-set，
  两次调用间存在竞态窗口）。
- **TTL 语义**：`Set` 与 `SetMulti` 的 ttl=0 语义一致——local 后端走
  默认 TTL（`default_ttl_seconds` / `WithDefaultTTL`，未配置则永不过期）；
  redis 后端无默认 TTL 概念，即永不过期。

## 4. 回源组合器：loadable

业务读路径的「查缓存 → miss → 查库 → 回填」样板与进程内击穿，一行交给
loadable（独立 module，依赖仅 `golang.org/x/sync`）：

```go
import "github.com/kalandramo/bald/cache/loadable"

func loadFromDB(ctx context.Context, key string) ([]byte, error) { ... }

c := loadable.New(backend, loadFromDB, loadable.WithTTL(5*time.Minute))
val, err := c.Get(ctx, "user:1") // miss → singleflight 合并 → 回填 → 返回
```

backend 可以是契约装配的实例（`rediscontract.Redis.Resource(app)` 取回的）或直用构造的。

Get 关键路径四步：

1. `backend.Get` 命中 → 直接返回（命中路径零额外开销，loader 不被触碰）；
2. miss → singleflight 把同 key 并发 miss 合并为一次 loader 调用——
   热点 key 过期瞬间的 N 个并发请求只打一次库；
3. loader 成功 → best-effort 回填（`WithTTL` 默认 TTL，0=语义同 Set）→
   返回值；
4. loader 失败 → 错误透传，**不回填**——不留脏数据，下次请求自然重试。

语义边界：

- **回填 best-effort**：`backend.Set` 失败时值照常返回——已拿到有效数据
  的读不该失败，下次请求再 miss 再回填。
- **负缓存**：loader 返回 `(nil, nil)` 或空切片也回填——业务自定义空值
  策略即等价于业务级负缓存（框架级负缓存显式不做，见设计文档）。
- **context 传播**：合并加载由第一个 caller 的 context 驱动，若它中途
  取消，同 flight 的所有 caller 都观察到取消错误（singleflight 固有语义）。
- **透传族**：`Set/SetNX/Delete/Has/SetMulti/Close` 直通 backend；
  `GetMulti` 逐 key 走 Get——Get 会回源则 GetMulti 也回源，语义一致。

loadable 不进段驱动装配（无契约段可消费）：loader 是业务函数，框架无法
从配置里变出它——「配置驱动参数，代码声明能力」。跨进程合并（多副本同
时 miss）用契约层 `SetNX` 原语自行组合。

## 5. 自定义后端：直用构造与段驱动 Provider

**直用构造**（零装配）：实现 8 方法 `cache.Cache` 接口（须并发安全），
业务自行构造、自管生命周期：

```go
type memcachedCache struct{ /* ... */ }

func (m *memcachedCache) Get(ctx context.Context, key string) ([]byte, error) { /* ... */ }
// ... 其余 7 方法

c := newMemcachedCache()
defer c.Close()
```

**段驱动接入**：实现 `appkit.Provider` 三件套（Segments / Apply / Build，
Provider 即 Option，与 `std.Standard()` 并列传入 `MustServe`）。local 暂
无现成接入面，下例即其接线配方（Build 复用既有构造）：

```go
type localCacheProvider struct{}

func (localCacheProvider) Segments() []string { return []string{"cache.local"} }
func (localCacheProvider) Apply(d *appkit.Decl) {
    d.Providers = append(d.Providers, localCacheProvider{})
}

func (localCacheProvider) Plan(context.Context, appkit.PlanScope) ([]appkit.Node, error) {
    return []appkit.Node{{Key: "cache.local", Phase: appkit.PhaseBootstrap,
        Priority: appkit.PriorityCache}}, nil
}

func (localCacheProvider) Build(ctx context.Context, s appkit.ApplyScope, _ appkit.Node) (any, appkit.Cleanup, error) {
    prod, cleanup, err := localcontract.Provider(ctx, s.Config.GetCache())
    if err != nil {
        return nil, nil, err
    }
    // 产物必须是 map（键=子段名）：引擎按 "<顶层段>.<子段>" 展开落资源面。
    return map[string]any{localcontract.Type: prod},
        func(context.Context) error { cleanup(); return nil }, nil
}
```

四条硬约定：

- **段路径须是契约里真实存在的段**（proto 字段），否则 Provider 被静默
  跳过（「段缺失 = 合法」语义）——bconf 现有 cache 子段只有 `local` /
  `redis`，新后端要占自己的段须先在 bconf 加字段（框架侧演进），yaml 里
  的未知键不会进契约。
- **产物形状**：cache 属六资源域，产物必须是 `map[string]any`（键 = 子段
  名），展开后经 `appkit.NewKey[cache.Cache]("cache.local").Resource(app)`
  这类段路径取用；非 map 产物启动期 fail-fast。
- **同段路径末者胜**：后声明的 Provider 接管同一路径，可用来顶替
  `cache.redis` 的默认后端；不存在重名注册 panic。
- **cleanup 挂停机 Effect**：cache 槽位在 database 之后注册，逆序回放
  保证缓存先关、数据库最后关。

## 6. 报错解读：fail-fast 及修复

段驱动装配下不存在注册表类报错（no CacheRegistry wired / provider not
registered / already registered）。报错只有两类来源：**L3 段覆盖检查**
（段存在而无人消费，启动期报错）与 **Provider fail-fast**（段被消费但
校验 / 建连失败，错误经引擎包装为 `appkit: provider [cache.redis]: …`
前缀）。

**cache 段存在但没有任何 Provider 接入**（漏接 `AppProvider()`）——L3
段覆盖检查，启动期报错：

```text
appkit: bootstrap config declares sections with no consumer:
  top-level sections with no consumer: cache
  hint: 该段无消费者，配置不会生效；引入对应 Provider、经 appkit.WithUnimplementedSegments 显式豁免，或从契约移除该段
```

修复：`MustServe` 变参加 `rediscontract.AppProvider()`，或照第 5 节自写
Provider / `appkit.WithUnimplementedSegments("cache")` 显式豁免 / 删掉
该段。L3 只查顶层段：cache 因任一 cache Provider 在而视为已消费，未接管
的子段（如只接 redis 时的 `cache.local`）不报错、静默不生效。

**redis.addr 缺失**：

```text
appkit: provider [cache.redis]: cache: redis.addr is required
```

修复：yaml 补 `cache.redis.addr`。

**cluster 与 sentinel 同时声明**：

```text
appkit: provider [cache.redis]: cache: redis.cluster and redis.sentinel are mutually exclusive
```

修复：三选一——`addr`（单节点）/ `cluster` / `sentinel`。

**cluster/sentinel 模式下还留着 addr**（配置漂移防护）：

```text
appkit: provider [cache.redis]: cache: redis.addr is only valid in standalone mode (remove it when cluster section is present)
```

（sentinel 模式下同一前缀，括号提示为 `remove it when sentinel section is present`。）

修复：删掉 `addr`（或去掉 cluster/sentinel 段回到单节点）。

**集群模式配了 db**（Redis Cluster 不支持 SELECT）：

```text
appkit: provider [cache.redis]: cache: redis.db is not supported in cluster mode
```

修复：去掉 `db`（单节点/哨兵模式可用）。

**client 构建失败**（地址不通 / 密码错 / Ping 超时）：

```text
appkit: provider [cache.redis]: cache: build redis client: dial tcp ...
```

修复：检查地址、密码、网络连通；失败前已构建的其他段实例由 Run 失败路
径逆序回放 cleanup，无泄漏。

## 7. 边界与注意事项

- **跨 module 引用**：`bald/cache`、`bald/cache/local`、`bald/cache/redis`、
  `bald/cache/loadable` 是独立 module 且均已发 tag，外部引用 require 对应
  module 即可；gopls 对嵌套 module 常报 BrokenImport 假阳性，以命令行
  build/test 为准。
- **停机顺序**：缓存 Effect 注册在 database-clients 之后，逆序回放保证
  「缓存先关（加速层，关了不影响正确性）、数据库连接最后关」。
- **不支持热更新**：cache 段变更不触发重装配（与 database 段同理）。
- **redis 直用轨的 Close 不关注入 client**：连接池、TLS、生命周期归调用方。
- **local 的 SetNX 有竞态窗口**：严格互斥场景必须用 redis 后端。
- **local 的 TTL 秒级精度**：亚秒 duration 向上取整。
- **业务旁路缓存（Cache-Aside）用 `cache/loadable`**：本层是通用 KV 缓存
  抽象（Get/Set/SetNX/Multi，进程内/分布式），面向任意键值加速；
  `cache/loadable` 在其上组合出读穿透（miss → loader → 回填 + singleflight
  合并并发 miss）的业务旁路语义。「Redis 故障降级直连 loader」由
  `cache/loadable` 的 `WithDegradeOnError()` 选项表达。
- **显式不做**（防 YAGNI 争议回潮）：L2 两级缓存（无失效广播的 Del 只清
  本节点）、框架级负缓存（哨兵值污染 []byte 语义）、typed 泛型便利层——
  见设计文档不做清单。

## FAQ

**Q：local 和 redis 能同时用吗？** 能：段驱动装配按段路径各归各——redis
接现成 `AppProvider`，local 照第 5 节自写 Provider 或直用构造；两段并存
各自装配、经 `rediscontract.Redis.Resource(app)` /
`appkit.NewKey[cache.Cache]("cache.local").Resource(app)` 分别取回。典型
用法 local 做热点加速 + redis 做共享层——但框架不提供两级联动与失效广播，
一致性业务自理（这正是 L2 显式不做的边界）。

**Q：Get miss 为什么返回错误而不是 nil？** `ErrNotFound` 哨兵让 miss 与
故障可区分：`errors.Is` 命中走回源，其他错误向上报。返回 nil 会丢掉这层
区分。

**Q：loadable 能套 loadable 吗？** 能（自身实现 Cache 契约），但单层回源
已覆盖常见场景，叠加一般无意义。

**Q：回填失败会怎样？** best-effort：值照常返回，下次请求再 miss 再回填
——缓存写失败不该失败一次已拿到有效数据的读。

**Q：多副本同时 miss 怎么办？** singleflight 只合并进程内并发；跨进程用
契约层 `SetNX` 原语自行组合（框架不预设分布式锁语义——租约、续期、活锁
规避是业务决策）。

**Q：业务旁路缓存（Cache-Aside）怎么做？** 用 `cache/loadable` 包在通用
KV 抽象之上组合——miss → loader → 回填，singleflight 合并并发 miss；
Redis 故障时用 `WithDegradeOnError()` 直连 loader。
