# bald 流程视图（调用 / 消息 / 生命周期）

> 产出方式：`flow-visualizer` + `graphviz`（源文件 `flows.dot`）。
> 证据日期：2026-11-01，`main` @ `ca981b1`。
> 核对方式：本文每条 `路径:行号` 都由脚本从制品中抽出后逐条打开源文件比对；`flows.dot` 已用 `dot -Tsvg` 真实渲染校验（graphviz 16.1.0，退出码 0、stderr 为空），并做过「边两端均为已声明节点」的结构检查。
> 读法：每步标注 **同步/异步** 与 **协议**；`UNKNOWN` 表示代码中未找到证据，不代表不存在。

---

## ① 入站 HTTP 请求（gin 路径）

| # | 步骤 | 证据 | sync/协议 |
|---|---|---|---|
| 1 | `HTTPServer.Start`：`net.Listen` + `Serve/ServeTLS`，在 appkit errgroup goroutine 中阻塞 | `transport/http/http_server.go:62-89` | sync / HTTP/1.1 |
| 2 | 服务构造：`stdHTTPProvider.Build` 取 `Decl.HTTPHandler`，按需包健康探针，交给 `bootstrap.HttpServerProvider`。`HTTPHandler` 有两条来源：业务直接用 `appkit.WithHTTP` 传一个建好的 handler；或声明 `appkit.WithHTTPContract(fn)`，由 `resolveContractFaces` 在契约装载后把 `server.http.middleware` 段交给 `fn`，产出的 handler 写回同一字段（域接线与 server 构造看到的形态一致） | `pkg/appkit/standard.go` 的 `stdHTTPProvider.Build`、`pkg/appkit/contract_faces.go` 的 `resolveContractFaces`、`pkg/appkit/health.go:72-80`、`bootstrap/server.go:137-149`、`_example/bald/internal/apiserver/apiserver.go` 的 `NewRouter` | in-process |
| 3 | 中间件链（**顺序由 bundle 固化，外→内**）：Recovery → RequestID → Tracing → Logging → CORS → Secure → Authn → Audit → Authz → RateLimit → CircuitBreaker → Timeout | `pkg/middleware/bundle/bundle.go` 的 `Gin()` | in-process |
| 4 | 路由 → 泛型 handler：绑定 → 校验 → `handler(c.Request.Context(), &req)` | `transport/web/handler.go:37-51` | sync |
| 5 | 响应：`WriteResponse`/`ErrorResponse`，错误经 `berrors` 映射为 HTTP code（兜底 INTERNAL） | `transport/web/handler.go:50`、`transport/web/response.go:56-93` | sync |

**链序的三个刻意设计**（不是 bug）：
- Tracing 在 Logging **之外**：两者并存时 logging 起的 span 是 tracing span 的子（`pkg/middleware/bundle/bundle.go` 的 `Gin()`）。
- RateLimit 在 Authz **之后**、CircuitBreaker/Timeout 之前：为了能看到认证后的 subject（为按用户限流留位），**代价是不保护认证/授权层自身**（同上，`Gin()` 内的追加序）。要保护认证层需业务自己把 `ginmw.RateLimit` 挂到链首。
- CircuitBreaker 夹在 RateLimit 与 Timeout 之间，三条判据缺一不可：更外（认证之前）匿名流量就能把失败率打满、替所有人开窗；更内（限流之前）被限流拒掉的请求会先进熔断，两层拒绝互相喂养；更内（超时之内）Timeout 写出的 503 就在它下面，熔断只看见「handler 都成功返回」——而「handler 总是超时」正是熔断该抓的形状。

**真实接线 vs 存在但未接线**（本仓库最重要的架构事实之一）：

