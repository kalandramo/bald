# bald 架构风险 / 质量评审

> 产出方式：`risk-quality-reviewer`，交叉引用 `system-modeler`（`current-architecture.md`）与 `architecture-health`（本文对制品自身的校验）。
> 证据日期：2026-11-01，`main` @ `ca981b1`，工作树干净。
> 核对方式：文中每条结论要么由我本人读源码/执行 grep 得到，要么由脚本从制品中抽出 `路径:行号` 后逐条打开源文件比对。凡未做到这一点的，明确标 ⚠ 并写清依据。
> **本评审登记时不改动任何代码**。状态列标 **已修** 的条目由后续修复轮次落地，每条带「修法 / 验活 / 未修」三段；行号会随这些改动漂移，引用以符号名为准复核。

## 严重度概览

| # | 风险 | 严重度 | 置信度 | 状态 |
|---|---|---|---|---|
| 6.5 | gRPC 的 `tls` 契约字段**未接线**，生成物硬编码明文；gateway 出站也硬编码明文 | **高** | ✅ 已核对源码 | **已修** |
| 3.1 | 根 module ↔ 适配器 **模块环**，靠互相 `replace` 才编译 | **高** | ✅ 已核对 go.mod/import | **已修**（删重复机制后实测 89 模块为 DAG；22 个适配器单向 require 根的扇入耦合仍在） |
| 6.1 | 鉴权 **fail-open**：未注入即整层消失，无 default-deny、无告警 | **高** | ✅ 已核对源码 | **已修** |
| 2 | **发布面 ≫ 使用面**：retry / circuitbreaker 等无消费方 | 中 | ✅ 全仓 grep | **部分收口**（Discovery 接线半已落地＝§2「选项 1」的 Discovery 分支；retry/circuitbreaker 仍 reference-only；移出发布批次未做） |
| 7.1 | **CI 全量只跑 `-short`**，集成测试在 CI 中从不执行 | 中 | ✅ 已核对 `.github/workflows/ci.yml` | **已修（registry/store/队列三条接缝）**：新增 `integration` 作业注入 etcd/redis/mongo，探活 + 逐条断言判据真的 PASS；示例侧（etcd/nacos 服务发现）仍只在本地跑，见 §7.1 收口 |
| 1.1 | race 作业是两处手工清单，webrtc/kafka 均缺失 | 中 | ✅ 已核对 `.github/workflows/ci.yml` / `Taskfile.yml` | **已修**（改成 find 派生 + 单份排除清单；扩范围实测揪出并修掉 graphql 自身的竞争） |
| 1.3 | kafka / rabbitmq 适配器 **零测试文件** | 中 | ✅ 实测计数 | **已修**（两个适配器各有一份测试，15 / 7 个测试；补测试当场揪出 4 个生产缺陷；订阅者摘除口径已定并由测试钉住） |
| 1.2 | 本地 `task verify` 手写清单，静默跳过约 40 模块 | 低-中 | ✅ 已核对 `Taskfile.yml` | **已修**（CI 与本地共用 `scripts/list-modules.sh` 单条派生；全量 92 模块实测 exit=0） |
| 附 | `tokenbucket.Close()` 不唤醒 `Wait`，与 gin 侧注释承诺相反 | 中 | ✅ 读源码 + 变异验证 | **已修**（`Close` 内 `close(notify)` + 幂等；判据 `TestClose_WakesBlockedWait`） |
| 5 | `berrors` 分类法未在 transport/broker/appkit 落地 | 中 | ✅ 实测计数 | **口径已定**（选项 b：定位为边界投影模型；不改码，兜底不对称与硬规则见 §5） |
| 6.2 | gRPC 链无限流、无超时（与 gin 链不对称） | 中 | ✅ 已核对源码 | **已修**（unary 面；契约段、stream、自动装配三项未修，见 §6.2） |
| 6.3 | 限流位于认证之后，认证层自身不受保护 | 中 | ✅ 已核对注释与实现 | **口径已定**（保持现位置 + 落文档；不改默认的理由与自保姿势见 §6.3） |
| 3.2 | 21 个 module 同时 require 同一库的两个 major（`// indirect`） | 低 | ✅ 脚本实测 | **已修（只读面）**：`scripts/multi-major.sh` + `task deps-multi-major`；`go list -deps` 实测确认真正同链的是 2 条，见 §3.2 |
| 4.1 | 停机期 `_ = deregister(...)` 吞错 | 低-中 | ✅ 已核对源码 | **已修** |
| 4.2 | 端点上界 5s、hook 超时 5s 为常量（Stop 超时本身可配） | 低 | ✅ 已核对源码 | **已修** |
| 8 | `docscheck` 只校验链接完整性，测不出描述过期 | 低 | ✅ 已核对测试 | **已修（清单半）**：`module-list` 围栏 + 双向断言 + 与脚本逐行比对，三条变异实测；「描述已删除 API」半仍未覆盖，见 §8 |

---

## 1. 模块面 vs 构建/验证面

**1.1 race 作业是手写清单，且两处重复** ✅ 已修

原状：`.github/workflows/ci.yml` 的 race 作业手工枚举 23 个模块，`Taskfile.yml`
的 `race` 镜像同一份清单（两处必须同步改）。`transport/webrtc`（15 个非测试文件
+ 8 个测试文件，含 `signalSessions` 共享会话表与 PeerConnection 生命周期，正是
race 该覆盖的形态）、`broker/kafka`、`contrib/*` 全部缺席，而 `metrics` 是零测试
文件的死条目。

- 修法：两处都改成**从仓库派生**——复用 test/lint/vuln 作业那条
  `find . -name go.mod -not -path "./.git/*" -not -path "./_example*" | xargs -n1 dirname | sort`，
  再减去 `.github/race-skip.txt`（单份排除清单，每条必须自带理由）。默认值从
  「不跑」变成「跑」：新增 module 两处都不用改。Taskfile 侧用 `vars.MODS` 的 `sh:`
  + `for:` 派生，逐模块仍走内部 `race-module` 的 task 级 `dir:`（go-task 3.52 会
  静默忽略「逐命令 dir」和「for 循环内的 dir」），并加 `platforms: [linux, darwin]`
  ——派生要 POSIX shell，Windows 下整个任务跳过，反正 `-race` 本身要 cgo、
  `.github/workflows/windows.yml` 也从不跑 race。
- 验活：按 CI 谓词共 **92 个 module**（89 个库模块 + `cobramcp/examples/` 下 3 个
  示例模块），本机逐模块 `go test -short -race` 全量一遍 = 90 通过 / 2 失败，
  串行合计 351s（race 作业原先 23 个模块在 CI 上 4.5 分钟，扩到全量的代价可接受）。
