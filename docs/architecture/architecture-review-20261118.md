# bald 整体架构评审（2026-11-18）

> **基线**：`main` @ `984a9655`，工作树干净（仅 `.dev/` 下未入库交接文件）。
> **范围**：全仓 69 个 `go.mod`，分五个切片并行评审——装配核心与配置面、传输层与中间件、基础设施适配器、
> 官方 CLI 与 contrib、跨切面结构（依赖/门禁/安全/示例）。
> **去重口径**：已登记于 `.dev/smell.md`（C-1…8、W-1…12）、[risk-review.md](risk-review.md)、
> [architecture-debt-remediation.md](architecture-debt-remediation.md)、
> [developer-experience-review.md](developer-experience-review.md)、
> [transport-unification-assessment.md](transport-unification-assessment.md) 的条目一律不重报；
> 每条候选报告前都按文件名/符号名在上述登记册里 grep 过。
> **判据**：[设计判据](../devel/zh-CN/设计判据.md) J1–J9。死代码按 J1 不算缺陷。
> **本评审不改任何代码**。复现用的临时测试跑完即删。

## 证据等级

| 标记 | 含义 |
|---|---|
| 【已复现】 | 写了最小复现测试或实跑命令，下文附输出摘要 |
| 【读码确证】 | 给出完整代码路径推理链，未实跑 |
| 【疑似】 | 有结构性证据，触发条件未完全确认 |

高严重度条目除上述证据外，主会话又逐条回读源码复核过。行号以 `984a9655` 为准，会随修复漂移，复核时以符号名为准。

---

## 总览

本轮共登记 **31 条**新缺陷：高 11、中 13、低 7。它们集中在四类系统性问题上：

1. **停机序与生命周期**（AR-1/2/12/25/27）。provider 资源 cleanup 混进了"撤销全局写入"的效应账本，
   结果在 server 排空**之前**就关了 DB/缓存/注册中心/logger。deregister 也不受 `stop_timeout` 约束。
2. **跨服务共享的可变状态**（AR-6/7）。节点熔断 Registry、registry 缓存的节点 TTL 表都是进程内单例，
   但 prune/过期语义是按"本次调用的候选集"写的。多下游、默认配置下熔断永不跳闸，服务发现会返回空列表。
3. **不安全默认值**（AR-9/11/14/15/16/22/23）。`amqps://` 默认跳过证书校验、入站 HTTP 无读头超时、
   出站客户端无默认超时（J3）、JWT 不要求 `exp`、`meta.name` 可路径穿越、原子写会放宽文件权限。
4. **静默失败与契约超承诺（J8）**。配置两遍发现规则不一致导致运行期 `Settings()` 为空（AR-3）、
   gin 超时中间件既不结束响应也不取消 ctx（AR-13）、对冲一次节流后永久关闭（AR-19）、
   redis cluster MGET 文档承诺按 slot 拆分但实现没拆（AR-20）、etcd 心跳耗尽重试后静默退出（AR-8）。

另有一个横切结论：**崩溃级 panic 隔离有两个洞**（AR-4/5）。gin 超时中间件起的 goroutine 和
未启用统一段时的 gRPC 流式链都没有 recover，业务 handler 一次 panic 就会让整个进程退出。

---

## 严重度总表