| 能力 | 状态 | 证据 |
|---|---|---|
| recovery / requestid | **REAL**，恒挂（链的前两层） | `pkg/middleware/bundle/bundle.go` 的 `Gin()` |
| logging / tracing | **REAL**，但开关不同：logging 由 `New()` 默认开、可 `NoLogging()` 关；tracing 默认关、须显式 `Tracing()` | `pkg/middleware/bundle/bundle.go` 的 `New()`/`Gin()`、`pkg/middleware/gin/tracing.go` 的 `Tracing`、`pkg/middleware/gin/observability.go` |
| ratelimit | **REAL**，契约驱动到链：`server.{http,grpc}.middleware.rate_limit` 经 `bundle.FromMiddleware`/`FromGRPCMiddleware` 出 Option，两座桥由生成脚手架在契约钩子里调用（`_example/bald/internal/bootstrap/middleware_e2e_test.go` 从 YAML 断到 429 / `RESOURCE_EXHAUSTED`）；后端来自 `ratelimit/tokenbucket` | `pkg/middleware/gin/ratelimit.go` 的 `NewRateLimit`、`pkg/middleware/bundle/middleware.go` 的 `RateLimit`、`pkg/middleware/bundle` 的 `FromMiddleware`/`FromGRPCMiddleware` |
| circuitbreaker | **REAL**，契约驱动到链（hystrix 一个实现）：`server.{http,grpc}.middleware.circuit_breaker` 经 `bundle.CircuitBreakerFromMiddleware`/`GRPCCircuitBreakerFromMiddleware` 出 Option，缺段即不挂这一层（opt-in）；HTTP 拒 503、gRPC 拒 `UNAVAILABLE`+`Reason=CIRCUIT_OPEN`；**stream 面刻意不挂**（一条流的「失败」没有一致答案）。`vegas`/`sres`/`sentinel` 三个后端没有契约段，换算法经 `bundle.CircuitBreaker(...)` 代码级注入 | `pkg/middleware/gin/circuitbreaker.go`、`pkg/middleware/grpc/circuitbreaker.go`、`pkg/middleware/bundle/circuitbreaker.go`、`_example/bald/internal/bootstrap/middleware_e2e_test.go`（4 条端到端） |
| authn / authz / audit / metrics 发射 | **REAL**（metrics 随 audit 事件发射） | `pkg/middleware/gin/audit.go:138-148` |
| Authn/Authz/CORS/Secure/RateLimit/CircuitBreaker/Timeout | **条件装配**：未注入即整层消失（零开销是既定设计）。CORS/Secure/RateLimit/CircuitBreaker/Timeout 仍静默省略；**Authn 不再能靠疏漏开放**——`Authn(…)` 与 `Public()` 二者必居其一，否则 `Bundle.Verify()` 报 `AUTHN_UNDECLARED`、出链点告警一次，生成脚手架显式写 `bundle.Public()` | `pkg/middleware/bundle/bundle.go` 的 `Verify`/`Gin()`/`GRPCInterceptors()`（风险 §6.1） |
| `retry/` | **原语已接、循环未接**：`broker/kafka` 的订阅退避与 `registry/etcd` 的重注册退避消费 `ExponentialBackoff` + `EqualJitter`/`FullJitter`；etcd 那条曲线与抖动能由契约段 `registry.etcd.backoff` 改写（`registry/etcd/contract` 的 `retreat()` → `etcd.WithRetreat`）；`Retrier.Do` 在仓内零调用方（`ratelimit/bbr/bbr.go` 的那处提及是注释） | `grep -rl '"github.com/kalandramo/bald/retry"' --include='*.go' .` 命中 5 个文件：`broker/kafka/subscriber.go`、`registry/etcd/{options,registry}.go`、`registry/etcd/contract/contract.go`、`registry/etcd/retreat_policy_test.go` |
| health | 接在**启动/注册**期，不在每请求路径 | `pkg/appkit/health.go:60-80` |

### gRPC 路径的差异

