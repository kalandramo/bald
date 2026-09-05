# Bald 配置系统设计

## 1. 配置源

### 1.1 定位

`bconfig` 包定义的是 **配置源抽象层 + 组合器**，自身不含任何具体后端实现——后端分布在同级 9 个子包（file / env / fs / http / etcd / consul / nacos / apollo / kubernetes），每个子包按需引入自己的后端 SDK（不用的 provider 不进二进制）。

一图概括职责划分：

```
┌─ bconfig 包（本文档范围）──────────────────────────┐
│  能力轴（接口）  +  组合器（FallbackReader）        │
└────────────────────────────────────────────────┘
        ↑ 实现                      ↑ 组装
   9 个 provider              上层框架 / 业务
```

### 1.2 能力轴与接口契约（`bconfig.go`）

| 轴 | 接口 | 方法 | 可选性 |
|---|---|---|---|
| 读 | `Reader` | `Load(ctx, key) ([]byte, error)` | 必需 |
| 关 | `Closer` | `Close() error` | 可选 |
| 变更通知（信号） | `Watcher` | `Watch(ctx, key) (<-chan struct{}, error)` | 可选 |
| 变更通知（推值） | `ValueWatcher` | `WatchValue(ctx, key) (<-chan []byte, error)` | 可选 |
| 解码 | `Decoder` | `Decode(data []byte, out any) error` | 正交独立 |

预定义的组合名只有两个：`ReadCloser = Reader + Closer`、`ReadWatcher = Reader + Watcher`。

#### 1.2.1 设计要点

1. **接口最小化 + 能力可选发现**。没有 `Provider` 这类大而全的接口，后端按需实现，调用方用**类型断言**在运行期探测能力（见 `fallback.go` 的 `r.(Closer)`、`r.(ValueWatcher)`）。`ValueWatcher` 的注释明确声明它是 *optional*。
2. **两种 watch 语义并存**。
   - `Watcher`：只通知「变了」，收到后仍需 `Load` 回读（多一次 IO，但普适）。
   - `ValueWatcher`：连新字节一起推送（省一次 IO，etcd/consul/nacos 等 SDK 本就能直接给新值）。
3. **`Decoder` 与读/监听完全解耦**。源只吐字节，格式由调用方注入。
4. **可增量实现**：只写 `Load` 也能当配置源用（`env`、`fs` 即如此），不要求一次实现全套。

---

### 1.3 契约的形状定义（`bconfig_test.go`）

该文件**不是功能测试**，它定义的是「每个接口的最小方法集」，做两件事：

1. **编译期断言**（`bconfig_test.go:46-54`）：`_ Reader = mockReader{}` 等 7 行，把接口形状钉死——任何人想给接口加方法，这里立刻编译失败。
2. **可组合性断言**（`TestInterfaces_Composable`）：明确三条关系——`ReadCloser` 可作为组合体使用；`ValueWatcher` **可以独立于 `Watcher` 单独实现**；`Decoder` 与读/监听互不依赖。

价值：证明这套抽象支持「渐进实现」，后端可以只实现一部分能力而不破坏契约。

---

### 1.4 级联组合器 `FallbackReader`（`fallback.go`）

这是本包唯一有真实行为的组件。用途是多配置源级联回退：比如高优先级的 `env` 覆盖低优先级的 `fs`，或「远程配置中心 + 本地默认值文件」按优先级兜底。

`fallback.go` 里 Load 按优先级取第一个成功源、Close 关所有可关闭子源、WatchValue 合并所有可推值子源的变更。

它的能力由 `fallback_test.go` 逐条固化：

| 能力 | 语义 | 对应测试 |
|---|---|---|
| 多源按序降级 | 按传入顺序依次 `Load`，第一个「无 error 且 data 非 nil」者胜出 | `Load_FirstSucceeds` / `Load_FallbackToSecond` / `Load_SkipErrors` |
| 失败聚合 | 全失败 → `errors.Join` 所有子错误；全返回 nil（无 error）→ 报 `no source could resolve key` | `Load_AllFail` / `Load_AllNil` |
| 生命周期传递 | `Close()` 遍历子源，只关实现了 `Closer` 的，聚合错误 | `Close_AllClosers` / `Close_SkipsNonClosers` / `Close_JoinErrors` |
| 多源 watch 合并 | 把所有实现了 `ValueWatcher` 的子源合并为**一个** channel | `WatchValue_*` |
| 空能力显式报错 | 无子源实现 `ValueWatcher` → 明确报错，而非静默「永不触发」 | `WatchValue_NoWatchers` |
| ctx 取消即关 channel | `cancel()` 后输出 channel 被 close | `WatchValue_ContextCancel` |