| # | 缺陷 | 位置 | 严重度 | 证据 |
|---|---|---|---|---|
| AR-1 | 停机阶段 0 先回放 provider cleanup，资源在 server 排空前就被关闭 | `pkg/appkit/appkit.go:831`、`pkg/appkit/cleanup_slots.go:66-90` | 高 | 已复现 |
| AR-2 | 停机 deregister 用 `context.Background()`，注册中心不可达时进程挂死 | `pkg/appkit/appkit.go:729`、`:762` | 高 | 已复现 |
| AR-3 | 预装载与正式装载的配置文件发现规则不一致，运行期配置为空且不受 watch | `pkg/appkit/serve.go:184`、`pkg/appkit/appkit.go:562-567`、`bootstrap/config/config.go:488-514` | 高 | 已复现 |
| AR-4 | gin Timeout 中间件 goroutine 内无 recover，handler panic 让进程崩溃 | `pkg/middleware/gin/timeout.go:89-93` | 高 | 已复现 |
| AR-5 | 未启用统一段时 gRPC 流式链没有 Recovery，stream handler panic 让进程崩溃 | `pkg/middleware/bundle/bundle.go:648-670` | 高 | 已复现 |
| AR-6 | registry 缓存默认 TTL 组合下，第 30–60s 返回空实例列表 | `registry/cache/cache.go:136-182` | 高 | 已复现 |
| AR-7 | 节点熔断 Registry 跨服务共用，互相 Prune 导致熔断永不跳闸 | `selector/circuitbreaker/circuitbreaker.go:94-117` | 高 | 已复现 |
| AR-8 | etcd 心跳重试耗尽后 goroutine 静默退出，实例被永久摘除 | `registry/etcd/registry.go:300-316` | 高 | 读码确证 |
| AR-9 | rabbitmq `amqps://` 未配 TLSConfig 时默认 `InsecureSkipVerify: true` | `broker/rabbitmq/connection.go:308-312` | 高 | 读码确证 |
| AR-10 | release 对子目录 v1+ module 解析版本失败，推出降级版本；root 升 major 打出非法 tag | `cli/internal/release/plan.go:209-217`、`cli/internal/release/repo.go:139-157` | 高（潜伏） | 已复现 |
| AR-11 | Gate1 不校验 `meta.name`/角色名形状，`gen app` 写到 `--out` 之外且永不回收 | `cli/internal/codegen/appspec_validate.go:89-94` | 高 | 已复现 |
| AR-12 | 应用 id/version/stop_timeout 冻结在预装载值，远程层与 flag 层的值不生效 | `pkg/appkit/bootstrap.go:224-230`、`pkg/appkit/decl.go:239-243` | 中 | 读码确证 |
| AR-13 | gin Timeout 写 503 后仍等 handler 返回，也不取消 handler 的 ctx | `pkg/middleware/gin/timeout.go:118-123` | 中 | 已复现 |
| AR-14 | CORS 多源拼成 `a, b` 单个响应头，浏览器拒绝全部跨域请求 | `pkg/middleware/bundle/middleware.go:215-222` | 中 | 读码确证 |
| AR-15 | 出站 HTTP/gRPC 客户端契约默认无超时（违反 J3） | `transport/client/http.go:64`、`transport/client/grpc.go:78` | 中 | 读码确证 |
| AR-16 | 所有入站 `http.Server` 无 ReadHeaderTimeout/IdleTimeout、无请求体上限，契约也配不了 | `transport/http/http_server.go:44` 等 6 处 | 中 | 读码确证 |
| AR-17 | registry 缓存回源用 `WithoutCancel`，不传播调用方 deadline | `registry/cache/cache.go:97-99` | 中 | 已复现 |
| AR-18 | 节点熔断把任何错误（含 4xx/Canceled）都计为失败，与设计文档口径矛盾 | `selector/circuitbreaker/circuitbreaker.go:176-180` | 中 | 读码确证 |
| AR-19 | Hedger 自适应节流触发后永久关闭对冲 | `retry/hedge.go:89-112` | 中 | 已复现 |
| AR-20 | redis cluster 下 GetMulti 报 CROSSSLOT，设计文档承诺按 slot 拆分（J8） | `cache/redis/redis.go:102` | 中 | 读码确证 |
| AR-21 | rabbitmq 解码失败仍以零值调用 handler 并 ack，毒消息被吞 | `broker/rabbitmq/rabbitmq.go:275-285` | 中 | 读码确证 |
| AR-22 | fsatomic 覆盖写重置已有文件权限，`chmod 600` 的 MCP 配置被放宽为 0644 | `cli/internal/fsatomic/fsatomic.go:58-83` | 中 | 读码确证 |
| AR-23 | authn-jwt 不要求 `exp`、无 aud 校验；非法/不在白名单的签名算法构造期不报错 | `contrib/authn-jwt/jwt.go:170-210` | 中 | 已复现 |
| AR-24 | `_example/bald` 不在 lint/vuln 门禁内，lint 现为红（5 处） | `scripts/list-modules.sh`、`.github/workflows/ci.yml` | 中 | 已复现 |
| AR-25 | 配置 watcher 在停机期间仍活跃，热更会在 logger 恢复后重建 logger | `pkg/appkit/appkit.go:648` | 低 | 疑似 |
| AR-26 | gin Recovery 丢弃 panic 日志；契约注释与实现语义相反（J8） | `pkg/middleware/gin/recovery.go:29` | 低 | 读码确证 |
| AR-27 | gateway 在 Stop 先于 Start 时泄漏后端连接与 resolver | `transport/gateway/gateway_server.go:151-204` | 低 | 读码确证 |
| AR-28 | resiliency 注释承诺"每个 attempt 完整超时窗"，实际所有重试共用一个截止期 | `transport/client/resiliency/resiliency.go:91`、`transport/client/grpc.go:78-84` | 低 | 读码确证 |
| AR-29 | loki 推送失败整批丢弃，且第 batchSize 条的调用方同步阻塞在 HTTP 上 | `log/loki/loki.go:156-171` | 低 | 读码确证 |
| AR-30 | broker 层并发小缺陷群（kafka writer 重建、rabbitmq 重连/订阅、SubscriberSyncMap、etcd watcher） | 见条目 | 低 | 读码确证 |
| AR-31 | 审计失败面与文档承诺不符（audit-gorm panic 丢事件、audit-stream Close 后静默丢） | `contrib/audit-gorm/store.go:90-93`、`contrib/audit-stream/stream.go:139-140` | 低 | 读码确证 |

---

## 一、装配核心与生命周期

### AR-1【高】停机阶段 0 先回放 provider cleanup，资源在 server 排空前就被关闭

- **位置**：`pkg/appkit/appkit.go:831`（`stopAll` 阶段 0 `a.UndoEffects(parent)`）；
  `pkg/appkit/cleanup_slots.go:66-90`（`cleanupSlots.effects` 把**每个 provider 的 cleanup** 注册成 Effect）。