同一 bundle，形态换成拦截器：unary 链 `Error → Recovery → RequestID → Observability → Authn → Audit → Authz → RateLimit → CircuitBreaker → Timeout`（`pkg/middleware/bundle/bundle.go` 的 `GRPCInterceptors()`）；stream 链 `Error → RateLimit`，只在挂了限流时产出（`GRPCStreamInterceptors()`，由 `GRPCChain()` 追加）。
- `ErrorInterceptor` 必须最外层，`RecoveryInterceptor` 紧随其后把 panic 转成 `*berrors.Internal` 再由外层收口为 gRPC status（`GRPCInterceptors()` 的链首两层）。stream 链的首层同样是 `ErrorStreamInterceptor`——少了它，限流拒绝的 `*berrors.Error` 会被 grpc-go 兜底成空 `Unknown`。
- **RateLimit/CircuitBreaker/Timeout 与 gin 同源同序，但覆盖面不同**：`bundle.RateLimit` 的同一个 limiter 实例同时喂 gin 与 gRPC（unary 逐调用、stream 逐建流），`bundle.CircuitBreaker` 与 `bundle.Timeout` 只作用于 gin 与 gRPC unary——stream 面不挂默认超时（否则 unary 的 `default_timeout_ms` 会静默变成「流的最大存活时长」），也不挂熔断（一条流的「失败」没有一致答案：按建流算则开窗与真实退化无关，按流终态算则一条长流长期占住半开探针、把其它流全拒）。gRPC 链仍没有 CORS/Secure（HTTP 专属概念）。两面的超时形态不对称，实测口径见《Bald 中间件设计》与 §6.2 的 e2e 表：gin 的 Timeout **提前返回 503**（不等 handler，响应头即刻 Flush），gRPC 的 Timeout **只给 handler 一个会到期的 ctx**（绝不提前返回，否则 panic 绕过 `RecoveryInterceptor`）——所以 gRPC 侧的超时断言依赖 handler 尊重 ctx，示例的 `sayHello` 因此显式 `select` 在 `<-ctx.Done()` 上。熔断的拒绝码同理按面成形：gin 可配状态码（默认 503），gRPC 固定 `UNAVAILABLE`。
- 链接入方式：`appkit.WithGRPC(register)` 声明服务注册；服务端选项有两条登记口——`appkit.WithGRPCOptions(fn)` 传运行期求值的工厂，`appkit.WithGRPCOptionsContract(fn)` 传按契约 `server.grpc.middleware` 段构造的钩子（`pkg/appkit/bootstrap.go`）。后者由 `resolveContractFaces` 在装配期求值一次，产出的选项包成工厂**并入同一条 `GRPCOptionFuncs` 通道**，之后与 `WithGRPCOptions` 走完全相同的路：阶段 B 逐工厂累加进 `bootstrap.WithGRPCUnaryFunc`（`pkg/appkit/standard.go`）→ `applyOptions` 按登记序串成一条 `[]grpc.ServerOption`（`bootstrap/server.go`）→ `transport/grpc/grpc_server.go` 构造 server。不再有第二条注入路径（原 `WithGRPCUnary` 值切片与 `mergeUnary` 已删）。
- **TLS 双传输对称**：HTTP 与 gRPC 都读各自的契约段（`server.http.tls` / `server.grpc.tls`）经同一 `bconf/tlsconf.Server`（`bconf/tlsconf/tlsconf.go:26`）构造——段缺失即明文，段给 `ca` 即强制校验客户端证书。gRPC 侧额外补 `NextProtos=["h2"]`（`transport/grpc/grpc_server.go:90-97`，grpc-go ≥1.67 强制 ALPN）。网关出站按 `server.grpc.tls` 选 `credentials.NewTLS` 或明文（`transport/gateway/gateway_server.go:70-80`）。

---

## ② 生命周期：`appkit.Run` / `stopAll`

启动（`pkg/appkit/appkit.go:561-703`）：

1. `loadConfig` → `Resolve` fail-fast（`pkg/appkit/appkit.go`）
2. `beforeStart` 钩子
3. `startComponents` 按序启动进程内组件
4. **errgroup 并发 `Start` 所有 `transport.Server`**（`Run` 里的 `g.Go`），任一失败级联取消
5. `waitForEndpoints`：轮询 `Endpoint()` 直到 `:0` 解析完成；上界在调用方 ctx 带截止期时按它计，否则取阶段预算 `phaseBudgetFor(stopTimeout)`
6. `register` 到发现服务（`buildInstance` 只收「可注册」端点，进程内组件的 `cron://scheduler` 这类不参与）
7. `afterStart` 钩子 → `runReconcilers` → 关闭 `startedCh`
8. `signal.Notify(SIGINT, SIGTERM)`，`select` 等待 gctx 或信号

