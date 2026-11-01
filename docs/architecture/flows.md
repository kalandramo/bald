# bald 流程视图（调用 / 消息 / 生命周期）

> 产出方式：`flow-visualizer` + `graphviz`（源文件 `flows.dot`）。
> 证据日期：2026-11-01，`main` @ `ca981b1`。
> 核对方式：本文每条 `路径:行号` 都由脚本从制品中抽出后逐条打开源文件比对；`.dot` 未渲染（本机无 graphviz），只做了括号配平与「边两端均为已声明节点」的结构检查。
> 读法：每步标注 **同步/异步** 与 **协议**；`UNKNOWN` 表示代码中未找到证据，不代表不存在。

---

## ① 入站 HTTP 请求（gin 路径）

| # | 步骤 | 证据 | sync/协议 |
|---|---|---|---|
| 1 | `HTTPServer.Start`：`net.Listen` + `Serve/ServeTLS`，在 appkit errgroup goroutine 中阻塞 | `transport/http/http_server.go:62-89` | sync / HTTP/1.1 |
| 2 | 服务构造：`stdServerProvider.Build` 取 `Decl.HTTPHandler`（业务用 `appkit.WithHTTP` 传入的 `*gin.Engine`），按需包健康探针，交给 `bootstrap.HttpServerProvider` | `pkg/appkit/standard.go:204-229`、`pkg/appkit/health.go:60-80`、`bootstrap/server.go:154-173`、`_example/bald/internal/apiserver/apiserver.go:15` | in-process |
| 3 | 中间件链（**顺序由 bundle 固化，外→内**）：Recovery → RequestID → Tracing → Logging → CORS → Secure → Authn → Audit → Authz → RateLimit → Timeout | `pkg/middleware/bundle/bundle.go:171-211` | in-process |
| 4 | 路由 → 泛型 handler：绑定 → 校验 → `handler(c.Request.Context(), &req)` | `transport/web/handler.go:37-51` | sync |
| 5 | 响应：`WriteResponse`/`ErrorResponse`，错误经 `berrors` 映射为 HTTP code（兜底 INTERNAL） | `transport/web/handler.go:50`、`transport/web/response.go:56-93` | sync |

**链序的两个刻意设计**（不是 bug）：
- Tracing 在 Logging **之外**：两者并存时 logging 起的 span 是 tracing span 的子（`pkg/middleware/bundle/bundle.go:175-176`）。
- RateLimit 在 Authz **之后**、Timeout 之前：为了能看到认证后的 subject（为按用户限流留位），**代价是不保护认证/授权层自身**（`bundle.go:199-204`）。要保护认证层需业务自己把 `ginmw.RateLimit` 挂到链首。

**真实接线 vs 存在但未接线**（本仓库最重要的架构事实之一）：

| 能力 | 状态 | 证据 |
|---|---|---|
| recovery / requestid | **REAL**，恒挂（链的前两层） | `pkg/middleware/bundle/bundle.go:177` |
| logging / tracing | **REAL**，但开关不同：logging 由 `New()` 默认开、可 `NoLogging()` 关；tracing 默认关、须显式 `Tracing()` | `pkg/middleware/bundle/bundle.go:159,178-183`、`pkg/middleware/gin/tracing.go:26`、`pkg/middleware/gin/observability.go:154` |
| ratelimit | **REAL**，契约驱动，后端来自 `ratelimit/tokenbucket` | `pkg/middleware/gin/ratelimit.go:81,104`、`pkg/middleware/bundle/middleware.go:48` |
| authn / authz / audit / metrics 发射 | **REAL**（metrics 随 audit 事件发射） | `pkg/middleware/gin/audit.go:138-148` |
| Authn/Authz/CORS/Secure/RateLimit/Timeout | **条件装配**：未注入即整层消失，无 default-deny、无启动告警 | `pkg/middleware/bundle/bundle.go:170`（注释）+ `:184-209`（`if` 追加） |
| `retry/` | **未接线**：全仓零 importer，唯一提及是注释 `ratelimit/bbr/bbr.go:68` | 全仓 grep 复核 |
| `circuitbreaker/` | **未接线**：只被自己的 4 个后端 import | `circuitbreaker/{hystrix,vegas,sres,sentinel}` |
| health | 接在**启动/注册**期，不在每请求路径 | `pkg/appkit/health.go:60-80` |

### gRPC 路径的差异