- **触发→后果**：`stopAll` 的注释把阶段 0 定义为"撤销装配期全局写入，先于一切停机钩子"，但 `effects()`
  把 DB/缓存/注册中心/logger 等全部 provider 的资源 cleanup 也塞进了同一个账本。于是正常停机的实际顺序变成：
  `provider cleanup → BeforeStop → Server.Stop(drain) → AfterStop → Dispose`。
  - 排空期间仍在处理的请求、BeforeStop 钩子拿到的是已关闭的客户端；
  - logger 槽位被 `log.SetLogger(oldLogger)` 切回装配前的 logger（生产上通常是静默 logger），
    之后 server stop 与 Dispose 的失败日志全部丢失。
  - 这与同一注释里阶段 4 的承诺"钩子期间组件仍可用"相矛盾：组件还在，组件依赖的 provider 资源已经关了。
- **证据**【已复现】：打桩记录停机序，输出 `[provider(registry).cleanup BeforeStop server.Stop(drain)]`。
  主会话回读 `cleanup_slots.go:66-90` 确认 logger 之外的槽位一律以 `effectOpt` 注册。
- **建议修法**：阶段 0 只保留全局句柄恢复类 Effect；provider 资源 cleanup 移到 AfterStop 之后、Dispose 之后执行，logger 最后。

### AR-2【高】停机 deregister 不受 `stop_timeout` 约束

- **位置**：`pkg/appkit/appkit.go:729`、`:762`，均为 `a.deregister(context.Background())`。
- **触发→后果**：注册中心不可达时（etcd `Delete` 会一直等到连接就绪），deregister 永久阻塞，
  进程走不到 `stopAll`，`app.stop_timeout` 形同虚设，最后只能被编排系统 SIGKILL。
  这违反 J3 的精神（出站调用必须有界）。risk-review §4.1 只修了"deregister 错误被吞"，与本条是两个缺陷。
- **证据**【已复现】：打桩 Registrar 记录 ctx，`Deadline()` 返回 `ok=false`；stopTimeout=200ms 时 cancel 后 3s Run 仍未返回。
- **建议修法**：用 `context.WithTimeout(ctx, phaseBudgetFor(a.stopTimeout))` 包一层。

### AR-3【高】两遍配置装载发现的不是同一个文件

- **位置**：预装载 `pkg/appkit/serve.go:184`（`Load{Name: name}`，不带 Env）；正式装载 `pkg/appkit/appkit.go:562-567`
  （`Name: a.cfgNamespace, Env: a.cfg.env`）；发现逻辑 `bootstrap/config/config.go:488-514`。
- **触发→后果**：依赖自动发现（不传 `--config`），且满足任一条件：配置文件里设了 `app.env`；`app.name` 与 `Name` Option 不同。
  - 预装载按 Option Name 找到 `bald-app.yaml`；
  - 正式装载只找 `<namespace>-<env>.yaml`。`discoverLocalFile` 的注释写着查找顺序是
    "`Name-Env.yaml` > `Name.yaml`"，但代码在 `Env != ""` 时**只**试 `Name-Env`，没有回退。
  - 启动看起来正常，因为启动同步把预装载的契约合并进来了；但运行期 `Settings()` 为空，业务段读不到值，
    文件也不在 watch 里。第一次热更会静默丢掉所有来自文件的契约值。
- **证据**【已复现】：两种触发方式都打印 `mybiz/app/server=<nil>`；对照组（无 env、name 一致）读值正常。
  主会话回读 `config.go:503-506` 确认没有回退分支。
- **建议修法**：正式装载复用预装载解析出的文件路径；`discoverLocalFile` 补上注释承诺的 `Name.yaml` 回退。

### AR-12【中】应用身份与 stop_timeout 冻结在预装载值

- **位置**：`pkg/appkit/bootstrap.go:224-230`、`pkg/appkit/decl.go:239-243`、`pkg/appkit/appkit.go:896-898`。
- **触发→后果**：id/version/stop_timeout 从预装载契约读取，而预装载没有配置中心层、没有 flag 层；
  最终配置同步之后这些字段不再刷新。因此注册到注册中心的实例 version、停机预算都会忽略 nacos 等远程层和 flag 的值，
  违反 J9（配置取值只有契约一条通道）。预装载注释还声称"挂远程层"，实际没有传入远程层。
- **证据**【读码确证】。
- **建议修法**：启动同步之后用最终契约刷新这些字段；或两者不一致时 fail-fast。

### AR-25【低】配置 watcher 在停机期间仍活跃

- **位置**：`pkg/appkit/appkit.go:648`，`closeStore` 是 defer，晚于 `stopAll` 执行。
- **触发→后果**：停机窗口内发生配置变更，热更路径会在 logger 已恢复之后重建 logger，新后端泄漏，全局 logger 被改写。
- **证据**【疑似】：时序窗口窄，未构造稳定复现。
- **建议修法**：`stopAll` 之前先关配置 store。

---

## 二、传输层与中间件

### AR-4【高】gin Timeout 中间件里 handler panic 会让进程崩溃