停机（`stopAll`）：**deregister 先于 stopAll**，随后五阶段
`undo effects → BeforeStop → 并发 Stop（各自独立预算）→ AfterStop → components Dispose 反序`。
- gRPC：`GracefulStop` + 超时兜底（`transport/grpc`）
- HTTP：`Shutdown`（`transport/http`）
- `runHook` 会 recover panic（`pkg/appkit/appkit.go:753-761`）

健壮性上这是全仓最成熟的一块（CAS 重入保护、逐阶段独立预算派生）。曾经的裂缝 `_ = a.deregister(...)` 吞错与「5s 端点上界/hook 超时不可配」均已修：反注册失败经 `logDeregister` 上报（`pkg/appkit/appkit.go`），端点等待与钩子预算统一由 `phaseBudgetFor`（`pkg/appkit/types.go:11`）从 `app.stop_timeout` 派生，调用方 ctx 带截止期时以它为准（`waitForEndpoints`）。启动窗口内「调用方取消」与「某个 server 启动崩溃」也不再共用一条 `wait for endpoints canceled` 文案：`startupOutcome` 一条判据（调用方已取消**且**这一步的错误就是这条取消本身 → 正常停机退出 `nil`）挂在全部五处启动分支上——`beforeStart` 钩子、组件顺序启动、端点等待、服务注册、afterStart 钩子；errgroup 之后的三处先取 servers 的非取消启动错误，崩溃优先返回真实错误。见 `risk-review.md` §4.4。

---

## ③ 出站调用与服务发现

| 环节 | 状态 | 证据 |
|---|---|---|
| 注册 | **REAL** | `appkit.register`（`pkg/appkit/appkit.go:863`）→ `registry.Registrar` 契约（`registry/registry.go:62-67`）→ etcd：Validate → lease Grant → Put `<ns>/<name>/<id>` → 后台 `heartBeat`（`registry/etcd/registry.go:109-161`，goroutine 在 `:158`；断链重注册的等待见 `retreatPolicy.delay`，默认曲线 1s 起翻倍、截到 5min 叠 FullJitter，可被契约段 `registry.etcd.backoff` 改写） |
| 注销 | **REAL** | `registry/etcd/registry.go:164-187`（删 key、取消心跳、关租约） |
| 发现 | **REAL（gateway 出站拨号）** | 地址以 `discovery://` 开头时装上 resolver（`GatewayServer.Start` → `grpc.WithResolvers(newDiscoveryBuilder(d))`，`transport/gateway/gateway_server.go:168`）→ `Build` 内 `GetService` 首次快照 + `Watch` 持续订阅（`transport/gateway/discovery.go:63,64`）；解析出的端点先归一化——`grpc://` 剥前缀、`http(s)://` 等其它面丢弃（`grpcAddressOf`，bald 注册的端点本来就带 scheme）。快照为空或快照报错都**只上报错误、不结束订阅**，后端注册后由推送自动接住 |
| 发现的接线方式 | 配置驱动，消费方零代码 | `server.gateway.backend_grpc_addr` 写成 `discovery:///<服务名>` 即生效：`AppProvider.Build` 按当次 `ApplyScope` 注入惰性来源（`transport/gateway/contract/provider.go:84`），来源从 `App.Registrar()` 断言出 `registry.Discovery`（`discoveryFromScope`）；写 `host:port` 仍走原生 dns。服务名要按后端的真实命名写：内存/etcd 用 `ServiceInstance.Name`，**nacos 用 `<名>.<scheme>`**，故那里得写 `discovery:///<服务名>.grpc`。端到端判据：`discovery_e2e_test.go`（真应用注册 `grpc://` 端点 → 转码面 REST 拿到 gRPC 响应）+ `discovery_registry_e2e_test.go`（**etcd、nacos 各一轮真机**：注册前 503 → 注册后 200 → 注销后仍 200 → 后端下线 503） |
| 未接线的面 | HTTP 主面、业务自拨 | `gatewaycontract.HTTPProvider` 不自动派生来源（它的上游是本进程 gRPC 面自己的绑定地址，填 `discovery:///` 属配置错误）；`transport/grpc` 客户端侧、`transport/http` 出站仍无 resolver，需要时业务自行 `gateway.WithDiscovery` |