- 扩范围立刻抓到的第一个真 bug 在 `transport/graphql`（此前从未被 race 覆盖）：
  `Start()` 在调用方 goroutine 里写 `s.listener` / `s.server`（`transport/graphql/server.go:116-119`），
  `Stop()`（`:141-148`）与 `Endpoint()`（`:152-163`）却无同步地读。接进装配面就命中：
  `Start` 跑在 `pkg/appkit/appkit.go:627` 的 `g.Go` 里，端点等待（`:634` → `waitForEndpoints` 轮询 `s.Endpoint()`，`:829`）
  在同一个 goroutine 上并发读；停机阶段每个 server 的 `Stop` 也各自起 goroutine
  （`pkg/appkit/appkit.go:736`）。改法：`mu sync.RWMutex`
  只保护这两个运行期字段（`:63`），`Serve`/`Shutdown` 改用局部 `hs`；测试里三处
  「固定 sleep + 直接读 `srv.listener`」换成轮询 `Endpoint()` 判就绪，中间件用例的
  共享变量 `headerSeen` 换成 channel 传递。
- 刻意没做：`log/aliyun` 的竞争在上游 `aliyun-log-go-sdk v0.1.131`（已核实是最新版，
  无升级可解）内部——`Start()` 起 `monitor.reportThread` goroutine，`Close()` 里
  `monitor.Stop()` 写同一字段，两者无同步。bald 的 `Close` 只是转调，要规避必须先
  关掉运行期指标上报（删能力），因此进排除清单并写明理由，而不是改代码掩盖。上游
  哪天修好，这条就该删。

**1.2 CI 的模块发现是健壮的，本地 `task verify` 不是** ✅ 已修

原状：CI 用 `find . -name go.mod ... | xargs -n1 dirname | sort` 零维护地发现全部模块（**循环排除了 `./_example*`**），逐模块 `go mod download` + `go mod tidy -diff` 闸门 + build + vet + test + bench 编译。
但 `Taskfile.yml` 的 `test` 只跑根 module，`verify` 手写依赖清单只覆盖 bconf/cobramcp/retry/ratelimit/broker/circuitbreaker/encoding/example —— `contrib/*`、`registry/*`、`transport/http|grpc|web`、`oss/*`、`ai/*`、log/metrics 后端**不在任何本地任务里**。只跑 `task verify` 会静默跳过约 40 个模块。严重度低（CI 兜底），但作者的问题要等到 CI 才暴露。

- 修法：把那条 `find` 提为唯一派生点 `scripts/list-modules.sh`（空清单 = 报错退出，不静默返回空），CI 的 test/lint/vuln/race 四处内联循环全部改成 `sh scripts/list-modules.sh`；本地新增 `verify-module`（单模块 tidy -diff + build + vet + test -short）、`verify-modules`（按派生清单逐模块跑，与 CI test 同范围）、`verify-scope PREFIX=<dir>`（前缀子集）。原先手写清单的 `retry-verify`/`encoding-verify`/`ratelimit-verify`/`broker-verify`/`circuitbreaker-verify`/`cobramcp-example-verify` 改成委托 `verify-scope`，约 30 个手抄模块名和 5 个只为它们服务的内部任务一并删除。`task verify` 的 deps 换成 `verify-modules`。
- 踩过的两个坑（都属会静默变绿的类型，记下来防止再犯）：① `go build -o <dir> ./...` 只对 main 包合法，纯库模块会报 `no main packages to build`——改成先用 `go list -json ./...` 判有没有 `"Name": "main"` 再决定形态（不用 `-f '{{.Name}}'` 是因为花括号会被 go-task 当模板渲染）；② `platforms:` 写在任务上会让它在其它 OS **静默跳过**，所以派生守卫写成「逐命令 platforms + Windows 分支显式 `exit 1`」，宁可让 Windows 报错也不假绿。
- 验活：空清单与不存在的 `PREFIX` 都硬失败（`verify: 清单派生为空…` / `verify-scope: … 下未派生出任何 module`，非零退出）；`sh scripts/list-modules.sh` 的输出与替换前的内联管道逐字节相同（92 个模块）。
- 实测：`task verify-modules` 全量跑完 **exit=0**，覆盖 92 个 module（每模块一次 `go mod tidy -diff` 闸门，共 92 次），158 个测试包 `ok`，0 FAIL。这轮改动之前，同一条命令只能覆盖到手抄清单里的那十来个模块。
- 未修：`task test` 仍只跑根 module（刻意保留——它是快速回路，全量入口是 `verify`/`verify-modules`）；`verify` 里 `go mod download` 这一步没有独立任务（CI 有；本地由 `tidy -diff` 间接完成）。

**1.3 零测试的网络适配器** ✅ 已修（kafka / rabbitmq）

| 模块 | 非测试 .go | 测试 .go（修前 → 修后） |
|---|---|---|
| `broker/kafka` | 9 | **0 → 2**（15 个测试 + 32 个子用例） |
| `broker/rabbitmq` | 11 | **0 → 1**（7 个测试 + 6 个子用例） |
| `transport/subscribe` | 1 | **0**（单文件 13 行选项，未补） |
| `retry` | 1 | 1 |

修前：kafka 适配器核心文件 639 行，含 writer 重建重试（`broker/kafka/kafka.go:415-437`）与订阅退避逻辑（`broker/kafka/subscriber.go:316-339`）与致命错误判定（`:341-362`）——这些分支从未被执行过。CI 对它只做到「能编译、vet 干净、空测试通过」。

- 修法：不起真实 broker，用「本地端口 listen 后立刻 close」造确定 ECONNREFUSED，
  失败分支全部在 `-short` 下可跑；把不可测的形态先拆成可测的——判据抽成
  `shouldRetryFreshWriter`（`broker/kafka/kafka.go:356-359`，两条发布路径共用），
  退避时长抽成纯函数 `backoffDuration`（`broker/kafka/subscriber.go:316-331`，注入随机源
  才能钉住边界）。发布侧覆盖缓存 writer 摘除与重建、`retriesCount=0`、热更重连后再发布、
  单 writer 分支（`broker/kafka/kafka.go:447`）、编码失败短路；订阅侧覆盖退避边界、`isFatalError` 真值表、
  致命退出与取消退出、close 幂等、`Unsubscribe(true)` 摘除注册表（`broker/kafka/subscriber.go:91-101`）。
  rabbitmq 只补不联网也能钉住的那层：`Init` 的地址归一化、三个未连接入口报错不 panic、
  Connect 失败后 `Disconnect` 仍能收尾、`rabbitHeaderToMap` 对非字符串 header 丢成空串。