- **位置**：`pkg/middleware/gin/timeout.go:89-93`，`go func(){ defer close(done); c.Next() }()` 内无 recover；
  链序见 `pkg/middleware/bundle/bundle.go:405`、`:432-435`。
- **触发→后果**：开启 `server.http.middleware.timeout` 后，后续 handler 都在新 goroutine 里执行。外层
  `ginmw.Recovery` 的 defer 在另一条 goroutine 上，接不住 panic，进程直接退出。
  risk-review §6 只把"起 goroutine 会丢 recover"当作 gRPC 侧不起 goroutine 的理由，gin 侧没有登记。
- **证据**【已复现】：子进程挂 `Recovery()+Timeout(1000ms)`，handler `panic("boom")`，`exit status 2`，栈顶 `timeout.go:92`。
- **建议修法**：goroutine 内 recover，把 panic 值带回主 goroutine 后重新 panic，交给外层 Recovery 处理。

### AR-5【高】未启用统一段时 gRPC 流式链没有 Recovery

- **位置**：`pkg/middleware/bundle/bundle.go:648-670`（`GRPCStreamInterceptors`）。
- **触发→后果**：unary 链在面段里有 Recovery；流式链只有 opt-in 的统一段，再加上可选的 Error/RateLimit，默认没有任何 recover。
  grpc-go 本身也不 recover，stream handler 一 panic 进程就退出。risk-review §6.2 只登记了流式链缺认证/授权/审计。
- **证据**【已复现】：`New(Public(), RateLimit(..)).GRPCChain()` 起真实 server，stream handler panic，子进程 `exit status 2`。
- **建议修法**：流式链最外层固定挂统一 Recovery（经 Stream 桥），与 unary 面对齐。

### AR-13【中】gin 超时既不结束响应，也不取消 ctx

- **位置**：`pkg/middleware/gin/timeout.go:118-123`，超时分支写完 503 后 `<-done` 等 handler 返回。
- **触发→后果**：客户端收到 503 响应头后，body 一直挂到 handler 自然返回才结束；handler 的 ctx 也从未取消。
  对客户端来说时延等于 handler 时延，这层超时基本无效。`timeout.go:41` 的"以 StatusCode 结束响应"、
  `bundle.go:386` 的"硬切断"都超出了实现（J8）。W-12 登记的是内存放大，与本条无关。
- **证据**【已复现】：超时 100ms、handler 睡 1.5s，`headerAt=101ms bodyDoneAt=1.50s ctxCancelled=false`。
- **建议修法**：用 `context.WithTimeout` 替换 `c.Request` 的 ctx；超时分支写完 503 后立即返回，缓冲交给 handler goroutine 回收。

### AR-14【中】CORS 多源拼接成非法响应头

- **位置**：`pkg/middleware/bundle/middleware.go:215-222`（`joinOr(c.GetAllowedOrigins(), …)` 用 `", "` 连接）；`pkg/middleware/gin/cors.go:51`。
- **触发→后果**：`allowed_origins` 配两个及以上源时输出 `Access-Control-Allow-Origin: a, b`。Fetch 规范只接受单个源或 `*`，
  浏览器会拒绝所有跨域请求，配置静默失效。实现也不回显请求 Origin、不写 `Vary: Origin`。
  `["*", "x"]` 配合 credentials 还能绕过 `Validate`。代码注释"写单个响应头故多源用 `, ` 连接"本身就是错误前提。
- **证据**【读码确证】。
- **建议修法**：按请求 Origin 匹配白名单后回显单个值，加 `Vary: Origin`；`Validate` 拒绝 `*` 与具体源混用。

### AR-15【中】出站客户端契约默认无超时（违反 J3）

- **位置**：`bconf/proto/bootstrap/v1/client.proto:33-35`（"0 表示不设默认，交给调用方 ctx"）；
  `transport/client/http.go:64`（`&http.Client{}`）；`transport/client/grpc.go:78`。
- **触发→后果**：条目不写 `timeout` 时 HTTP/gRPC 都没有超时，对端不响应就一直挂住。J3 明文否定了"随调用方 ctx"的口径。
  同类：`oss/s3/client.go:47-52` 也无默认超时（并入本条）。
- **证据**【读码确证】。
- **建议修法**：未配置时给框架默认值（如 30s），显式配置才能放宽。

### AR-16【中】入站 HTTP 无读头/空闲超时、无请求体上限

- **位置**：`transport/http/http_server.go:44`，同形 `transport/websocket/server.go:175`、`transport/sse/server.go:150`、
  `transport/mcp/server.go:175`、`pkg/metricshttp/metricshttp.go:45`、`cobramcp/rest.go:107`。
- **触发→后果**：slowloris（逐字节发请求头）或空闲 keep-alive 连接会无限占用 goroutine 与 fd。gin Timeout 只管 handler 阶段，管不到读头。
  全仓无 `MaxBytesReader`，请求体无上限。`server.http` 契约里也没有字段可配，`.golangci` 未开 gosec，G112 抓不到。