#### 1.4.1 最关键的一条语义

`fallback.go` 的 `WatchValue`：任一子源推送变更时，**丢弃推送来的值，重新走一遍 `Load` 全链路**，再把「重算后的有效值」转发。

测试 `WatchValue_MultipleWatchers` 正是验证这一点：低优先级源 r2 变更并推送通知，但高优先级源 r1 仍有值 → 合并 channel 推出的仍是 `"a"`（r1 的值）；只有 r1 变更时才推出 `"a-updated"`。

> 若直接用推送值转发，多源**遮蔽（shadowing）语义就会崩**。

这条可推广为一个通用原则：
**永远不要把「事件里带着的值」当真值，只把事件当作「该重算了」的触发器。**

#### 1.4.2 工程细节

- **防 goroutine 泄漏**：先收集齐所有 sub channel 再起 goroutine（`fallback.go` 注释明写），中途某个 `WatchValue` 失败即整体返回错误。
- **构造即校验**：`NewFallbackReader()` 无参返回 error，而不是造出一个「永远失败」的对象。
- 输出 channel 缓冲为 1：合并后不保证每个源事件都一一送达（合并/去重语义），慢消费者下的新值会覆盖旧值。

---

### 1.5 接口组合矩阵与缺口

#### 1.5.1 矩阵

含 `Reader` 的组合空间为 `2 × 2 × 2 = 8` 格（Closer / Watcher / ValueWatcher 各有或无）：

| 组合 | 是否命名 | 实际存在 |
|---|---|---|
| `Reader` | ✅ | `env`、`fs` |
| `Reader+Closer` | ✅ `ReadCloser` | — |
| `Reader+Watcher` | ✅ `ReadWatcher` | — |
| `Reader+ValueWatcher` | ❌ | 多数 provider |
| `Reader+Closer+Watcher` | ❌ | — |
| `Reader+Closer+ValueWatcher` | ❌ | **`FallbackReader` 自身形态** |
| `Reader+Watcher+ValueWatcher` | ❌ | — |
| 三者全有 | ❌ | — |

8 格只命名了 2 格。`FallbackReader` 落在第 6 格，因此 `fallback.go` 的编译期断言只能拆成三行核心声明（`Reader` / `Closer` / `ValueWatcher`）+ 一行冗余的 `ReadCloser`——后者已由 `Reader`+`Closer` 保证，写它纯为声明意图。

#### 1.5.2 缺口的真实代价

**先明确：代价不是「不能用」。** Go 的接口是隐式满足 + 结构化类型，组合接口名只是**类型别名**，零运行时成本、零语义贡献。`FallbackReader` 该有的能力一个不少。

实际影响只有三处，都不严重：

1. **声明啰嗦**：本可一行 `_ ReadCloserValueWatcher = (*FallbackReader)(nil)`，现在写四行。
2. **形参无法一个词表达**：需要「可关闭 + 能推值监听的源」时，签名只能内联 `interface{ Reader; Closer; ValueWatcher }`。
3. **契约不可读**：读者无法从接口清单看出「成熟 provider 应有的形态」，只能从断言反推。

**判断标准：只有当某个函数/字段的形参需要它时才命名组合。** 当前调用点是 `NewFallbackReader(readers ...Reader)` —— 入参是最窄的 `Reader`，内部靠类型断言升级能力。按此标准，`ReadCloserValueWatcher` **现在不需要存在**，缺口是良性的。

（Go 生态先例支持「按需命名」：`io` 包命名了 `ReadCloser`/`ReadWriteCloser`/`ReadSeekCloser`/`ReadWriteSeekCloser` 等一大串，缺失的组合就地用匿名接口。补名不违背惯例，但也非必须。）

#### 1.5.3 比命名缺口更值得关注：signal 源被静默丢弃，以及落地方式

只支持信号通知的后端无法直接被组合层监听——由 **provider 内部自行回读**（收到信号后调一次 `Load`，再以 `ValueWatcher` 形式暴露）。能力转换下沉到后端，框架只认一种契约。