同一 bundle，形态换成拦截器：`Error → Recovery → RequestID → Observability → Authn → Audit → Authz`（`pkg/middleware/bundle/bundle.go:224-241`）。
- `ErrorInterceptor` 必须最外层，`RecoveryInterceptor` 紧随其后把 panic 转成 `*berrors.Internal` 再由外层收口为 gRPC status（`pkg/middleware/bundle/bundle.go:217-219`）。
- **gRPC 链上没有 RateLimit、没有 Timeout、没有 CORS/Secure** —— 与 gin 链不对称。
- 链接入方式：`appkit.WithGRPC`（`pkg/appkit/bootstrap.go:77`）→ `bootstrap.WithGRPCUnary`（`bootstrap/server.go:51`）→ `pkg/appkit/standard.go:176-189`；服务端构造 `transport/grpc/grpc_server.go:51-63,80-91`。
- **TLS 不对称**：HTTP 按契约 `server.http.tls` 启 TLS（`transport/http/http_server.go:72-84`），gRPC 不看 `server.grpc.tls`——`GrpcServerProvider` 只读 `GetReflection()`（`bootstrap/server.go:100-126`），生成物硬编码 `grpc.Creds(insecure.NewCredentials())`（`internal/codegen/appspec_tmpl_app.go:288`）。详见 `risk-review.md` §6.5。

---

## ② 生命周期：`appkit.Run` / `stopAll`

启动（`pkg/appkit/appkit.go:561-703`）：

1. `loadConfig` → `Resolve` fail-fast（`pkg/appkit/appkit.go:574-598`）
2. `beforeStart` hooks（`pkg/appkit/appkit.go:604`）
3. components 按序启动（`pkg/appkit/appkit.go:614`）
4. **errgroup 并发 Start 所有 `transport.Server`**（`pkg/appkit/appkit.go:624-630`），任一失败级联取消
5. `waitForEndpoints`：轮询 `Endpoint()` 直到 `:0` 解析完成，5s 上界（`pkg/appkit/appkit.go:809-829`）
6. `register` 到发现服务（`pkg/appkit/appkit.go:643`、`:840-846`）
7. `afterStart` → reconcilers → 关闭 `startedCh`（`pkg/appkit/appkit.go:655-672`）
8. `signal.Notify(SIGINT, SIGTERM)`，select 等待信号或 ctx（`pkg/appkit/appkit.go:675-684`）

停机（`stopAll`，`pkg/appkit/appkit.go:715-750`）：**deregister 先于 stopAll**（`pkg/appkit/appkit.go:686-690`），随后五阶段
`undo effects → BeforeStop → 并发 Stop（共享超时）→ AfterStop → components Dispose 反序`。
- gRPC：`GracefulStop` + 超时兜底（`transport/grpc/grpc_server.go:94-107`）
- HTTP：`Shutdown`（`transport/http/http_server.go:92-94`）
- `runHook` 会 recover panic（`pkg/appkit/appkit.go:753-761`）

健壮性上这是全仓最成熟的一块（CAS 重入保护、逐阶段独立超时）。两处小裂缝见 `risk-review.md` §4：`_ = a.deregister(...)` 吞错（`pkg/appkit/appkit.go:690`）、5s 端点上界与 hook 超时是不可配常量（`pkg/appkit/appkit.go:812`、`pkg/appkit/types.go:9`）。

---

## ③ 出站调用与服务发现（生产侧半环）

| 环节 | 状态 | 证据 |
|---|---|---|
| 注册 | **REAL** | `appkit.register`（`pkg/appkit/appkit.go:840`）→ `registry.Registrar` 契约（`registry/registry.go:62-67`）→ etcd：Validate → lease Grant → Put `<ns>/<name>/<id>` → 后台 `heartBeat`（`registry/etcd/registry.go:108-160`，goroutine 在 `:157`） |
| 注销 | **REAL** | `registry/etcd/registry.go:163-181`（删 key、取消心跳、关租约） |
| 发现 | **未消费** | `registry.Discovery`（`registry/registry.go:70-75`）+ `Watch`（`registry/etcd/registry.go:212`）有实现，但 `GetService` 的调用点只在 registry 后端自身与测试里。**仓内没有 gRPC resolver / HTTP client 消费它。** |
| 实际服务间调用 | 配置地址直连 | gateway 拨号 `s.backend.GetAddr()`（`transport/gateway/gateway_server.go:72`），地址取自契约 `Grpc.addr`（`bootstrap/server.go:159-165`） |

> 结论：**bald 目前只解决「被找到」，不解决「找到别人」。** 置信度 medium——外部消费方（bald-admin）是否自研了解析层，本次未核验。

---

## ④ 消息流（以 broker/kafka 为例）