- **证据**【读码确证】：全仓 grep `ReadHeaderTimeout|IdleTimeout|MaxBytesReader|MaxHeaderBytes` 零命中。
- **建议修法**：构造期默认 `ReadHeaderTimeout=10s`、`IdleTimeout=120s`；`server.http` 契约开放 timeouts 与 `max_body_bytes`。

### AR-26【低】gin Recovery 丢弃 panic 日志

- **位置**：`pkg/middleware/gin/recovery.go:29`，`RecoveryWithWriter(io.Discard, …)`，回调里也不记日志。
- **触发→后果**：HTTP panic 不留日志和栈（统一 Recovery 会记）。契约 `server.proto:34` 写"记录堆栈，默认 true"，
  实现是"响应体带栈，默认 false"，两者语义相反（J8）。
- **建议修法**：回调里 `log.Error` 记录栈；契约注释改成与实现一致。

### AR-27【低】gateway 在 Stop 先于 Start 时泄漏连接

- **位置**：`transport/gateway/gateway_server.go:151-204`。
- **触发→后果**：启动失败回滚时 `stopAll` 可能早于 gateway `Start`。Stop 见 `conn == nil` 跳过，Start 随后建的连接和 resolver 永不关闭。
- **建议修法**：加 `stopped` 标志，Start 发现已停止直接返回。

### AR-28【低】resiliency 的超时语义与注释不符

- **位置**：`transport/client/resiliency/resiliency.go:91` 注释承诺"每个 attempt 拿完整超时窗"；
  `transport/client/grpc.go:78-84` 把超时拦截器装在重试外层。
- **触发→后果**：第一次尝试耗尽超时后，后续重试立即以 deadline exceeded 失败，重试对超时类故障无效。
- **建议修法**：超时拦截器挪到重试内层，或改注释并在文档里写明是总预算。

---

## 三、基础设施适配器

### AR-6【高】registry 缓存在默认配置下返回空实例列表

- **位置**：`registry/cache/cache.go:136-182`；默认值 `cache.go:25`（TTL 1m）、`:28`（NodeTTL 30s）。
- **触发→后果**：第 30–60s 之间服务级快照仍新鲜（`isValidLocked` 为真），但 `pruneExpiredNodesLocked`
  把全部节点剪掉并从 `nttls` 删除，返回空列表、不回源，调用方当成"无可用实例"。
  更怪的是下一次调用 `nttls` 已空，走 `len(nttls) == 0` 分支又返回全量快照——结果在空与满之间抖动。
- **证据**【已复现】：`t=35s first=0 second=1 underlying=1`。主会话回读确认剪空后既不回源也不标记失效。
- **建议修法**：剪枝后为空时视为服务级失效并回源；或强制 `NodeTTL >= TTL`。

### AR-7【高】节点熔断 Registry 跨服务共用，互相 Prune

- **位置**：`selector/circuitbreaker/circuitbreaker.go:94-117`（`Prune` 按"本次候选集"删除不在其中的熔断器）、`:167`；
  示例接线 `_example/bald/.../outbound.go:51`、`:69`；`selector/balancer/balancer.go:185`。
- **触发→后果**：进程内有两个以上下游时共用一个 Registry。A 服务 Select 时把 B 服务所有节点的熔断器 Close 并删除，
  计数反复清零，熔断永不跳闸。实例不带 `bald.selector.service` 元数据时（仓内无任何地方设置），p2c 的 EWMA 状态也同样跨服务互清。
- **证据**【已复现】：A、B 交错调用，A 节点连续失败 6 次（阈值 3）仍 `err=<nil>`；对照组（无 B 流量）返回 `circuit is open`。
- **建议修法**：Registry 按服务隔离（键含服务名），或 Prune 只处理本服务节点集。

### AR-8【高】etcd 心跳重试耗尽后静默退出

- **位置**：`registry/etcd/registry.go:300-316`。
- **触发→后果**：etcd 中断超过约 35s（`max_retry` 耗尽），`kac == nil` 直接 `return`，无日志、无回调。
  租约过期后实例被永久摘除，进程却以为自己仍在注册中心里，只能靠重启恢复。
- **证据**【读码确证】：主会话回读确认 `return` 前没有任何上报。
- **建议修法**：改为无限退避重注册；至少记错误日志并把状态暴露给健康检查。

### AR-9【高】rabbitmq `amqps://` 默认跳过证书校验

- **位置**：`broker/rabbitmq/connection.go:308-312`；契约入口 `broker/rabbitmq/contract/contract.go:39-45`。
- **触发→后果**：配置 `broker.rabbitmq.url=amqps://...`。契约层只传地址不传 TLSConfig，`TLSClientConfig == nil`
  时代码补的是 `&tls.Config{InsecureSkipVerify: true}`。用户以为开了 TLS，实际对端未被验证，中间人可截获
  契约注入 URL 的用户名/密码和消息正文。契约里也没有 `tls` 字段可以纠正。
  `connection_state_test.go:78-79` 还把"应写入 InsecureSkipVerify"断言成了预期行为。