最终落地即方案：无需任何代码逻辑改动，仅修正以下几处注释：

- `bconfig.go` 的 `Watcher`：声明能力边界——组合层只合并 `ValueWatcher`，不识别本接口；并给出指引「信号型后端请在 provider 内部 Load 回读后转推值」。
- `bconfig.go` 的 `ReadWatcher`：补一句「组合层不识别本组合，仅作类型便利」。
- `bconfig.go` 的 `ValueWatcher`：把「仅支持信号模式的通知者实现 `Watcher` 即可」改为「本接口才是组合层唯一识别的监听契约」。
- `fallback.go` 的 `WatchValue`：补一句「只合并 `ValueWatcher`，不引入适配器去兼容信号源，转换责任下沉到 provider」。

这同样贴合 §1.4.1 的结论：既然信号与推值最终都要「重新 `Load` 才敢用」，就不在框架层为统一两种模式引入额外抽象，转换交给 provider 自己完成。

#### 1.5.4 抗组合爆炸的真正手段：组合器嵌套

`_ Reader = (*FallbackReader)(nil)` 这条断言意味着：

**`FallbackReader` 自己也是 `Reader`，因此可以被塞进另一个 `FallbackReader`。**

```go
// 远程组：etcd 兜底 consul
remote, _ := NewFallbackReader(etcdSrc, consulSrc)
// 再与本地文件组成最终链：本地 > 远程组
all, _ := NewFallbackReader(fileSrc, remote)
```

这才是「组合矩阵爆炸」的标准答案：**不为每种组合命名，而是让组合器自己实现接口，用嵌套表达任意组合。**


### 1.6 后端实现矩阵

| 能力组合 | provider |
|---|---|
| `Load` + `WatchValue` | etcd / consul / nacos / apollo / kubernetes / vault（轮询） |
| `Load` + `WatchValue` + `Close` | file、http、etcd（自建 client） |
| 仅 `Load` | env、fs |

远程 provider（etcd/consul/nacos/apollo/kubernetes/vault，2026-09-05 补齐）统一 **双模式构造**：`New(opts...)` 从连接参数自建 client（契约装配路径，惰性或启动即连），`NewWithClient(c, opts...)` 注入已有 client（复用注册发现等场景的既有连接，本源不负责关闭）。watch 语义：etcd 原生 watch / consul watch plan / nacos ListenConfig / apollo 变更事件 / kubernetes ConfigMap watch 为**推送**；vault 无推送能力，以轮询模拟（与 http 的 ETag 轮询同型）。apollo 相比 go-wind 原版修正了两点：`New` 连接失败返回 error 而非 panic；watcher 修复了注册后立即反注册的 bug。

> 契约瘦身记录（2026-09-05）：`bootstrapv1.Config` 砍掉无 provider 的死源 fs/redis/zookeeper/oss/polaris（字段号 reserved 防复用）。fs 源的 `fsys` 是编译期资源无法经契约表达（`bconfig/fs` 走代码 API）；其余按需实现时以新字段号追加。vault 同日实现（KV v1/v2 + 轮询 watch），恢复字段号 10（原契约从未发布，无历史数据风险），并砍掉原契约中无消费者的 `mount_path` 字段。**契约字段须全有消费者**：nacos 的 namespace/server_addrs、kubernetes 的 config_map_name/key（WithConfigMapName/WithDataKey 回填空 key 装配默认值）均在此原则下补齐实现。

---

## 2. 配置契约

配置契约层（`bald/bconf`）是**配置形状的单一真相源**：用 Protobuf 声明「一个应用长什么样」，`buf` 把它生成强类型的 Go 包，上层（配置源 `bconfig`、配置初始化层）只消费这份契约，不各自定义结构。

### 2.1 模块与生成物

```text
bald/bconf/                       module github.com/kalandramo/bald/bconf
├── go.mod / go.sum               仅依赖 google.golang.org/protobuf
├── buf.yaml                      buf v2 模块定义（path: proto）
├── buf.gen.yaml                  managed mode：go_package_prefix 决定生成路径
├── proto/bootstrap/v1/           框架级契约（15 个，package bootstrap.v1）
│   ├── bootstrap.proto           顶层 BootstrapConfig
│   ├── config.proto / server.proto / registry.proto / database.proto
│   └── ...（app / cache / log / metrics / tracer / ai / broker / storage / script / workflow 共 15 个）
├── proto/bald/                   域级契约
│   ├── store/v1/store.proto      分页/过滤/排序契约（pkg/store 消费）
│   └── appspec/v1/appspec.proto  应用规格契约（代码生成 appspec_gen.go 消费）
└── gen/go/                       *.pb.go 生成物（包名 bootstrapv1 / storev1 / appspecv1）
```