- 补测试当场揪出 4 个生产缺陷（`fix(broker/kafka)` 7545f8c），每个都按「把缺陷改回去、测试必须红」验活：
  1. `Writer.Close` 把容器置成 nil map（`broker/kafka/writer.go:96-107`），热更重连后第一次发布
     往 nil map 写元素直接 panic（`broker/kafka/kafka.go:399`、`:431`）；panic 落在 `b.Lock()`
     窗口内，写锁永久泄漏，随后的 `Disconnect` 卡在 `RLock`（`broker/kafka/kafka.go:271`）——
     现场表现是测试既不失败也不通过，而是挂到超时。
  2. 重建路径 `if err = writer.Close(); err != nil { break }` 用 Close 的返回值顶掉写失败本身；
     `retriesCount == 0` 时重试循环一次不跑，`Publish` 对一条从未写出的消息返回 nil。
  3. `closeLocked` 关掉 `done` 后又把字段置 nil（`broker/kafka/subscriber.go:109-129`），
     而两条 process 循环的 `select` 不加锁读它（`:166`、`:234`）——`-race` 直接命中
     （`Unsubscribe` 与订阅 goroutine 并发）。`closed` 标志本就挡得住二次 close，改写是多余的。
  4. 致命错误分支 `return` 前没有 close（`broker/kafka/subscriber.go:185-188`、`:242-245`），
     reader 与 `done` 泄漏，订阅者还留在 `b.subscribers` 里。
  另附一条只有 race 闸门扩到全量后才会暴露的：退避原先用包级
  `rand.New(rand.NewSource(..))` 自建源（非并发安全，每个订阅各自一个 goroutine），已换成
  `math/rand` 全局函数。
- 刻意没做：`transport/subscribe` 只有一个 13 行的选项文件，不值得单独立一份测试；
  `retriesCount` 没有对外 Option（`retriesCountKey` 只被读、从没被写），测试按包内白盒直接改字段，
  没有为它新增公开 API；rabbitmq 的连接/通道/重订阅面要真实 AMQP server 才谈得上覆盖，本次未做。
- **「致命退出后订阅者还留在 `b.subscribers`」的口径已定（不改码）**：摘除责任在**调用方**，
  订阅 goroutine 自己只释放资源、不动注册表——`Unsubscribe(true)` 与 `Disconnect` 是仅有的两条摘除路径。
  三条理由：① 注册表按 topic 键且 `Add` 是覆盖式，退出中的订阅者无从判断「表里这条还是不是我」，
  自摘会在调用方已为重订阅换上新订阅者时把新的误摘掉；② kafka / rabbitmq / redis pubsub / redis stream /
  rocketmq 五个 broker 形态一致，只改 kafka 等于让同一问题在不同后端有不同答案；③ 残留条目对清理无害，
  `Disconnect` → `Clear` → `Unsubscribe(false)`，而 close 已幂等。**代价如实写明**：
  `broker.SubscriberSyncMap` 的 `Topics()/Len()/Has()` 是公开 API，会把已死的订阅算进「当前订阅」上报；
  仓内无人读它，判存活要看返回的订阅者本身（kafka 的 `IsClosed()`，不在 `broker.Subscriber` 接口上）。
  这条口径由 `TestSubscriberExitKeepsRegistryEntry` 钉住（fatal 退出后条目仍在、同 topic 重订阅覆盖而非累积、
  带死条目 `Disconnect` 仍清空且不报错）；变异验证：在两处致命分支加自摘 → 恰好红在
  「致命退出不得自行摘除注册表条目」这一条，复原后 `go test -race ./broker/kafka/` ok。
  真要改成「自摘」是行为变更（公开 API 可见性变了），得把上面三条理由都翻过来才做。

---

## 2. 发布面 ≫ 使用面（已核实的未接线能力）✅

| 能力 | 模块数 | 消费方 | 证据 |
|---|---|---|---|
| `retry/` | 1 | **无** | 全仓 `grep kalandramo/bald/retry` 唯一命中是注释 `ratelimit/bbr/bbr.go:68` |
| `circuitbreaker/` | 1 + 4 后端 | 仅自己的后端 | `circuitbreaker/{hystrix,vegas,sres,sentinel}/*.go`；无 transport/middleware/appkit 引入 |
| `registry.Discovery` | 接口 + 4 实现 | **已接**（gateway 出站） | 评审初值「无外部调用者」成立；现非测试的 `GetService` 调用点新增在 `transport/gateway/discovery.go` 的 `discoveryBuilder.Build`（watch-based resolver），消费方是 `transport/gateway` 段驱动装配；etcd 与 nacos 两个后端已真机跑通注册→推送→拨通 |
| `transport/graphql`、`workflow/argo`、`ai/{openai,eino,langchaingo}`、`oss/{s3,minio}` | 7 | 仓内 0 | 全仓 grep 仅 `transport/mcp/options.go:14` 的注释提到 graphql；其余无命中。有 `*/contract` Provider 但无消费者或示例 |

**为什么这是风险而不是「可扩展性好」**：这些模块进发布 tag、进 vuln/lint CI、进 `CHANGELOG.md`，但只有构造函数级测试（或没有测试）在触碰它们，**会静默腐烂**。Discovery 尤其值得单列——它让「服务发现」在 README 与契约里存在，而评审当时实际服务间调用走配置地址直连（`transport/gateway/gateway_server.go` 用 `s.backend.GetAddr()` 拨号，地址来自契约段），使用者容易误判。**这一条的误判面已由下面的「接线半」消解**：同一地址现在可写 `discovery:///<服务名>` 并真的被解析。

消解路径（按成本排序，任选其一，不要默认「留着以后用」）：
1. 接进 `pkg/appkit/std` 的标准角色（retry/circuitbreaker 作为可选 bundle 层，Discovery 作为 gRPC resolver）；
2. 显式标注为 reference-only，移出主线发布批次，在 README 的 SPI 表里标「未接线」；
3. 归档删除。

**收口（选 (2) 的标注半，未动发布批次）**：owner 既有口径是「能力不删除——未接线 ≠ 没用，删之前要问」，所以本轮只做「不再假装」：