- **证据**【读码确证】：登记册 grep `InsecureSkipVerify|amqps` 只命中 tencent（§7.26）。
- **建议修法**：nil 时用 `&tls.Config{ServerName: host}`（默认校验）；`broker.rabbitmq` 契约补 `tls`（复用 `bconf/tlsconf`），跳过校验必须显式声明。

### AR-17【中】registry 缓存不传播调用方 deadline

- **位置**：`registry/cache/cache.go:97-99`（`context.WithoutCancel`）。
- **触发→后果**：singleflight 回源与调用方 ctx 解绑是对的，但**等待**结果时也不看调用方 ctx，调用方 deadline 失效。
- **证据**【已复现】：调用方 deadline=100ms，实测 3s 后返回 `err=<nil>`。
- **建议修法**：等待 singleflight 结果时 `select` 调用方 ctx。

### AR-18【中】节点熔断不区分失败类型

- **位置**：`selector/circuitbreaker/circuitbreaker.go:176-180`。
- **触发→后果**：NotFound、InvalidArgument、Canceled 都计为失败。示例配置 10s 内 3 次即跳闸，健康节点会因业务 4xx 被硬排除。
  与《Bald 熔断设计》的失败口径（4xx 与 Canceled 不记）矛盾。
- **建议修法**：复用中间件侧已有的失败分类函数，两处判据单一来源（J2）。

### AR-19【中】Hedger 节流后永久关闭对冲

- **位置**：`retry/hedge.go:89-112`。
- **触发→后果**：自适应节流触发后不再发备份请求，统计也就不再更新，比率永远停在阈值之上——一次抖动就让对冲永久失效。
- **证据**【已复现】：后续 5 轮调用均 `fn calls=1`，`backupTotal` 停在 1。
- **建议修法**：统计加时间窗或指数衰减，节流期保留低比例探针备份。

### AR-20【中】redis cluster 下 GetMulti 报 CROSSSLOT（J8）

- **位置**：`cache/redis/redis.go:102`；设计文档《Bald 缓存设计》第 100 行。
- **触发→后果**：go-redis 的 ClusterClient 按首个 key 的 slot 路由 MGET，不会拆分，跨 slot 直接报错。文档却声称按 slot 拆分。
- **建议修法**：cluster 模式下按 slot 分组并发 MGET 再合并；或改文档并在 cluster 模式下降级为 pipeline GET。

### AR-21【中】rabbitmq 解码失败仍调用 handler

- **位置**：`broker/rabbitmq/rabbitmq.go:275-285`。
- **触发→后果**：毒消息以零值交给业务 handler 处理并 ack，数据被静默吞掉。kafka 侧解码失败不调 handler，两份 broker 口径不一致。
- **建议修法**：与 kafka 对齐，解码失败记错误并 nack/进死信，不调 handler。

### AR-29【低】loki 推送失败整批丢弃

- **位置**：`log/loki/loki.go:156-171`。
- **触发→后果**：发送前先清空缓冲、错误被吞，一次 5xx/429 丢一整批（默认 100 条）。触发 flush 的第 batchSize 条日志调用方同步阻塞在 HTTP 上，最长 10s。
- **建议修法**：失败批次有界回填重试；flush 移到后台 goroutine。

### AR-30【低】broker 层并发小缺陷群

均为【读码确证】，单条影响有限，建议随 smell.md 未解决项 1（kafka/rabbitmq transport 统一）一并处理：

- kafka 并发重建 writer 时覆盖旧 writer 而不 Close：`broker/kafka/kafka.go:432-437`、`:518-523`；重试 `Sleep` 不看 ctx：`:416`、`:504`；
  Subscribe 改写共享 `Dialer.Timeout`：`:612-615`。
- rabbitmq 重连循环不检查 Close，Close 后仍无限重拨并留下 `connected=true` 僵尸态：`broker/rabbitmq/connection.go:215-223`
  （C-3/C-5 修的是二次 close 与无锁写，本条未覆盖）。
- rabbitmq subscriber 无锁读 `conn.close`：`broker/rabbitmq/subscriber.go:76`；`wg.Add` 与 Disconnect 的 `Wait` 并发：`:114`；
  `s.ch = ch` 前不复查 closed：`:102-104`。
- `SubscriberSyncMap` 删除不校验身份，同 topic 旧订阅者 Unsubscribe 会删掉新条目：`broker/subscriber.go:107-117`（risk-review 只部分覆盖）。
- etcd watcher `reWatch` 替换 `w.watcher` 无同步：`registry/etcd/watcher.go:95-100`。

---

## 四、CLI 与 contrib

### AR-10【高·潜伏】release 子目录 v1+ module 推出降级版本

- **位置**：`cli/internal/release/repo.go:139-157`（`latestTag` 返回完整 tag，如 `pkg/v1.2.3`）；
  `cli/internal/release/plan.go:209-217`（`parseVersion` 只去掉开头的 `v`）。
- **触发→后果**：`"pkg/v1.2.3"` 拆成 `["pkg/v1","2","3"]`，`Atoi("pkg/v1")` 失败按 0 处理，feat 提交算出 `pkg/v0.3.0`。
  这个版本低于现有版本，`go get` 永远选不到。当前全仓 tag 都是 v0，`major` 恰好为 0，所以没暴露。
  另外 root module breaking 产出 `v2.0.0` 但 module 路径未加 `/v2`，代理会判 tag 非法；`Tag()` 两种情况都不拦。
