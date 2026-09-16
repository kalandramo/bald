# Bald 缓存使用

面向使用者的操作手册：默认行为是什么、启用 local/redis 后端要做哪几步、
契约操作语义怎么用、回源击穿怎么交给 loadable 组合器、报错了怎么修。
设计论证（为什么契约只认 []byte、为什么回源必须合并并发、为什么组合器
不进装配）见 `docs/devel/zh-CN/Bald 缓存设计.md`，本文只讲操作。

## 心智模型：一句话版

**缓存生效 = 后端装配 + 按需组合两件事。后端契约段 `cache.local` /
`cache.redis` 可并存：引一个依赖 + 两行注册 + yaml 加一段即得实例；回源
组合（miss → 查库 → 回填）不进装配，用 `loadable.New` 显式构造——loader
是业务函数，配置变不出来。**

| | 契约装配（FromBootstrap） | 直用构造（New + Option） |
| --- | --- | --- |
| 时机 | 启动期一次性（BeforeStart，首个请求前） | 业务代码任意时点 |
| 参数来源 | yaml `cache.local` / `cache.redis` 段 | Option 链代码声明 |
| 生命周期 | 框架管（停机 Effect：缓存先关、数据库最后关） | 业务管（Close 自理） |
| redis client | contract 自建 + Ping 校验 + 停机关闭 | 注入已有 client，Close 不关它 |
| 适合 | 大多数项目：启动即正确 | 测试 / 复用已有连接 / 细粒度控参 |

## 0. 什么都不做：默认行为

`main.go` 不写任何缓存代码、yaml 不配 `cache` 段：

- 装配 no-op，`app.Cache()` 恒空——缓存零成本可关闭。
- 与审计不同，**没有零注册的内置后端**：local 虽是纯内存实现，同样要
  引 module + 注册（`cache` 契约与各后端是独立 module，用的人付依赖代价）。
- `cache` 段不支持热更新（与 database 段同理）。

## 1. 启用 local / redis：通用三步

以 redis 为例（local 同理；两段是 optional 段集合，可并存、各自装配）。

**步骤 1：引依赖**（后端是独立 module）：

```bash
go get github.com/kalandramo/bald/cache/redis
```

注：`cache`、`cache/loadable`、`cache/local`、`cache/redis` 均已发 tag，
`go get` 即用。

**步骤 2：main.go 两行注册**：

```go
import (
    "github.com/kalandramo/bald/bootstrap"
    "github.com/kalandramo/bald/pkg/appkit"
    localcontract "github.com/kalandramo/bald/cache/local/contract"
    rediscontract "github.com/kalandramo/bald/cache/redis/contract"
)

cr := bootstrap.NewCacheRegistry()
cr.MustRegister(localcontract.Type, localcontract.Provider)  // "local"，可选
cr.MustRegister(rediscontract.Type, rediscontract.Provider)  // "redis"
app, err := appkit.FromBootstrap(cfg, appkit.WithCacheRegistry(cr), ...)
```

没有 `init()` 自注册——注册必须显式出现在你的代码里。

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

**取实例（时机关键）**：装配发生在 BeforeStart（契约装载校验后），
FromBootstrap 构造期 `app.Cache()` 返回空。业务经 `WithBeforeStart` 桥接
（追加在框架装载链之后执行，契约终值可读）：

```go
var app *appkit.AppKit // 前置声明：闭包需捕获 app，而 := 的作用域从语句末才开始
app, err := appkit.FromBootstrap(cfg,
    appkit.WithCacheRegistry(cr),
    appkit.WithBeforeStart(func(ctx context.Context) error {
        if v, ok := app.Cache("redis"); ok {
            myCache = v.(cache.Cache) // 桥接给业务层
        }
        return nil
    }),
)
```

生产范例见 go-bald-admin 的 `cacheRegistry()` + `WithBeforeStart` 接线。

## 2. 双后端速查