**发布**（sync）：`Broker.Publish`（`broker/broker.go:29`）→ `kafkaBroker.Publish`（`broker/kafka/kafka.go:326-334`，先过发布中间件链 `:329-331` / `broker/publish.go:12`）→ `internalPublish`（`broker/kafka/kafka.go:337`）→ `broker.Marshal(codec, msg.Body)`（`broker/encoding.go:40`；无 codec 时 fail-fast `errNoCodec` `broker/encoding.go:34`）→ `publishMultipleWriter`（`broker/kafka/kafka.go:354`）→ `writer.WriteMessages`（`broker/kafka/kafka.go:398`）；缓存 writer 失败则重建 + `retriesCount` 重试（`broker/kafka/kafka.go:419-428`）。

**订阅**（async）：`kafkaBroker.Subscribe`（`broker/kafka/kafka.go:524-639`）构造 ReaderConfig、把 handler 包进 subscriber 中间件链（`:619-621`）、`go sub.run()`（`:632-633`）→ `processSingleMessage`（`broker/kafka/subscriber.go:221-258`）阻塞 `FetchMessage`，出错走抖动指数退避（`:312-331`），致命错误按规则退出（`:334-355`）→ `handleMessage`（`:268-307`）：binder 分配类型化 body → `broker.Unmarshal`（`:284`）→ `newPublication` → `s.handler(ctx, pub)`（`:294`）。类型化入口是泛型 `broker.Subscribe[T]`（`broker/broker.go:36-70`）。

**ACK**：`pub.Ack()` → `reader.CommitMessages`（`broker/kafka/publication.go:48-53`），`AutoAck` 默认 true（`broker/kafka/kafka.go:532`）。
**UNKNOWN**：handler 返回错误只是跳过 ack（`broker/kafka/subscriber.go:294-297`），代码中**未发现重投递或死信队列**。
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
| 暴露 | 独立端口 `:9091`（`observability/metricshttp/metricshttp.go:36-53`），由 contrib provider 的 Build 启动（`contrib/observability-otlp/contract/contract.go:73-85,91-111`），以 `appkit.MetricsExposure` 返回（`pkg/appkit/observability.go:44-55`） | 停机顺序「先停暴露 → 再 flush」由引擎保证（`metricshttp.go:55-56` 注释、`pkg/appkit/observability.go:19-20`） |

---

## ⑦ WebRTC：不是请求/响应，是会话/推送

与其它 transport 的模型差异明显，值得单列：

1. **信令 = HTTP POST `/signal`**（`transport/webrtc/server.go:123` mux；`signalHandler` `:350-496`）：JSON `signalRequest{Offer|Candidate}`（`transport/webrtc/signaling.go:12-17`）→ `SetRemoteDescription`（`transport/webrtc/server.go:460`）→ `CreateAnswer`（`:467`）→ 回 Answer。ICE candidate 通过 `signalSessions` 注册表应用到 DataChannel 打开之前的会话（`transport/webrtc/server.go:53`、`handleICECandidate` `:668-705`）。**同步 HTTP，一次性。**
2. **数据面 = DTLS/SCTP DataChannel**（异步、带外）：`pc.OnDataChannel`（`transport/webrtc/server.go:433`）→ `session.BindDataChannel`（`transport/webrtc/server.go:441`）→ `transport/webrtc/session.go:88,97,105` 的 `OnOpen/OnMessage` → `Server.handleSocketRawData`（`transport/webrtc/server.go:327,335`）→ `defaultUnmarshalNetPacket`（`:285`，用 `encoding.Codec`）→ 按消息类型查 handler 注册表（`RegisterMessageHandler` `:163`）。
3. **服务端主动下发**：`SendMessage`/`Broadcast` 按 session id（`transport/webrtc/server.go:254,264`），**无请求关联**。SFU 媒体路由：`SubscribeToPublisher`（`:734`）+ 重协商 `sendRenegotiation`（`:814`）。
4. 生命周期仍守 `transport.Server`：`Start`（`transport/webrtc/server.go:559`）、`Stop`（`transport/webrtc/server.go:602`）。

> 注意：WebRTC 有 15 个非测试文件与共享会话表，却**不在 CI race 作业的模块清单里**，详见 `risk-review.md` §1.1。

---

## 未覆盖的流程

| 项 | 原因 |
|---|---|
| `transport/mcp` + `cobramcp` 的工具调用回合 | 本次只确认其依赖形态（包装 `mark3labs/mcp-go`，`transport/mcp/server.go:62,106,186`），未追完整调用流 |
| `workflow/argo`、`ai/*`、`oss/*`、`transport/graphql` | 仓内**零消费者**（见 `risk-review.md` §2），无真实流程可追 |
| 变体（Variant）effects 在 plan 期的合并细节 | 属装配语义而非运行流，见 `docs/devel/zh-CN/Bald 声明式装配设计.md` §5/§8.3 |
| bald-admin 端到端业务流 | 在另一仓库，本次范围外 |