- **Go 导入路径**：`github.com/kalandramo/bald/bconf/gen/go/...`。
- **生成方式**：`buf` managed mode（`buf.gen.yaml` 的 `go_package_prefix` 是生成路径的最终裁决者）。各 `.proto` 的 `option go_package` 已于 2026-09-05 与之全量对齐（历史上 store/appspec 两文件曾残留旧 `pkg/conf/gen` 串、靠 managed mode 兜底——已修正，不再依赖兜底）。
- **改 proto 后**：跑 `buf generate` 重新生成 `.pb.go`——**不要直接手改 `.pb.go`**，`rawDesc` 内嵌描述符带 protobuf 长度前缀，改长字符串会让长度错位、运行时 panic。（protoc-gen-go 生成 rawDesc 时会剥离 `go_package`，故修正源串不影响生成产物。）

### 2.2 为什么用 proto 作契约（而非 Go struct + mapstructure）

> 本节为 2026-09-05 从《proto 配置契约设计.md》（已删除）收编的决策记录，动因与坑位均经实证。

**起点问题：Go struct + mapstructure 两个真实缺陷**

早期版本曾用 Go struct + mapstructure 反序列化配置（viper 时代），两个缺陷无法根治：

1. **flag 压不过配置文件**：`viper.BindPFlag` 绑定的 flag 若未显式传入，`pflag.Value` 的零值（如 `""`、`false`）会覆盖配置文件里已设置的值——除非逐字段判断 `Changed`，易漏。
2. **嵌套段静默丢失**：配置文件里写错的键名（如 `https` 而非 `http`）或未导出字段的嵌套段（如 `http.tls`），mapstructure 直接忽略——**TLS 配置静默失效**，无任何告警。

写错键名静默落到零值，是配置系统最危险的故障模式：进程照常启动，行为却不对。

**框架横向比对**

| 方案 | 配置形状定义 | 取舍 |
|---|---|---|
| go-lulu | Go struct + yaml tag | 键名错误同样静默；无默认值/校验的单一来源 |
| go-wind | proto 契约（conf module）+ 各 provider 自注册 | **契约先行**：形状、默认值、校验注解一处声明——方向一致 |
| onexstack | `IOptions` 接口 + 手写 NewXxxOptions | 每加一个配置项要改 3-4 处（interface/struct/flags/New），样板爆炸 |
| **bald** | **proto 契约（bootstrapv1）+ buf 生成** | 强类型 + 默认值 + validate 注解 + flag 绑定全走同一份描述符 |

**proto 契约换来的能力**

- **编译期可查**：字段名/类型是生成代码的强类型字段，IDE 跳转、重构、拼写检查全覆盖；
- **默认值单一来源**：`bconf.NewBootstrap()` 从 proto 默认值体系填充，不存在 struct 零值与「文档里的默认值」漂移；
- **校验同源**：`buf.validate` 注解写在契约上，`bconf.Validate` 统一执行（见《校验设计.md》）；
- **flag 绑定零样板**：`bconf.BindFlags` 按描述符自动生成 `--http.addr` 这类层级 flag，新增配置项零额外代码。

**代价（诚实记录）**

| 代价 | 缓解 |
|---|---|
| 需要 protoc/buf 工具链 | buf 一条命令；生成物入库，业务方无需装工具 |
| proto3 标量无 presence（三态语义受限） | 关键字段用 `optional`（见 §2.4 坑 3） |
| 业务自定义配置段不受契约约束 | `DiscardUnknown` 放行，业务段自行解析（proto 只约束框架级配置） |
| map→proto 的类型边界需要适配层 | `bconf.UnmarshalMap` 内建类型规范化（见 §2.4） |

### 2.3 契约形状

顶层消息 `BootstrapConfig` 声明式描述一个应用的完整拓扑（14 个顶层域），字段用 `optional` 支持「同时配置多种传输/配置源」的混合模式：