> 结论：**bald 现在既解决「被找到」，也让 gateway 能「找到别人」**——但消费面只有 grpc-gateway 出站一处，不是全局服务间调用层。置信度 high（判据：`GetService` 的非测试调用点现在只有 `transport/gateway/discovery.go` 的 `discoveryBuilder.Build` 与 registry 后端自身；四个生产后端里 etcd、nacos 已真机跑通，consul、kubernetes 仍是代码阅读）。多副本默认 `pick_first` 故障转移，非负载均衡。

---

## ④ 消息流（以 broker/kafka 为例）

**发布**（sync）：`Broker.Publish`（`broker/broker.go:29`）→ `kafkaBroker.Publish`（`broker/kafka/kafka.go:326-334`，先过发布中间件链 `:329-331` / `broker/publish.go:12`）→ `internalPublish`（`broker/kafka/kafka.go:337`）→ `broker.Marshal(codec, msg.Body)`（`broker/encoding.go:40`；无 codec 时 fail-fast `errNoCodec` `broker/encoding.go:34`）→ `publishMultipleWriter`（`broker/kafka/kafka.go:362`）→ `writer.WriteMessages`（`broker/kafka/kafka.go:406`）；缓存 writer 失败则关闭并摘除、换新 writer 按 `retriesCount` 重建（`broker/kafka/kafka.go:415-437`），Close 自身的错误只记日志、不覆盖写失败。首次写入的瞬时错误按 `shouldRetryFreshWriter` 原地重试一次（`broker/kafka/kafka.go:356-359`）。

**订阅**（async）：`kafkaBroker.Subscribe`（`broker/kafka/kafka.go:527-642`）构造 ReaderConfig、把 handler 包进 subscriber 中间件链（`:622-624`）、`go sub.run()`（`:635-636`）→ `processSingleMessage`（`broker/kafka/subscriber.go:225-263`）阻塞 `FetchMessage`，出错走抖动指数退避（`:321-337`，曲线取 `retry.ExponentialBackoff` + `retry.EqualJitter`），致命错误按规则退出并 close 订阅者（`:340-361`）→ `handleMessage`（`:273-312`）：binder 分配类型化 body → `broker.Unmarshal`（`:289`）→ `newPublication` → `s.handler(ctx, pub)`（`:299`）。类型化入口是泛型 `broker.Subscribe[T]`（`broker/broker.go:36-70`）。

**ACK**：`pub.Ack()` → `reader.CommitMessages`（`broker/kafka/publication.go:48-53`），`AutoAck` 默认 true（`broker/kafka/kafka.go:535`）。
**UNKNOWN**：handler 返回错误只是跳过 ack（`broker/kafka/subscriber.go:299-302`），代码中**未发现重投递或死信队列**。
**transport 侧**：`transport/kafka` 把队列包成 `transport.Server`，`RegisterSubscriber` 以 per-topic queue name 作为消费组（`transport/kafka/server.go:60-111,118`）。

---

## ⑤ 缓存流

- 契约：`cache.Cache`（`cache/cache.go:28-69`）。
- **redis 后端**（`cache/redis/redis.go`）：key 前缀逐操作施加 `c.prefix(key)`（`:166`，用于 `:49,61,68,77,99`）；`Get`:48 / `Set`+TTL:60 / `SetNX`:67（分布式锁原语）/ `GetMulti` 用原生 MGET 单次往返:92-102。后端自身**无读穿、无失效机制**——失效即 `Delete`:76。
- **loadable = 读穿组合器**（不是后端）：`Get`（`cache/loadable/loadable.go:86-121`）后端命中直接返回 → `ErrNotFound` → `singleflight.Do(key, loader)`（`:103`）→ 尽力回填 `backend.Set`（`:116`，忽略错误）→ 返回前拷贝（`:120`）。后端错误会向上抛，除非 `WithDegradeOnError`（`:95-97`）；loader 报错则不缓存、下次重读。
- **UNKNOWN**：不存在 write-through 路径。

---

## ⑥ 可观测性横切