- 修法：`README.md` 仓库结构段下新增「未接线能力（reference-only）」一条，点名 retry / circuitbreaker 后端 / `registry.Discovery` / graphql / argo / ai/* / oss 并说明「照常发布、不删除、不假装已接线」；`docs/architecture/current-architecture.md` §6 标题改为「未接线面（reference-only）」，补上 `transport/graphql`+`workflow/argo`+`ai/*`+`oss/*` 这一组（评审初值只列了前三项）与**可复现判据**（`find` 非测试文件 + 排除自身目录的 grep），并把两处随 §6.2 漂移的行号引用换成符号名。
- 验活：README 的链接与 §6 锚点由 `internal/docscheck` 校验通过；表内每条能力重新 grep 一遍，消费方仍为 0（`retry` 唯一命中还是 `ratelimit/bbr/bbr.go:68` 的注释）。
- 刻意没做：① **移出主线发布批次**——「哪些模块进 tag」由 `internal/release/repo.go`（`isExampleDir` 是唯一的范围规则，且只排除 `_example`）决定，改它等于改变下游能拉到什么，属对外行为变更，要 owner 单独定；② 未接线 ≠ 下架，标注不改变任何包的公开性；③ 接线（选项 1）里 **retry/circuitbreaker 的 bundle 层没做**（要定「默认开还是 opt-in」的失败语义，属产品决策）；Discovery 分支已单独落地，见下。
- 还不能证明：仓外消费方（bald-admin 及其它下游）是否已在用这些包。本轮只核了 bald-admin 的 berrors 采用面，未核它对 retry/circuitbreaker/oss 的 import，所以「零消费方」的严格含义是**仓内零消费方**。

**接线半（选项 1 的 Discovery 分支）已落地**：owner 后续口径是「接线吧，用 bald-admin 作为消费者」，所以这里不是标注，是真接。

- 修法（六层；1–4 是 source-compatible 的新增，5–6 是 resolver 内部**行为变更**、非 API 变更）：
  1. **读取口**：`App.Registrar()`（`pkg/appkit/appkit.go:342`）。此前段 Provider 产物经 `SetRegistrar` 只落在 `App` 私有字段上，装配好的注册中心对外**没有读取口**——消费方要 Discovery 只能按配置重连一次。
  2. **地址判据**：`usesDiscoveryTarget`（`transport/gateway/discovery.go:33`）把「是否走发现」交给地址字符串自己表达，契约字段 `backend_grpc_addr` 仍是 `string`，不改类型、不加新段；判定与 `DiscoveryScheme` 同文件，避免规则两处漂移。契约注释同步写下两种形态（`bconf/proto/bootstrap/v1/server.proto` 的 `Gateway.backend_grpc_addr`，随 `task proto-contract-local` 重生成 `gen/go`，`task bconf-verify` 含生成物一致性检查，绿）。
  3. **优先级在拨号点裁决**：`GatewayServer.resolveDiscovery`（`transport/gateway/gateway_server.go:129`）＝**显式实例 > 惰性来源 > fail-fast**。新选项 `WithDiscoverySource`（`:60`）是「需要时才取」的闭包——只有 addr 真用了 `discovery://` 且没显式 `WithDiscovery` 才被调用。这层延迟不是装饰：`scope.App` 是活指针，注册中心可以在 server 段 `Build` 之后才被 `SetRegistrar` 换上（NewApp 路径把注册中心交给应用自持即如此），`Build` 时派生会把「那一刻没有」固化成错误；而「谁被真正用上」只能按地址判，不能按选项顺序猜。addr 用 `discovery://` 而来源缺失时 **Start 直接失败并报明确错误**，不再让 gRPC 回落到默认 dns resolver 去解析一个不存在的主机名。
  4. **段驱动装配**：`gatewaycontract.AppProvider` 改为保存构造参数（`register` + `opts`）而不是预绑工厂，`Build` 时按当次 `ApplyScope` 前置注入 `discoveryScopeOption`（`transport/gateway/contract/provider.go:84`、`contract/discovery.go:35,46`）；来源从 `App.Registrar()`（次选 `Decl.Registrar`）断言 `registry.Discovery`。**消费方因此只改一行配置。**
  5. **端点归一化（接线的实测缺陷，不是一处装饰）**：`grpcAddressOf`（`transport/gateway/discovery.go:133`）——resolver 原样把注册端点交给 gRPC 拨号，而 **bald 自己注册的端点是带 scheme 的**（`transport/grpc/grpc_server.go:143` 交 `grpc://host:port`，HTTP 面交 `http(s)://host:port`，同实例混排）。gRPC 只吃 `host:port`，带 scheme 直接 `too many colons in address`——**即按 §6 判据「已接线」写完前，真实注册形态一个都拨不通**（既有单测都用裸 `host:port` 造实例，恰好绕过了这条）。现在 `grpc://` 剥前缀、其它 scheme 丢弃、裸地址照用；只剩非 gRPC 面的实例整体出局（走「无实例」路径并报订阅错误，而不是给出必然拨不通的地址）。
  6. **首次快照失败不再交回 gRPC（真机跑出来的第二个缺陷）**：`Build`（`transport/gateway/discovery.go:57`）原来把 `GetService` 的错误当成构建失败返回。「后端还没注册」到底是空集还是错误，是各后端的自由——内存与 etcd 返空集，**nacos 直接报 `instance list is empty!`**。而 gRPC 对构建失败的 resolver **不会在近期内重新解析**（实测：客户端只解析一次，45s 窗口内一次都没有重建），所以返回错误等于放弃恢复：实例后来注册上了也接不住。nacos 那轮端到端因此稳定 503 到超时。现改为「`cc.ReportError` 上报原因 + 保留订阅」，与「解析成功但实例为 0」走同一条路径，由 `Watch` 的推送把实例带回来；仍然返回错误的只剩两种确定不可能的情况——服务名为空（配置写错）与 `Watch` 建不起来（没有推送通道，重建 resolver 是唯一恢复路径）。**这条是「网关先于后端启动」这一常见时序的前提**，而 nacos 正是 bald-admin 用的注册中心。
- 验活：`transport/gateway`（含 `contract`）与 `pkg/appkit` 全量 `-race` 绿；判据共 **23 条新测试**（+1 条改写）——网关侧 10 条（`discovery://` 无来源 fail-fast、来源返回 `(nil,nil)` fail-fast、来源报错使 Start 失败并 wrap 原错、惰性来源拨通、直连 addr 不调来源、显式实例胜过来源、带 scheme 端点归一化、全是非 gRPC 面时报「无实例」、按真实注册形态拨通进 READY、**首次快照报错后仍能被推送救回**），装配侧 7 条（真 `grpc.Server` + `inmemory.Registrar` 端到端进 READY、`Decl.Registrar` 路径、无注册中心 fail-fast、注册中心不实现 Discovery 时 fail-fast、直连不需注册中心、显式 `WithDiscovery` 不被要求装配注册中心、`HTTPProvider` 确实不派生），appkit 侧 2 条（`App.Registrar()` 读取口 + 「registry 段阶段 B 产物在 server 段 `Build` 时可见」的时序不变量），**端到端 4 条**：
  - 消费者闭环 `_example/bald/internal/bootstrap/discovery_e2e_test.go`（默认构建，进 `task verify` 清单）起真应用——`appkit.register` 把 `grpc://` 端点注册进 inmemory，配置里 `backend_grpc_addr: discovery:///bald-demo`，REST 打到转码面拿到 `hello, discovery`。
  - 生产注册中心 `_example/bald/internal/bootstrap/discovery_registry_e2e_test.go`（**opt-in**：`-short` 跳过、地址不可达即 `t.Skip`，所以 CI 的 `-short` 作业不会因此变红；文件头给了可复制的 etcd/nacos 起法与 `BALD_E2E_ETCD_ADDR`、`BALD_E2E_NACOS_ADDR` 覆盖）——etcd 一轮、nacos 一轮，加一条把「nacos 的服务名带 `.grpc` 后缀」这个命名事实单独钉住的判据（裸名解析到 0 个、`<名>.grpc` 解析到 1 个且端点仍是 `grpc://`）。两轮的注册都刻意把**无人监听的 `http://` 面排在 `grpc://` 前面**，因此「拨通了」同时证明过滤在生效。
- **真机侧的状态机**（etcd、nacos 各跑一遍，四条断言按序）：注册前 503 → 注册后 200 且拿到 `hello, registry`（greeting 带回本用例自带的后端，排除了打到注册中心里残留的同名实例；Watch 推送 + 端点过滤同时成立）→ 注销后**仍 200**（钉住「空快照不下发」这条既定语义，不是遗漏）→ 后端进程下线后 503。
- **21 处改坏点逐条变异验证**（fail-fast 三个分支、scheme 判据、惰性/显式优先级、resolver 未安装、`App`/`Decl` 两条取源、两个错误分支、scope 选项被丢、`HTTPProvider` 误接线、`App.Registrar()` 返回 nil、把阶段 B 循环移到 late 钩子之后、scheme 不剥、非 gRPC 面不丢、示例 e2e 依赖解析、**删空快照守卫**、**首次快照失败改回返回错误**）——每条都是「基线 pass / 变异后恰好对应判据转红」，时序那条精确红在 `seen == nil` 的断言上；真机三条红法分别是：不剥 scheme → etcd 轮 `dial tcp: address grpc://127.0.0.1:… : too many colons in address`；删空快照下发守卫 → etcd 轮阶段③ `bad resolver state`；快照报错返回给 gRPC → 单测红 `Build(discovery:///svc): gateway: discovery svc: instance list is empty!`（这一条原先只能靠 nacos 那轮 45s 超时暴露）。`golangci-lint` 三模块 0 命中、`gofmt` 干净、`go mod tidy` 无差异、`task verify`（92 模块 + example）exit=0。
- 刻意没做：① `gatewaycontract.HTTPProvider`（转码面接管 `server.http`）**不派生来源**——它的上游是本进程 gRPC 面自己的绑定地址，填 `discovery:///` 是配置错误，测试 `TestHTTPProviderDoesNotAutoDeriveDiscovery` 把这条钉住；② 裸工厂 `contract.Provider` 不自动派生（它没有 scope，看不到装配作用域），用它的人显式传 `gateway.WithDiscovery`；③ 不改 resolver 的负载均衡语义——多实例交给 gRPC 默认 `pick_first`，是故障转移不是分摊流量，要 round_robin 得在拨号侧另配；④ **来源只在 Start 取一次**，不做「注册中心被换掉后重取」：resolver 的生命周期跟 `ClientConn` 而不是跟装配，Start 之后再 `SetRegistrar` 换实例，已建连接仍指向旧实例。现契约下这条路是闭合的——registry 段既没有 `ReloadReconcile` 也没有 reconciler（reconciler 只由 domain 节点与组件注册，`pkg/appkit/bootstrap.go:508,534`），热更新真重建的只有 logger 段，所以换注册中心本来就要重启进程；⑤ 不动 `transport/http` 与业务自拨 gRPC 客户端（那是另一个消费面，需要独立的 client 抽象）；⑥ 不手工写 CHANGELOG 条目——该文件由 `bald release changelog` 从提交生成。
- 还不能证明：① **`Watch` 真机验证只覆盖四个生产后端里的两个**——etcd、nacos 各跑通上面那台状态机（全量快照推送、断线后自愈的 `reWatch` 只读到代码，未被本轮触发）；**consul、kubernetes 仍是代码阅读**。且两轮验的都是「客户端主动注册/注销」这条正向：nacos 服务端健康检查踢掉实例、etcd 租约过期后是否推空集，都没有造出那个故障场景，也未验多实例并发变更下的顺序。② **bald-admin 的真实消费未跑通**——它 pin `bald v0.34.0` 且零 `replace`，本次新增的 API 要模块 tag 才可用，而 tag 是对外共享状态、需 owner 决策；③ 本轮改动的 CI 结果（提交后以流水线为准）；④ nacos 那轮是在 `--platform linux/amd64` 模拟下跑的（官方镜像无 arm64 manifest），功能语义不受影响，但不是原生 arm64 的时序。

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
- 还不能证明：断环带来的实际收益只有「版本追赶不再成对」这一条，已经过 Linux CI（`5f771e0`：test/race/lint/vuln 全绿）与 `_example` 起进程实测。扇入本身没消：根 module 一次改动仍要让 22 个适配器重编，那要抽 `appkit` 叶子 module 才做得到，仍是 owner 决策项。

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

**3.2 收口（只读报告 + 把「未测」补成实测）**：

- 修法：`scripts/multi-major.sh`（逐 module `go mod edit -json`，按去掉尾部 `/vN` 的基础路径分组，列出同组内 ≥2 个 major 的组合）+ Taskfile `deps-multi-major`。明确**不是闸门**：不改 go.mod、不加 exclude/replace、退出码恒 0，`platforms: [linux, darwin]`（要 POSIX awk）。
- 实测（本轮）：**43 条 = 21 个 module**，按库分布 `go.yaml.in/yaml` 20、`alibabacloud-go/tea-utils` 20、`pelletier/go-toml` 2、`cenkalti/backoff` 1。与评审初值差一条：`nacos-sdk-go v1+v2` 只存在于 `_example/bald`，而清单谓词排除 `_example*`，报告按 CI 同一条派生所以不含它。
- **把「二进制里到底链了几个」这一条从推测变成证据**（原「未测」项）：`go list -deps ./...` 在根 module 与 `_example/bald` 各自展开后，`go.yaml.in/yaml/v2` + `/v3` **与** `pelletier/go-toml` + `/v2` 都真的同时进入依赖集——即同一二进制确实链接两套实现；`cenkalti/backoff/v5`、`nacos-sdk-go/v1` 则没有（被 MVS 剪掉），`tea-utils` 两个 major 都不在根的依赖图里（只在 `require` 清单里挂着）。
- 因此优先级口径落定：值得跟的不是「require 里并存」（43 条里多数是上游 SDK 传递），而是**「`go list -deps` 同时命中两个 major」这 2 条**——它们是能观测到行为差异/类型互不兼容的那一类。后续新增按此判据筛。
- 刻意没做：不加 `exclude`/`replace`（与上游 SDK 冲突，且会掩盖真实传递链）；不把报告接进 CI 阻断（它今天 43 条全属已知，接成闸门只会让 CI 长期红）。

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

## 5. 错误分类法未统一 ✅ 口径已定（选项 b），未改码

`berrors` 存在且设计良好，但采用度极低：
- 全仓非测试文件 import `berrors`：**13 个**（`berrors/` 自身除外）。
- `transport/http`：**0**　`broker/kafka`：**0**　`pkg/appkit`：**0**
- 同期 `transport` + `broker` + `pkg` 三个目录下非测试文件里 `fmt.Errorf` **278** 处、`errors.New` **102** 处。
- appkit 直接返回裸包装错误，如 `appkit.go:829` `fmt.Errorf("appkit: servers not ready within %s …")`。

例外（说明能力是真的）：`transport/web` 统一经 `berrors.FromError` + `httperr.CodeToHTTP` 映射并兜底 INTERNAL（`transport/web/response.go:56-93`）；gRPC 有集中式 `grpcmw.ErrorInterceptor`（链首，`pkg/middleware/bundle/bundle.go:298`）。

- 影响：**错误语义只在「泛型绑定面」成立**，在 server 构造面（transport/http、broker）不成立。跨语言/跨协议消费者拿到的是无码错误。
- 消解：二选一并停止摇摆——(a) 在 transport/broker 边界强制 `berrors`；(b) 承认 `berrors` 只服务 `transport/web` + gRPC 拦截器，从 README 的能力叙述里降级。

**收口（选 (b)）**：`berrors` 的定位写定为**边界投影模型**——只在协议投影收口点（HTTP 响应体、gRPC 拦截器）要求错误可用，内部构造面不强求；这不是「待补的采用率」，是设计承诺。理由与实测一并落在《[Bald 错误模型设计](../devel/zh-CN/Bald%20错误模型设计.md)》的「采用边界」节，此处只记结论：

- 复测（本轮，判据：`find` 全仓非测试文件 + 排除 `berrors/`、`_example/`）：berrors 族 importer **12 个**（transport/web 4、pkg/middleware/grpc 6、pkg/middleware/gin 2）；桥接子包 grpcerr 1、httperr 3；`transport`+`broker`+`pkg` 非测试文件 `fmt.Errorf` **279**、`errors.New` **103**。与评审初值同数量级，说明采用面没有自发增长。
- 下游 bald-admin：import 根包 **38 个**文件、import 桥接子包 **0 处**——投影被框架收口后业务仓库与桥接天然解耦，这正是 (b) 的兑现面，也是当初「零依赖」承诺的实际形态。
- **不修的代价写明白**：gRPC 侧非 berrors 错误经 `status.Convert` 保留原状态，裸 error 落 `codes.Unknown`；HTTP 侧非 berrors 错误落 `berrors.Internal`。两侧兜底不一致是**已决定的行为**（Unknown 表示「框架不认识这个错误」，Internal 表示「服务端出问题了」），不是待修的遗漏。
- 给接手者的两条硬规则：① 新增协议投影收口点必须走 berrors + 对应桥接子包；② 内部构造面继续用 `fmt.Errorf`，不要为了「统一」做全仓改写——那会把 279 处错误文案换成 279 处需要维护的 Reason 键，收益只在边界，成本摊在中间。

---

## 6. 安全

**6.1 鉴权 fail-open（高）** ✅ 已修
原状：`pkg/middleware/bundle/bundle.go` 的注释把设计说得很清楚——「未注入的依赖对应层直接省略（零开销），如未设 Authn 则链中无认证层」，实现即条件 `append`。后果：**一个没配 authenticator 的 bundle 就是一个无认证服务器，且没有任何启动告警**。零开销是优点，默认开放是风险，两者被同一个 `if` 绑在一起。
- 消解（已落地，零开销设计不变）：认证姿态必须被决定——`bundle.Authn(...)` 或 `bundle.Public()` 二者之一。`Bundle.Verify()`（`pkg/middleware/bundle/bundle.go:185-212`）在两者都没给时报 `AUTHN_UNDECLARED`，`Gin()`/`GRPCInterceptors()` 出链时对未决定的姿态告警一次；业务想要硬失败就在装配期调 `Verify()`。生成脚手架写出 `bundle.Public()` + TODO，使「本服务无认证层」成为一行显式声明而不是疏漏。
- 为什么不是构造期 panic：链是零开销省略的载体，`New()` 直接失败会让「先起链、再决定」的既有形态无处安放，也会让新 `gen app` 出的最小示例跑不起来（最小路径必须可复制即跑）。
- 回归：`pkg/middleware/bundle/authn_posture_test.go`（Authn/Public/两者都不给/只给 Authz 四种判定；两个出链点对未决定出声、对已决定不出声）。
- 未修：契约侧仍没有 authn 段（`server.*.middleware` 只有 recovery/cors/logging/request_id/tracing/rate_limit/timeout），认证器实例仍只能代码注入；`Server.Http.Middleware` 整体是「业务自取」的参考段，无框架消费者。

**6.2 gRPC 链缺限流与超时（中）** ✅ 已修（unary 面）

原状：`pkg/middleware/bundle/bundle.go` 的 `GRPCInterceptors()` 到 Authz 即结束，而 `Gin()` 另有 RateLimit + Timeout 两层。gRPC 面上的暴力尝试与慢 handler 没有任何框架级保护——同一对 `bundle.RateLimit`/`bundle.Timeout` Option 在 gRPC 侧被静默吃掉。

- 修法：新增 `pkg/middleware/grpc/ratelimit.go`（`RateLimitInterceptor(limiter, wait)`）与 `timeout.go`（`TimeoutInterceptor(d)`），`GRPCInterceptors()` 在与 gin 相同的链位（Authz 之后、最内层之前）按同一对 Option 挂载。**零新增 API、零配置零开销**（`TestEmpty_NoOverhead` 的基线长度 4 未变），复用 gin 侧 `RateLimiter` 已暴露的 `Limiter()`/`Wait()`，因此两条链共用同一个桶，不会出现「HTTP 与 gRPC 各扣一份配额」。
- 语义差别全部写在注释里而不是抹平：拒绝 = gin `429` vs gRPC `RESOURCE_EXHAUSTED`+`Reason=RATE_LIMITED`；超时 = gin 硬性提前 `503` vs gRPC 只给 handler 一个会到期的 ctx，**且绝不缩短调用方已带的 deadline**（`d<=0` 时 gRPC 省略该层，gin 照旧挂立即 503 的层——这是 gin 的既有行为，动它需要独立决策）。
- 为什么 gRPC 超时不把 handler 丢进 goroutine 提前返回：那样 panic 会绕过外层 `RecoveryInterceptor`（只覆盖自己调用栈），进程直接崩。代价（响应不保证按时结束）如实写进注释。
- 回归：`pkg/middleware/grpc/timeout_test.go`(6) + `ratelimit_test.go`(6) 逐条钉住上述语义；`pkg/middleware/bundle/grpc_contract_test.go`(6) 钉住链上真实生效——含「配额耗尽时授权拒绝仍以 `PERMISSION_DENIED` 返回」（限流确在授权之后）与「被限流的调用不穿过超时层」（Timeout 确在最内层）。三处判据均按变异验证会失败：删 deadline 保留分支 / 删非正值守卫 / 删 `defer cancel()` / 删 `err != nil` 拒绝 / 把 bundle 两处 `append` 短路。
- 未修（三项，都不是这次能顺手收的）：① **契约不对称仍在**——`Server.Grpc.Middleware` 没有 rate_limit/timeout 段，「写 gRPC 配置就能限流」依然不成立，gRPC 吃到的是 HTTP 那一段的 Option；补段要动 proto 并重发 `bconf`。② **stream 拦截器没有**——`GRPCChain()` 只出 unary 链，流式调用的限流/超时要业务自建。③ **bundle 的 gRPC 链没有自动装配**——std server 构造不吃 bundle，业务必须显式 `appkit.WithGRPCOptions(b.GRPCChain()...)`；把链默认塞进 std 服务器会连带改变 §6.1 的认证姿态默认，属行为变更。

- 附带修掉一处真缺陷：`tokenbucket.Close()` 过去只置 `closed`，从不写 `notify`，而 `pkg/middleware/gin/ratelimit.go` 承诺「`Close` 会唤醒阻塞在 `Wait` 的调用者」——停机时 `wait=true` 的请求会挂到自己那轮 `time.After(wait)` 到期（rate 低时是小时级）。现在 `Close` 在锁内 `close(notify)` 并以 `closed` 保证幂等。判据 `TestClose_WakesBlockedWait`（冻结时钟 + `rate=0.0001`，wait 算出约一万秒，`Close` 后必须 2 秒内返回）；原有 `TestClose_WaitReturnsErrAfterClose` 通过是因为 `New(1,1)` 的 wait 恰好 1 秒，**不是因为唤醒生效**。

**6.3 限流在认证之后（中，作者已知）** ✅ 口径已定，未改码

`pkg/middleware/bundle/bundle.go` 明确记录了这个取舍：为了认证后的 subject 可见（为按主体配额留位），代价是认证层自身不受这一层保护，匿名洪水先穿过认证/审计/授权才撞限流。

- 结论：**保持现位置，不改码**。把默认挪到认证之前会让现有部署静默变形——今天按主体计的配额一旦搬到认证前只能看到全局流量，等于悄悄把「按主体限流」换成「全局限流」，且没有任何报错。要保护认证层的部署显式把 `ginmw.RateLimit`/`grpcmw.RateLimitInterceptor` 挂到自己链首，两者都不要求认证上下文。
- 本轮补的是**口径落文档**：《Bald 限流设计》新增「链序与两侧口径」一节，给出实测链序图、双向代价、不改默认的理由、以及两面拒绝语义的差异；《Bald 中间件设计》的链序段同步。此前设计文档里写的是「限流在 `Recovery` 之后、`Authn` 之前」，与实现相反——文档在这一点上是错的，已按实现改写。

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

**6.6 机密** ✅ 未发现机密被写日志的证据；脱敏有文档化缺口：不覆盖 `slog.Attr` 形态（如 `slog.String("password", v)`），见 `bconf/gen/go/bootstrap/v1/log.pb.go:36` 的说明。**govulncheck 是 fail-closed 且覆盖全模块**（`vuln` 作业的 `install govulncheck` step 固定 v1.9.0，逐模块循环消费 `scripts/list-modules.sh`）——这一项没有问题。

---

## 7. 测试与验证策略

**7.1 CI 全量只跑 `-short`（中）** ✅ 评审初值：`test` 与 `race` 作业的每模块命令都是 `go test -short`（`.github/workflows/ci.yml` 的「build + vet + test all modules」/「go test -race (all modules)」两步）。按 CI 自己的注释，`-short` 的语义是**跳过集成测试**。因此 kafka/redis/nacos 之类的真实后端交互在 CI 里从不执行；唯一非 `-short` 的是「TestTree_EndToEndTimed / TestTree_AuthzClosedLoop」这一步的 `internal/codegen` 两条端到端用例。
叠加既有事实：asynq 集成测试需要 `BALD_TEST_REDIS_URI`，Redis 不可达时自动跳过——**注入与否决定这批用例是否真的跑过**。
- 消解：明确哪些接缝必须有集成覆盖（至少 store/registry/broker 各一条），为它们开一个按需注入依赖的 CI job，而不是继续在 `-short` 里假定绿。

**7.1 收口（已落地：`integration` 作业）**

- 修法：`.github/workflows/ci.yml` 新增 `integration` 作业，`services:` 注入 **etcd（quay.io/coreos/etcd:v3.5.14）/ redis:7-alpine / mongo:7**，三个后端地址由 `env:` 显式给出（`BALD_E2E_ETCD_ADDR`、`BALD_TEST_REDIS_URI`、`BALD_MONGO_URI`），跑三条接缝的非 `-short` 套件：`registry/etcd`（含其 `contract` 包，同 module）、`contrib/store-mongo`、`transport/asynq`。三步结构：探活 → 逐套件 `-v` 留档 → 按 EXPECTED 逐条断言 `--- PASS`。
- **为什么必须有第三步**（这是新增闸门最容易自欺的地方）：这些用例写的是「不可达即 `t.Skip`」，所以服务挂了作业照样绿。本地实测反证：把三个地址都指向死端口，**跑套件这一步 exit=0、23 条 SKIP**，而断言步骤 exit=1 并逐条点名五条判据。探活步同样实测：加一条指向 27018 的检查 → `::error::deadservice 未就绪`，作业在跑测试之前就失败，而不是放行成一片 Skip。EXPECTED 只列「非真服务不可证」的判据（`TestRegisterDiscoverDeregister`、`TestWatch_ReceivesChanges`、`TestProvider_BuildsUsableRegistrar`、`TestMongoCRUD`、`TestTaskSubscribe`），普通单测不重复列。
- 主循环为什么仍留 `-short`：`transport/{redis,kafka,rabbitmq,rocketmq,tcp,webrtc}` 的 `TestServer`/`TestClient` 是等信号的手动用例（`t.Skip("manual test: blocks on signal…")`），去掉 `-short` 会把作业挂到 timeout。集成作业因此**点名套件**而不是全仓扫。
- 验活：三个步骤从 YAML 里导出后用 bash 原样执行过（GitHub 默认 shell 同为 bash），正向三件套全绿（`READY etcd/redis/mongo` → 套件 ok → 五条判据逐条 PASS）；反向两条如上。YAML 由解析器加载通过（作业顺序 test/race/integration/lint/vuln）。三处描述「CI 无 etcd / 无 mongod」的过期注释（`registry/etcd/registry_test.go`、`registry/etcd/contract/contract_test.go`、`contrib/store-mongo/testutil.go`）改成指向 `integration` 作业。
- 刻意没做：① **`_example/bald` 不进 CI**——那里的服务发现端到端（etcd + nacos）只在本地跑，因为 `_example/bald/go.mod` 把两个 kratos contrib 模块 `replace` 到**仓库外**的同级检出（`../../kratos/contrib/{config,registry}/nacos`），干净检出装载不出模块图；要在 CI 跑示例得先给它们找到已发布版本，属另一件事。② 没有 `task integration` 本地入口——命令以 `integration` 作业与 README 为准，再加一份清单就是再造一条会漂移的真相。③ 不覆盖 consul / kubernetes / nacos 后端（`bconfig/kubernetes` 需要真集群，用 `BALD_KUBE_INTEGRATION` 显式开关，保持现状）。④ Kafka/RabbitMQ 的 broker 接缝没有可在 CI 里跑的集成用例（仓内那批是等信号手动用例，`broker/kafka` 的测试不需要真 Kafka）——§7.1 原话里「broker 一条」由 `transport/asynq`（Redis 队列）代。⑤ 未加覆盖率闸门（§7.2 另议）。
- 还不能证明：**GitHub Actions 上的实际作业结果**——`services:` 的镜像可用性、bridge 网络下 etcd/nacos 的端口映射、runner 的 Go 1.27.3 与本地 1.27.1 的差异，都只能在推送后以流水线为准。本地验证覆盖的是命令语义与判据逻辑，不含 GitHub 的运行环境。

**7.2 无覆盖率闸门** ✅ 所有 workflow grep `coverage|codecov` → 无匹配。根 module 有 365 个测试文件（绝对量可观），但没有任何比例约束，零测试模块不会因此暴露——§1.3 那两个适配器是评审时数测试文件点数出来的，不是闸门响出来的。

**7.3 已具备的成熟度**（避免只报负面）：`-timeout 5m/10m`、codegen 树的端到端用例拆成独立步骤单独给 15m 预算（`test` 作业的 `TestTree_*` step）、依赖下载 retry×3、golangci-lint/govulncheck 版本 pin（`install *` 两步）、基准冒烟编译每模块跑（均在 `.github/workflows/ci.yml`，行号随作业增删漂移，故按 step 名指）。核心包（`pkg/appkit`、`bconf`、`bconfig`、`transport/http`、`pkg/middleware`）非测试代码里 **TODO/FIXME/HACK = 0 命中**——干净到更像是「债务存在但没标记」，而非没有债务。

---

## 8. 文档漂移

`README.md`（81 行）**确实**描述了 v2 Provider（「段驱动的 Provider 契约」`:5`，appkit 一行含「段驱动 Provider」`:58`）——我原先的怀疑不成立。
`internal/docscheck/docscheck_test.go` 随 `go test ./...` 执行，校验两件事：每条相对 Markdown 链接的目标存在（`:21`）；Go 注释里引用的每个 `docs/*.md` 路径可解析（`:65`）。
- 缺口：**它只测链接完整性**，测不出「文字在描述已删除的 API」或「新增模块没进文档」。§2 那些未接线模块正是这类漂移的候选。
- 消解：加一条模块清单断言（`find` 结果 vs docs 中列举的模块集合），成本极低且能自动失效。

**8 收口（已落地，只覆盖「清单」这一半）**：`docs/architecture/current-architecture.md` §2 末尾加了一块 ` ```module-list ` 围栏（92 行，与 `scripts/list-modules.sh` 同源），`internal/docscheck` 新增两条断言：

- `TestModuleListMatchesDerivation`——Go 侧复刻同一条谓词（含 `go.mod` 的目录，排除 `.git`/`node_modules`/`vendor`/`_example*`，根记作 `.`），与文档块**双向**比对；另带两条防空转断言（派生数 <50、文档块为空 → `t.Fatal`），谓词一旦被改坏是响而不是静默通过。
- `TestModuleDerivationMatchesScript`——直接 `sh scripts/list-modules.sh` 与 Go 复刻逐行比对，把「测试里的谓词」钉在「CI 用的那一条派生」上，Windows 跳过（无 POSIX sh）。
- 变异验证（三条都实测过再复原）：删文档块里 `workflow/argo` → 「module workflow/argo 未出现在文档清单里」；塞 `not/a/real/module` → 「在仓库中不是 module」；把 Go 侧 `_example` 跳过改成 `if false &&` → 「Go 派生 93 个，脚本派生 92 个」。复原后 `go test ./internal/docscheck/` → ok。
- **未覆盖**：「文字描述已删除的 API」这半仍然测不出——那要逐条把散文里的断言映射到符号，成本与误报都不划算。现在的兜底是人肉：本轮 §2/§5/§6.2/§6.3 的每处过期描述都逐条 grep 复核过，行号引用优先改成符号名（`bundle.go:171-211,224-241` 这类漂移点已换成 `Gin()`/`GRPCInterceptors()`）。

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

1. **接上 gRPC TLS**（§6.5）——静默明文是本轮最该先修的项，改动集中在 `GrpcServerProvider` + 生成模板 + gateway 出站三处。✅
2. **`bundle` 加未配 Authn 的显式警告/契约开关**（§6.1）——改动小，风险面最高。✅
3. **`_ = deregister` 补日志**（§4.1）——一行。✅（连带 §4.2 的等待上界）
4. **race 清单去人工化**（§1.1）✅：两处手写清单已换成同一条 `find` 派生 + 单份 `.github/race-skip.txt`，webrtc/kafka/contrib 全量入闸，`metrics` 这类死条目随派生自然消失；扩范围当场抓到并修掉 `transport/graphql` 自身的生命周期竞争。本地 `task verify`（§1.2）也已接上同一条派生（`scripts/list-modules.sh`，92 模块实测 exit=0）。
5. **kafka 的 writer 重建 + 订阅退避补测试**（§1.3）✅：`broker/kafka` 与 `broker/rabbitmq` 各有了第一份测试，全部不起真实 broker（死端口造 ECONNREFUSED）。过程中揪出并修掉 4 个生产缺陷，其中「重连后发布 panic + 写锁泄漏」和「`retriesCount=0` 吞掉写失败」都只在失败/热更路径上，原先没有任何执行证据。
6. **给 §2 的未接线面做决策** ✅ 口径已定：reference-only 保留 + README/当前架构 §6 标注；其中 **Discovery 走的是选项 1（接线）而不是标注**——`transport/gateway` 出站现在真消费 `GetService`/`Watch`（见 §2「接线半」）。retry/circuitbreaker 的 bundle 层与「移出发布批次」仍按 §2「刻意没做」留给 owner。
7. **`berrors` 边界口径**（§5）✅ 选 (b)：定位为边界投影模型，不改码。**Discovery 消费侧**（§2）已做——watch-based resolver + 装配作用域自动派生来源，消费方只改一行配置。
8. **抽离 appkit 契约面，把适配器从「require 聚合根」解耦**（§3.1）——breaking，放在最后，且只在 §5/§6 的口径定清之后做。模块环已由 gateway 那一条的重复机制删除而断开（实测图为 DAG），这条剩下的收益是 `replace` 数量与版本追赶，不再是断环。
9. **gRPC 链补限流与超时**（§6.2）✅ unary 面：`RateLimitInterceptor`/`TimeoutInterceptor` + bundle 两侧共用同一对 Option，当场揪出并修掉 `tokenbucket.Close()` 不唤醒 `Wait` 的真缺陷。剩 stream 面、gRPC 独立契约段、自动装配三项（见 §6.2「未修」）。
10. **限流链序**（§6.3）✅ 口径已定：保持「认证之后」，落文档；改默认会把按 subject 的配额静默变成全局配额。顺带修掉设计文档里写反的链序。
11. **文档漂移闸门**（§8）✅ 清单半：`module-list` 围栏 + 双向断言 + 与脚本逐行比对（三条变异实测）。「描述已删除 API」半仍未覆盖。
12. **依赖卫生只读报告**（§3.2）✅：`scripts/multi-major.sh` + `task deps-multi-major`，再用 `go list -deps` 把「是否真同链」从推测改成证据；不加 exclude/replace。
13. **CI 集成作业**（§7.1）与 **覆盖率闸门**（§7.2）——未做，见各节。