| 字段 | 含义 |
|---|---|
| `app` | 应用元数据（含 `stop_timeout`，全契约唯一 `google.protobuf.Duration` 字段） |
| `server` | 传输层（HTTP/gRPC/…可同时配置多种） |
| `config` | 配置中心来源（file / etcd / nacos / consul / apollo / kubernetes / vault / http / env 可混合） |
| `registry` | 服务注册发现 |
| `logger` | 日志系统 |
| `tracer` / `metrics` | 链路追踪 / 指标 |
| `broker` / `storage` / `cache` / `ai` / `workflow` / `script` / `database` | 其余能力域 |

其中 `Config` 消息（对应 1.6 后端矩阵）把每种配置源声明为独立内嵌 message（`File` / `Etcd` / `Nacos` / `Consul` / `Apollo` / `Kubernetes` / `Vault` / `Http` / `Env`），多个源可在同一 `Config` 中并存，由配置初始化层解析为命名层列表（§3.7）；fs/redis/zookeeper/oss/polaris 字段号已 reserved（§1.6 契约瘦身记录）。

**契约设计细节（收编自《proto 配置契约设计.md》）**：

1. **TLS 用显式子消息**（`server.proto` 的 `Server.http.tls` → `message TLS`）：启用 TLS 是三态语义（未配置/禁用/启用+参数），标量 bool 表达不了；显式子消息天然有 presence——`GetTls() == nil` 即未配置。坑位实证（2026-09-05）：`BindFlags` 走契约前必须 `m.Has(fd)` 判存在再 `Mutable`，否则 nil 子消息被实例化 → TLS 误启用 → e2e 全挂。
2. **Duration 用 `google.protobuf.Duration`**（现仅 `app.stop_timeout` 一处）：拿到类型安全与 protojson 标准序列化，代价是序列化形态必须遵守 protojson 约定（见 §2.4 坑 1）。若新配置项无跨语言/跨域需求，秒数 int 字段反而更省心。
3. **repeated 字段整体替换语义**：配置中出现列表时覆盖默认值而非追加（`UnmarshalMap` 保证，见 §2.4 坑 2）——契约层面无需为「覆盖 vs 追加」引入任何标记。

### 2.4 桥接：`bconf.UnmarshalMap` 的合并语义与三个坑

`UnmarshalMap(m map[string]any, msg proto.Message)`（`bconf/unmarshal.go`）是配置树（map 形态）与 proto 契约之间的唯一桥接点，加载器（`bootstrap/config` Store、测试桩）只需先合并出 map。**语义是「合并」而非「替换」**：先解析到同类型空消息再 `proto.Merge` 进 msg，未被配置覆盖的字段保留默认值（通常是 `bconf.NewBootstrap()` 填入的）。

流程：`map → coerce 类型规范化 → json.Marshal → protojson.Unmarshal（DiscardUnknown）→ clearPresentLists → proto.Merge`。

三个坑（实现内建防御，业务侧须知其存在）：

1. **Duration 格式化**：protojson 的 Duration 只接受十进制秒 + `"s"` 结尾（`"90s"`）。不能用 `time.Duration.String()`——它产生 `"1m30s"` 复合表示会解析失败。flag 传入的 `time.Duration` 须经 `strconv.FormatFloat(d.Seconds(),'f',-1,64)+"s"` 格式化（`coerceDuration` 已内建）。
2. **proto.Merge 对 repeated 是 append**：默认值 `["stdout"]` 与配置值 `["stdout"]` 会合并成 `["stdout","stdout"]`（日志写两遍的真实案例）。`clearPresentLists` 在合并前清掉「配置中显式出现的」list 字段的旧值，使其表现为替换；配置未出现的 list 保留默认值。
3. **proto3 标量无 presence**：无法区分「未配置」与「显式配置为零值」——若字段默认值非零（如 `http.addr` 默认 `:8080`），配置里显式写空串**不会**覆盖它。需要三态语义时在 proto 里用 `optional` 启用 field presence（或如 TLS 用显式子消息）。

另注：`coerceMessage` 会把 env/flag 来的字符串与整数规范化成 protojson 严格模式接受的类型（`"8080"`→int32、纳秒→`"10s"`、枚举名/编号、snake/kebab-case 键名归一），这是 map 侧与 proto 侧的类型缓冲层——新加配置类型支持时改这里，不要在加载器里散落特判。

### 2.5 三层定位