| 面 | 挂在哪 | 证据 |
|---|---|---|
| span | **中间件层，不是 transport 层**：gin `tracer.Start`（`pkg/middleware/gin/observability.go:154`，`otel.Tracer("bald/gin")` `:28`，SpanKindServer）；gRPC `UnaryObservability`/`StreamObservability`（`pkg/middleware/grpc/observability.go:92,196`） | 无全局 TracerProvider 时 otel 为 no-op（安全默认，`pkg/appkit/observability.go:23`） |
| metrics | 契约 `metrics.Metrics`（`metrics/metrics.go:15-19`）；**请求路径上的发射点在 Audit 中间件里**（`pkg/middleware/gin/audit.go:138-148` `emitMetricsSafely`，recorder 由 `bundle.Metrics` 注入） | 后端：`metrics/prometheus`（含 `promhttp.Handler()`）、`metrics/otel`（OTLP grpc/http）、`metrics/datadog`（statsd） |
| 暴露 | 独立端口 `:9091`（`pkg/metricshttp/metricshttp.go:36-53`），由 contrib provider 的 Build 启动（`contrib/observability-otlp/contract/contract.go:73-85,91-111`），以 `appkit.MetricsExposure` 返回（`pkg/appkit/observability.go:44-55`） | 停机顺序「先停暴露 → 再 flush」由引擎保证（`metricshttp.go:55-56` 注释、`pkg/appkit/observability.go:19-20`） |

---

## ⑦ WebRTC：不是请求/响应，是会话/推送

与其它 transport 的模型差异明显，值得单列：

1. **信令 = HTTP POST `/signal`**（`transport/webrtc/server.go:123` mux；`signalHandler` `:350-496`）：JSON `signalRequest{Offer|Candidate}`（`transport/webrtc/signaling.go:12-17`）→ `SetRemoteDescription`（`transport/webrtc/server.go:460`）→ `CreateAnswer`（`:467`）→ 回 Answer。ICE candidate 通过 `signalSessions` 注册表应用到 DataChannel 打开之前的会话（`transport/webrtc/server.go:53`、`handleICECandidate` `:668-705`）。**同步 HTTP，一次性。**
2. **数据面 = DTLS/SCTP DataChannel**（异步、带外）：`pc.OnDataChannel`（`transport/webrtc/server.go:433`）→ `session.BindDataChannel`（`transport/webrtc/server.go:441`）→ `transport/webrtc/session.go:88,97,105` 的 `OnOpen/OnMessage` → `Server.handleSocketRawData`（`transport/webrtc/server.go:327,335`）→ `defaultUnmarshalNetPacket`（`:285`，用 `encoding.Codec`）→ 按消息类型查 handler 注册表（`RegisterMessageHandler` `:163`）。
3. **服务端主动下发**：`SendMessage`/`Broadcast` 按 session id（`transport/webrtc/server.go:254,264`），**无请求关联**。SFU 媒体路由：`SubscribeToPublisher`（`:734`）+ 重协商 `sendRenegotiation`（`:814`）。
4. 生命周期仍守 `transport.Server`：`Start`（`transport/webrtc/server.go:559`）、`Stop`（`transport/webrtc/server.go:602`）。

> 注意：WebRTC 有 15 个非测试文件与共享会话表，是 race 门禁该覆盖的形态。CI 的 race 作业现已改成 `find` 派生的全量清单（`risk-review.md` §1.1 已修），这块并发在闸内。

---

## 未覆盖的流程

| 项 | 原因 |
|---|---|
| `transport/mcp` + `cobramcp` 的工具调用回合 | 本次只确认其依赖形态（包装 `mark3labs/mcp-go`，`transport/mcp/server.go:62,106,186`），未追完整调用流 |
| `workflow/argo`、`ai/*`、`oss/*`、`transport/graphql` | 仓内**零消费者**（见 `risk-review.md` §2），无真实流程可追 |
| 变体（Variant）effects 在 plan 期的合并细节 | 属装配语义而非运行流，见 `docs/devel/zh-CN/Bald 声明式装配设计.md` §5/§8.3 |
| bald-admin 端到端业务流 | 在另一仓库，本次范围外 |