| | `local`（进程内） | `redis`（分布式） |
| --- | --- | --- |
| module | `cache/local` | `cache/redis` |
| 底层 | freecache 零 GC 环形缓冲 | go-redis v9 |
| 注册 | `localcontract.Provider` | `rediscontract.Provider` |
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

backend 可以是契约装配的实例（`app.Cache("redis")` 取回的）或直用构造的。

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

loadable 不进 CacheRegistry：loader 是业务函数，框架无法从配置里变出它
——「配置驱动参数，代码声明能力」。跨进程合并（多副本同时 miss）用契约层
`SetNX` 原语自行组合。

## 5. 自定义后端

实现 8 方法 `cache.Cache` 接口（须并发安全），直用构造即得：

```go
type memcachedCache struct{ /* ... */ }

func (m *memcachedCache) Get(ctx context.Context, key string) ([]byte, error) { /* ... */ }
// ... 其余 7 方法

c := newMemcachedCache()
defer c.Close()
```

注意：契约装配的段枚举固定为 local → redis（`bootstrap` 的
`cacheSections`，proto 字段序）——注册自定义 Provider 不会被调度。新后端
要进契约装配须先在 bconf 加段（框架侧演进）；业务仓的现实路径是直用构造
+ 业务自管生命周期。

## 6. 报错解读：八类 fail-fast 及修复

**配了 cache 段但没接 CacheRegistry**（漏了 WithCacheRegistry）：

```text
appkit: bootstrap.cache present but no CacheRegistry wired (WithCacheRegistry missing)
```

修复：FromBootstrap 加 `appkit.WithCacheRegistry(cr)`。

**段存在但没注册**（漏了步骤 2）：

```text
bootstrap: cache.local present but provider not registered (import the backend contract package and MustRegister it)
```

修复：照第 1 节步骤 2 补 `MustRegister`。

**redis.addr 缺失**：

```text
bootstrap: build cache redis: cache: redis.addr is required
```

修复：yaml 补 `cache.redis.addr`。

**cluster 与 sentinel 同时声明**：

```text
cache: redis.cluster and redis.sentinel are mutually exclusive
```

修复：三选一——`addr`（单节点）/ `cluster` / `sentinel`。

**cluster/sentinel 模式下还留着 addr**（配置漂移防护）：

```text
cache: redis.addr is only valid in standalone mode (remove it when cluster section is present)
```

（sentinel 模式下同一前缀，括号提示为 `remove it when sentinel section is present`。）

修复：删掉 `addr`（或去掉 cluster/sentinel 段回到单节点）。

**集群模式配了 db**（Redis Cluster 不支持 SELECT）：

```text
cache: redis.db is not supported in cluster mode
```

修复：去掉 `db`（单节点/哨兵模式可用）。

**client 构建失败**（地址不通 / 密码错 / Ping 超时）：

```text
bootstrap: build cache redis: cache: build redis client: ...
```

修复：检查地址、密码、网络连通；已构建的其他段实例会被逆序回滚，无泄漏。

**重名注册**（panic）：

```text
bootstrap: cache provider "redis" already registered
```

修复：去掉重复注册。

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
- **与 contrib/cache-redis 的边界**：本层是通用 KV 缓存抽象（Get/Set/
  SetNX/Multi，进程内/分布式），面向任意键值加速；contrib/cache-redis 是
  带 loader 回填的业务旁路缓存组件（Cache-Aside），长在具体后端之上——
  关注点不同，互不替代。
- **显式不做**（防 YAGNI 争议回潮）：L2 两级缓存（无失效广播的 Del 只清
  本节点）、框架级负缓存（哨兵值污染 []byte 语义）、typed 泛型便利层——
  见设计文档不做清单。

## FAQ

**Q：local 和 redis 能同时用吗？** 能：契约段是 optional 段集合，两段并存
各自装配、分别取回。典型用法 local 做热点加速 + redis 做共享层——但框架
不提供两级联动与失效广播，一致性业务自理（这正是 L2 显式不做的边界）。

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

**Q：缓存和 contrib/cache-redis 什么关系？** 见 §7 边界——通用 KV 抽象 vs
业务旁路缓存组件，关注点不同。