```text
bconf（契约）  ← 谁都不依赖，声明形状
   ↑              ↑
bconfig（源）  bootstrap（初始化）
（读取能力）   （读契约 → 建 provider → 注册/装配）
```

`bconf` 只定义「有什么配置」，不关心「怎么读」「怎么装配」。这与 1.x 的 `bconfig`（能力轴 + 9 个 provider 子包）形成契约/实现分离：契约是稳定的接口，provider 可独立演进。


## 3. 配置初始化

配置初始化层负责把**契约**（`bconf` 的 `*v1.BootstrapConfig`）翻译成**可运行的配置源**（`bconfig` 的 `Reader`），并按优先级装配成 `FallbackReader`。它是「读契约 → 建源 → 注册/装配」的桥梁。

本层的核心设计原则：**不用 `init()` + blank import 的隐式全局副作用**，使用显式 `Registry` + 工厂函数 `Provider`。

### 3.1 定位

| 层 | 包（module） | 职责 | 依赖方向 |
|---|---|---|---|
| 契约 | `bconf` | 声明「应用长什么样」（proto 生成） | 谁都不依赖 |
| 源 | `bconfig` + 子包 | 单后端的读取能力（`Reader`/`Watcher`/…） | 只依赖抽象层，不依赖契约 |
| **初始化** | **`bootstrap`** | 读契约 → 建 provider → 按注册序装配成 `FallbackReader` | 依赖 `bconf`（契约）+ `bconfig/*`（源） |

依赖方向反转保持干净：`bootstrap → bconf`、`bootstrap → bconfig/*`，而 `bconfig/* → 零契约依赖`。源层（file/env/…）保持「只读字节的纯实现」，不认识 protobuf 契约——契约字段到 provider `Option` 的翻译职责全部收口在 `bootstrap`。

### 3.2 为什么不用 blank import + init()

每个 provider 一个薄 module，在其 `init()` 里调 `bootstrap.MustRegisterConfigAction(ConfigTypeXxx, newAction)`；主程序靠 `_ "bootstrap/config/apollo"` 触发 `init()`，把 apollo 装配进来。

这种「全局 map + init 副作用」模式有三个真实代价，与 bald 既有哲学冲突：

1. **依赖不透明**：`import _ "x/apollo"` 读代码看不出 apollo 被装配了，IDE/重构工具也跳不到、追不到。
2. **装配顺序失控**：多个 provider 的 `init()` 执行序由 Go 导入图决定，无法表达「先 file 后 etcd」这种级联优先级（而级联优先级恰恰是 `FallbackReader` 的语义核心）。
3. **与 appkit 体系冲突**：bald 已有 `Provides/Requires/Resolve`（capability.go 的显式能力声明）、`Registry.Mount/Unmount`（mount.go 的显式运行期装配）、`Component` 五阶段生命周期（component.go 的显式生命周期）——全是**显式装配**哲学。再造一个全局 `map[string]ConfigAction` + `init()`，是在显式体系里塞一个隐式全局态的异类。

取舍判定：便利（少写几行 import）换不来确定性的依赖图与可重构性，不划算。

### 3.3 模块布局

```text
bald/bootstrap/                  module github.com/kalandramo/bald/bootstrap
├── go.mod                       依赖 bconf（契约）+ bconfig（源）+ 各 provider 子包
├── registry.go                  Registry + Provider 类型 + Register/MustRegister
├── build.go                     Build：按注册序装配成 FallbackReader（逆序回滚）
├── env.go                       EnvProvider：契约 Env 字段 → bconfig/env 的 Option
├── file.go                      FileProvider：契约 File 字段 → bconfig/file 的 Option
├── registry_test.go / providers_test.go
└── ...（etcd.go / nacos.go / consul.go / ... 按需补）
```

> 状态：**已落地**（2026-09-05）。env / file 两个适配器已实现并通过全部测试（Registry 机制 9 项 + 真适配器与端到端装配 5 项）；其余后端按需追加。

### 3.4 核心类型：`Provider` 与 `Registry`