- **证据**【已复现】：包内临时测试，`pkg/v1.2.3`+feat 得 `next=v0.3.0`；root `v1.2.3`+breaking 得 `v2.0.0`。
  主会话回读确认 `latestTag` 只在匹配时去前缀、返回值仍是完整 tag。
- **建议修法**：解析前先去掉 `dir/` 前缀；major ≥ 2 时要求 go.mod 路径以 `/vN` 结尾，否则报错。

### AR-11【高】`meta.name` 可路径穿越，生成物写出 `--out` 且永不回收

- **位置**：`cli/internal/codegen/appspec_validate.go:89-94`（只判非空）、`:198-206`（角色名同样只判非空）；
  `cli/internal/codegen/manifest.go:212`（`pruneStaleOwned` 跳过 `..` 路径）。
- **触发→后果**：`meta.name` 填 `"../../escaped"`，`gen app --out .` exit 0，并在 `--out` 上级目录建出
  `escaped-api/main.go`、`escaped-api/app/*`。清单记成 `../escaped-api/...`，prune 跳过 `..`，这些文件永远不会被回收。
  appspec 来自 AI 会话或第三方模板时，这是任意目录写入面。
- **证据**【已复现】：临时目录用编译出的 bald 实跑；角色名路径为【读码确证】。
- **建议修法**：仿 `variantNameRe` 给 `meta.name`、`roles[].name`、`domains[].name` 加 `^[a-z][a-z0-9-]*$` 白名单（J7）。

### AR-22【中】fsatomic 覆盖写放宽已有文件权限

- **位置**：`cli/internal/fsatomic/fsatomic.go:58`、`:72`、`:83`；调用点 `cli/internal/ide/ide.go:201`、`:293`。
- **触发→后果**：旧的 `os.WriteFile` 覆盖已有文件时保留原 mode；现在是临时文件 + `Chmod(perm &^ umask)` + rename，
  结果恒为 `0644 &^ umask`。`ide init` 会合并全局 MCP 配置（可能含其他服务的 API key），用户 `chmod 600`
  的文件会变成其他用户可读，与包注释"防止被放宽"的初衷相反。还会打断硬链接、改变属主；
  只读文件在 Unix 上可被覆盖（rename 成功），Windows 上失败，两平台不一致。
- **证据**【读码确证】：Windows 无法区分权限位，未实跑。
- **建议修法**：目标已存在时 `Stat` 取原 mode（及属主）替代传入的 `perm`。

### AR-23【中】authn-jwt 构造期与验签期把关不严

- **位置**：`contrib/authn-jwt/jwt.go:175`、`:184`、`:195`（`GetSigningMethod` 返回值未判 nil）；`:208`（parser 选项）。
- **触发→后果**：
  - `WithSigningAlg("HS999")` 构造成功，首次 `IssueToken` nil pointer panic（违反 J7）；
  - `WithSigningAlg("PS256")` 签出的 token 不在默认白名单，自签自验失败；
  - parser 未开 `WithExpirationRequired`，不带 `exp` 的 token 永久有效；
  - 无 aud 校验，同一密钥下签给别的服务（`aud=other-svc`）的 token 也被接受（J4：身份不完整应 fail-closed）。
- **证据**【已复现】：包内临时测试四种情况全部命中。`alg=none` 与密钥类型混淆已被 `WithValidMethods` 挡住（排除）。
- **建议修法**：构造期校验签名方法非 nil 且 ∈ 白名单；验签默认要求 `exp`；提供 `WithAudience`。

### AR-31【低】审计失败面与文档承诺不符

- `contrib/audit-gorm/store.go:90-93`：注释写"失败/panic 降级 fallback"，recover 后只打日志，mapper panic 的事件丢失。
- `contrib/audit-stream/stream.go:139-140`：`Close` 后 `Record` 的事件进入无人消费的 channel，静默丢失，也不走 fallback。
- **建议修法**：recover 分支补 `recordFallback`；`Close` 后 `Record` 直接走 fallback。

---

## 五、工程面与门禁

### AR-24【中】`_example/bald` 不在 lint/vuln 门禁内，lint 现为红

- **位置**：`scripts/list-modules.sh`（`-not -path "./_example*"`）；`.github/workflows/ci.yml` 的 example 作业只跑 `go test`。
- **触发→后果**：示例是用户照抄的模板，手写文件与测试的缺陷不会被 CI 拦住。
- **证据**【已复现】：`_example/bald` 下 `golangci-lint run ./...` rc=1，共 5 处——`user/cmd.go:21:17` errcheck（`defer p.Close()`），
  `internal/bootstrap/nacos_http_probe_test.go:29,37,41,58` 四处 unused。`govulncheck` 可达 0，vuln 当前只是未覆盖。
  risk-review 只提到本地 `task verify` 会另加 `_example`。