```go
// Provider 是配置源工厂：从契约顶层结构读一个子配置，
// 构造并返回一个 bconfig.Reader。返回值为 (reader, cleanup, error)：
//   - reader 为 nil 表示「该源未在契约中配置」，Build 阶段跳过（非错误）；
//   - cleanup 为释放 provider 资源的函数（可 nil，如 env 无资源）；
//   - 出错返回 error，Build 短路并回滚已构造的源。
type Provider func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (bconfig.Reader, func(), error)

type Registry struct {
    mu        sync.RWMutex
    providers map[string]Provider // key = "file" / "env" / "etcd" ...
    order     []string            // 注册序 = 默认级联优先级（高优先级在前）
}

func NewRegistry() *Registry { ... }

// Register 注册一个配置源工厂。重名报错（不覆盖，fail-fast）。
func (r *Registry) Register(name string, p Provider) error { ... }

// MustRegister 是 Register 的 panic 版本，仅用于主程序 main() 内显式注册，
func (r *Registry) MustRegister(name string, p Provider) { ... }
```

与 `ConfigAction`（返回无参 `func()` 清理闭包）的关键差异：

- 本设计的 `Provider` 返回可见的 `bconfig.Reader`，`Build` 把它喂给 `FallbackReader`；每次调用构造一个新实例（多实例可行）；`cleanup` 语义清晰（file 的 `cleanup` 即 `src.Close()`），与 `bconfig.Closer` 对齐。

### 3.5 适配器：契约字段 → provider Option（放 bootstrap 内部）

适配器示例（file）：

```go
// bald/bootstrap/file.go
package bootstrap

import (
    "fmt"

    bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
    "github.com/kalandramo/bald/bconfig"
    "github.com/kalandramo/bald/bconfig/file"
)

// FileProvider 是 file 配置源的初始化器。
// 它知道「契约里 Config.GetFile() 返回什么字段」+「file.New 接受什么 Option」，
// 因此 bconfig/file 包无需 import bconf，保持源层零契约依赖。
// 契约 File.format 字段（json/yaml/toml）属于 Decoder 职责（源层只吐原始字节，见 §1.2.1），此处忽略。
func FileProvider() Provider {
    return func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (bconfig.Reader, func(), error) {
        c := cfg.GetConfig().GetFile()   // 源配置在顶层 config 字段下
        if c == nil {
            return nil, nil, nil // 未配置 file 源，跳过
        }
        var opts []file.Option
        if ctx != nil {
            opts = append(opts, file.WithContext(ctx)) // watch 协程跟随装配方生命周期
        }
        if p := c.GetPath(); p != "" {
            opts = append(opts, file.WithPath(p))
        }
        if c.GetWatch() {
            opts = append(opts, file.WithWatch(true))
        }
        src, err := file.New(opts...)
        if err != nil {
            return nil, nil, fmt.Errorf("bootstrap: build file source: %w", err)
        }
        return src, func() { _ = src.Close() }, nil
    }
}
```

env 适配器同理：

```go
// bald/bootstrap/env.go
func EnvProvider() Provider {
    return func(_ context.Context, cfg *bootstrapv1.BootstrapConfig) (bconfig.Reader, func(), error) {
        c := cfg.GetConfig().GetEnv()    // 源配置在顶层 config 字段下
        if c == nil {
            return nil, nil, nil
        }
        var opts []env.Option
        if p := c.GetPrefix(); p != "" {
            opts = append(opts, env.WithPrefix(p))
        }
        if k := c.GetKey(); k != "" {
            opts = append(opts, env.WithKey(k))
        }
        src, err := env.New(opts...)
        if err != nil {
            return nil, nil, fmt.Errorf("bootstrap: build env source: %w", err)
        }
        return src, nil, nil // env 无资源需释放
    }
}
```

**为什么适配器不放 provider 子包**：若放进 `bconfig/file`，`file` 包就要 `import bconf`，「只读文件系统的配置源」被迫依赖 protobuf 契约——分层崩塌。翻译职责归 `bootstrap`（它本就是「读契约→建源」层），依赖方向保持 `bootstrap → bconf` / `bootstrap → bconfig/*`，源层纯净。

### 3.6 层优先级 = 注册序

`order` 切片即层优先级：**先注册的源优先级高**（排在 `[]config.Layer` 列表首位）。这与 `config.Store` 的层合并约定一致——层列表从尾向头叠加（低→高），列表首元素最后合并、覆盖其余层。

选注册序而非给 `Provider` 加 `Priority int` 字段的理由：注册序已能表达全部场景（主程序按想要的顺序 `MustRegister` 即可），引入 `Priority` 字段只会在「同优先级又需排第二序」时再加一层规则，徒增复杂度。

> 语义归一说明（2026-09-05）：装配产出曾是 `bconfig.FallbackReader`（key 级 first-wins 回退），现已归一为**命名层列表**。回退语义做不到字段级合并（本地只覆盖部分字段时其余字段无法从远程基准继承），且热更新回读会被高优先级源永久屏蔽（远程变更静默失效）。`FallbackReader` 退守它擅长的域——同一 key 的多候选降级（见 §1.5.4 嵌套示例），不再出现在装配主链路上。

### 3.7 装配：Build 出命名层列表

```go
// bald/bootstrap/build.go
type Provider func(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) (*config.Layer, func(), error)

func (r *Registry) Build(ctx context.Context, cfg *bootstrapv1.BootstrapConfig) ([]config.Layer, func(), error) {
    if cfg == nil {
        return nil, nil, errors.New("bootstrap: config is nil")
    }
    names, providers := r.snapshot() // 快照，Build 不长时间持锁

    var (
        layers  []config.Layer
        closers []func()
    )
    for _, name := range names {         // 注册序 = 层优先级（首元素最高）
        l, closer, err := providers[name](ctx, cfg)
        if err != nil {
            runClosers(closers)          // 失败回滚已构造的源
            return nil, nil, fmt.Errorf("bootstrap: provider %s: %w", name, err)
        }
        if l == nil {
            continue // 该源未在契约中配置，跳过
        }
        if l.Reader == nil {             // 层 Reader 必填（fail-fast）
            runClosers(closers)
            return nil, nil, fmt.Errorf("bootstrap: provider %s: layer Reader is nil", name)
        }
        if l.Name == "" {
            l.Name = name                // Build 回填注册名（层名用于日志/错误定位）
        }
        layers = append(layers, *l)
        if closer != nil {
            closers = append(closers, closer)
        }
    }
    if len(layers) == 0 {
        return nil, nil, errors.New("bootstrap: no config source configured")
    }
    return layers, func() { runClosers(closers) }, nil
}
```

层职责划分：provider 填 `Reader`（整文档源，`Load(ctx, "")` 返回整份文档）、`Format`（契约 format 字段或扩展名推断）、`Watch`（契约 watch 字段，reader 须实现 `bconfig.ValueWatcher`，Build 期校验 fail-fast）；`Name` 由 Build 回填注册名。

### 3.8 主程序用法（无 blank import）

```go
func main() {
    reg := bootstrap.NewRegistry()
    // 显式注册：读代码即知装了什么；注册序即层优先级（先注册者覆盖后注册者）
    reg.MustRegister("env", bootstrap.EnvProvider())
    reg.MustRegister("file", bootstrap.FileProvider())
    // reg.MustRegister("nacos", bootstrap.NacosProvider()) // 按需追加

    var bootCfg bootstrapv1.BootstrapConfig
    // ... 从引导配置装载 bootCfg ...

    layers, cleanup, err := reg.Build(ctx, &bootCfg)
    if err != nil { log.Fatal(err) }
    defer cleanup() // 逆序释放各 provider 资源

    // 层接入 Store：整体位于本地文件/env/flag 之下、RemoteConfig 远程桥之上
    app := appkit.New(name,
        appkit.ConfigLayers(layers...),
        appkit.WatchConfigFile(true),
        appkit.OnConfigChange(func(m map[string]any) { /* 重新 Unmarshal */ }),
    )
    // ...
}
```

Store 侧的完整优先级链（高 → 低）：`flag > env > 本地文件 > 契约源层（列表首最高）> 远程桥（基准）`。任一层实现 `bconfig.ValueWatcher` 即参与热更新：变更 → decode → 层缓存更新 → 全量重合并 → `OnConfigChange`；层 reader 资源归 `Build` 返回的 cleanup 释放，`Store.Close` 只停自己的转发 goroutine（构造方职责分离，不重复关闭）。

依赖图全程可见：谁被装配、以什么优先级叠加、失败时如何回滚——都写在 `main()` 里，无需追踪 `init()` 副作用。

> 注：本层暂无「启动期 fail-fast 能力校验」——若需要，可复用 `appkit` 的 `Provides/Requires/Resolve`（capability.go）在 `Build` 阶段断言「契约要求的能力都有对应 provider 注册」。待与 appkit 集成时再补。