- **建议修法**：example 作业加 golangci-lint 与 govulncheck 两步（先 buf generate），修掉 5 处。

### 依赖与门禁统计

| 项 | 数字 |
|---|---|
| `go.mod` 总数 | 69（`list-modules.sh` 派生 68，只排除 `_example/bald`） |
| CI 覆盖 | test/lint/vuln 68；race 67（`race-skip` 去掉 `log/aliyun`）；example 只跑 test（三种 tag） |
| go directive | 69 个全为 1.27.1，无 toolchain 行；CI 用 1.27.4 |
| replace | 372 条，全部相对路径；0 条绝对路径、0 条悬挂（W-11 已修） |
| 依赖图 | DFS 无环，无越层依赖 |
| cli | 零 replace，`bconf v0.28.1` = 最新 tag |
| 版本 skew（MVS 取最高，不构成冲突） | 4 版本并存：x/sync、go-redis/v9（v9.7–v9.22）、otel（v1.44–1.47）、genproto/rpc、x/exp；跨度最大：gorm v1.25.12↔v1.31.2、modernc sqlite v1.23.1↔v1.56.0 |
| 示例一致性 | `_example/bald` 三种 tag 下 build/vet 通过；`bald gen verify` app 树 24 文件、域产物 5 文件一致 |

---

## 修复优先级建议

按"爆炸半径 × 修复成本"排序，每批可独立分支、独立合并：

| 批 | 条目 | 理由 |
|---|---|---|
| 1 | AR-4、AR-5、AR-2、AR-9、AR-11 | 进程崩溃 / 挂死 / 凭证泄漏 / 任意目录写入；每条改动都在 10 行量级 |
| 2 | AR-1、AR-25 | 停机序重排，涉及效应账本与 cleanup 槽位的职责切分，需要带停机序断言的回归测试 |
| 3 | AR-6、AR-7、AR-17、AR-18 | 服务发现与熔断的正确性，影响所有多下游服务；AR-7 需要 Registry 按服务隔离的接口调整 |
| 4 | AR-3、AR-12 | 配置两遍装载统一，与 AR-1 同属 appkit 装配流水线，建议同一人做 |
| 5 | AR-8、AR-13、AR-14、AR-15、AR-16、AR-23 | 默认值与契约对齐；AR-15/16 需要扩 proto 契约，走 `bald gen` 重派生 |
| 6 | AR-10、AR-22、AR-24 | CLI/门禁，AR-10 在第一个 v1 tag 之前修完即可 |
| 7 | 其余中低项 | 随相关模块的下一次改动顺手修；AR-30 并入 kafka/rabbitmq transport 统一 |

## 检查过但排除

- **已修复核**：C-1、C-2、C-3、C-5 修法完整；W-4（`parseTolerantArgs`）、W-6（tlsconf 二选一）、W-7（读锁快检）、W-8（默认已改 FullJitter，`retry/retry.go:167`）均已落地；W-11 悬挂 replace 已清。
- **已登记**：W-3 keywatch、§4.1 deregister 错误被吞、§4.4 启动取消后日志被吞、§6.2 流式链缺认证/授权/审计、§6.3 限流在认证之后、§6.4 CORS/Secure 默认关、§6.6 slog 脱敏缺口、§7.25/7.26 aliyun/tencent、§7.40 本地 pkg 配已发布 bootstrap 漂移、§3.2 multi-major、W-12 gin Timeout 内存、C-4 transport Stop 竞态。
- **设计如此/已文档化**：Dispose 后运行期挂载（`mount.go:68` 已拦）、服务端 tlsconf 忽略 `insecure_skip_verify`、统一 Timeout 遇已有 deadline 跳过、local SetNX 非原子、loadable 用首个调用者 ctx、sentinel Allow 只支持 QPS 规则、loki `flush_interval` 语义、resiliency 无 GetBody 不重试、metricshttp 默认 `:9091` 旁路语义、argo 30s 默认超时与显式 insecure 开关。
- **验证干净**：Stop 先于 Start 的 HTTPServer/sse/websocket/gRPC、Transporter 对象池复用、tokenbucket 数学、p2c EWMA CAS、redis 单机 MGET、berrors grpc/http 映射一致、nacos namespace、health checker 超时、casbin 缓存与 Reload（无 fail-open、空策略默认拒绝）、JWT `alg=none` 与密钥类型混淆、codegen 输出确定性（map 遍历均排序）、manifest prune 越界防护、release 已存在 tag 检测、门禁循环"跑完再汇总"与空模块派生判红、`|| true` 仅用于诊断 grep。
- **CI `./cli` Linux 判红**：交接记录推断"绿→红窗口内 cli 唯一变化是 fsatomic"不成立——同批推送还有 `0b36b04d`。
  真实根因是 `TestWriteGuarded_StatErrors` 的 `!os.IsNotExist` 断言：父路径被普通文件占用时，Linux `Stat` 返回 ENOTDIR，
  不映射为 ErrNotExist；Windows 返回 `ERROR_PATH_NOT_FOUND`，会映射。已由 `d7edaafc`（merge `984a9655`）修复，main 上已包含，待推送后 CI 复核。
