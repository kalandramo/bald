# Bald 健康检查装配设计：协议实现只管协议，探针由框架提供、业务组装

> Author(s): bald 团队
>
> Last updated: 2026-09-17
>
> Discussion at: 源码 `transport/{server.go,http,grpc,gateway}`、`health/`、`bootstrap/server.go`、`pkg/appkit/bootstrap.go`
>
> Status: Accepted（目标态；实现见「实现与过渡」清单）

## 摘要

健康检查与就绪从**协议实现**里整体退出：`transport/http` 不再注册 `/healthz` `/readyz`，`transport/grpc` 不再持有就绪轮询，`transport/gateway` 不再透传就绪回调，`transport.Server` 契约不变（仍是 `Start`/`Stop`/`Endpoint` 三个方法）。**唯一留在协议层的是 gRPC 的 `grpc.health.v1` 标准服务注册**——它是 gRPC 生态的标准服务，不是框架的私货；但它只在位、不判断，状态由外部推。

能力落点是两层：`health` module（零依赖，只提供零件：`Checker`/`Health` 聚合 + 双探针 `Handler` + `Readiness` 适配）与装配层（`bootstrap` 的 gRPC health 组合服务器、`appkit.WithHealth` 的一行默认装配）。业务有两条路：一行 `appkit.WithHealth(h)` 拿到"HTTP 双探针 + gRPC health 联动"，或完全手工把 `health.NewHandler(h)` 挂到自己的 gin/mux 上——两条路都不要求协议实现知道健康检查的存在。

**最重要的承诺：协议实现里没有健康检查，健康检查里没有协议依赖。** `transport` 根包与 `health` module 都保持零 require；`health` 不 import grpc（否则破坏"纯标准库"承诺），gRPC 的桥由 `bootstrap`/`appkit` 承担。

> 本文是健康检查域的第二篇设计文档：《Bald 健康检查设计》讲 `health` module 内部（三态、聚合、超时、出口），本文讲"探针与就绪归谁所有、在哪装配"。两篇合起来才是完整答案。

---

## 背景与动机

### 健康检查现在散在四层，谁也说不清边界

改动前的分布（数字为仪式性估算，用于衡量"协议实现里塞了多少非协议代码"）：

| 层 | 位置 | 内容 |
|---|---|---|
| 生命周期契约 | `transport/server.go` | `Start`/`Stop`/`Endpoint`，**本来就不含 health**（正确） |
| 就绪契约 | `transport/readiness.go` | `ReadinessFunc func(ctx) error` + `Ready(...)` 聚合 |
| 端点实现 | `transport/http/http_server.go` | 两个探针常量、`WithProbePaths`、内部 mux 注册、两个 handler（约 40 行） |
| gRPC 侧 | `transport/grpc/grpc_server.go` | `health.NewServer()` + `RegisterHealthServer` + 2s 轮询 `SetServingStatus` + 一个 goroutine 生命周期（约 60 行） |
| 健康检查域 | `health/` | `Checker`/`Health`/双出口 handler/五种 checker（零依赖，**全仓零消费者**） |

### 问题一：两套真相源，且两套都不完整

`transport/http` 与 `health` 各有一套探针实现：前者吐 `text/plain "ok"` 与 `err.Error()`，后者吐 JSON 明细 + 三态 + 全局超时 + 并发。聚合也是两套：`transport.Ready`（顺序 + `errors.Join`）与 `health.Health`（并发 + 超时 + 三态）。`transport.Ready` 至今**零生产消费者**（仅 2 个测试引用）。

`health` 则是另一种极端：tag `health/v0.1.0` 已发、能力齐备、**全仓零 import**。零件造好了，没人装。

### 问题二：框架在协议实现里偷偷注册路由，行为与注释相反

`transport/http` 把业务 handler 挂在内部 mux 的 `/`，再把自己的 `/healthz` `/readyz` 挂成精确路径。Go 1.22+ 的 `ServeMux` 里精确路径优先于 `/` 子树——于是**框架探针覆盖业务同名路由**，而代码注释写的是"业务优先（框架 probe 不覆盖）"，另一处还写着"业务重复注册会 panic"（不可能发生：框架用的是独立 mux）。注释描述的语义与实现的语义相反，这本身就是越权的证据：协议实现不该有"抢注业务路径"的能力。

### 问题三：就绪语义被翻译了三次

`ReadinessFunc`（transport）→ `health.PingFunc`（health）→ gRPC 的 `SetServingStatus`（推）。三者是同形语义的三份表达，桥接成本为零（`func(ctx) error` 一望即知可互换），却因为是三份而需要三份文档、三份测试、三处维护。

### 判别原则

- **协议实现只承诺协议形状**：监听地址、TLS、`Start`/`Stop`/`Endpoint`、服务注册。它不认识"健康""就绪"这类业务语义。
- **健康检查是可组合的出口能力**：由 `health` 提供零件，由装配层（或业务）决定挂在哪个路由、推到哪个协议、按什么频率。
- **框架提供默认装配，但不隐藏**：默认装配写在 `appkit`（可见、可覆盖、可绕过），而不是写死在协议构造函数里。

---

## 设计

### 归位总览

| 关注点 | 归位 | 说明 |
|---|---|---|
| 生命周期（Start/Stop/Endpoint） | `transport` | 不变 |
| gRPC 标准健康服务注册 | `transport/grpc` | 只注册不判断（协议标准） |
| 状态推送（就绪 → health 状态） | `bootstrap`（组合服务器） | 契约/框架装配，非协议实现 |
| gRPC reflection 注册 | `bootstrap`（按契约 `grpc.reflection`） | 从 transport 外移 |
| HTTP 双探针路由 | `appkit.WithHealth` 或业务 | 用 `ServeMux` 包住业务 handler |
| 探针/聚合/明细 | `health` module | 零依赖，已有能力 + `Readiness` 适配 |
| 就绪契约 | `health` | `transport.ReadinessFunc`/`Ready` 删除 |

### HTTP：构造函数回归"地址 + handler"

```go
// transport/http：只剩协议
func NewHTTPServer(cfg *bootstrapv1.Server_Http, handler http.Handler) *HTTPServer
```

同理 `NewGatewayServer(httpCfg, backend, register)`、`NewGRPCServer(cfg, srv)`、
`NewGRPCServerWithRegister(cfg, unary, register)`：**不再有 `opts`**——原
`HTTPServerOption`/`GRPCServerOption` 的唯二选项（探针路径、轮询间隔）都是健康
相关，随参数一起删除。

删掉：`DefaultHealthPath`/`DefaultReadyPath`、`WithProbePaths`、`healthPath`/`readyPath` 字段、`readiness` 字段与参数、`handleHealthz`/`handleReadyz`。`AttachHandler`（gateway 延迟挂载用）保留。

装配层的默认探针（`appkit`），本质就是"包一层"：

```go
func withProbes(business http.Handler, h *health.Health, live, ready string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(live, health.NewLivenessHandler())   // 恒 200
	mux.Handle(ready, health.NewHandler(h))         // Down → 503 + JSON 明细
	mux.Handle("/", business)                       // 业务路由兜底
	return mux
}
```

业务手工组装等价于在自己路由里加两行：

```go
router.GET("/healthz", gin.WrapH(health.NewLivenessHandler()))
router.GET("/readyz",  gin.WrapH(health.NewHandler(h)))
```

### gRPC：health 服务留在协议层，状态由外部推

`transport/grpc` 保留：

```go
hs := health.NewServer()
healthpb.RegisterHealthServer(s, hs)
```

删掉 `readiness` 参数、`WithReadinessPollInterval`、`pollReadiness`/`syncHealth`、`readinessCancel`，新增一个只读访问器把状态写入口交出去：

```go
// HealthServer 返回本服务注册的 gRPC 标准健康服务，
// 供装配层按业务就绪状态调用 SetServingStatus。
func (s *GRPCServer) HealthServer() *health.Server
```

推送由 `bootstrap` 的组合服务器承担（`bootstrap/health.go`）：

```go
type grpcHealthServer struct {
	*grpcserver.GRPCServer
	health   *health.Health
	interval time.Duration

	mu     sync.Mutex
	cancel context.CancelFunc
}

func (s *grpcHealthServer) Start(ctx context.Context) error {
	rctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	go s.poll(rctx)                  // 立即探一次 + 定时同步
	err := s.GRPCServer.Start(ctx)   // 阻塞直到 Serve 返回
	cancel()                         // Serve 结束（含 listen 失败）即收摊，不泄漏 goroutine
	return err
}
```

判据：`health.Check` 的 `StatusDown` → `NOT_SERVING`，否则 `SERVING`（与 HTTP `/readyz` 的 200/503 同源，两端对称）。

### reflection：从协议层外移到装配层（按契约）

`transport/grpc` 不再看 `cfg.GetReflection()`；`bootstrap.GrpcServerProvider` 在业务 register 之前按契约注册：

```go
register := func(s *grpc.Server) {
	if c.GetReflection() {
		reflection.Register(s)   // 契约 reflection=true 由框架注册，业务勿重复注册
	}
	if deps.register != nil {
		deps.register(s)
	}
}
```

契约字段 `Server.Grpc.reflection` **保留**（仍有实现消费，符合"契约只为已实现的东西承诺形状"）；变的是实现位置。

### health：补一个就绪适配

```go
// Readiness 把聚合结果适配为 func(ctx) error 形态：
// 全部 Up/Unknown 返回 nil，任一 Down 返回错误（含明细）。
func (h *Health) Readiness(ctx context.Context) error
```

这样 `health.Health` 可以喂给任何"返回 error"的位置，`PingFunc` 反向（`func → Checker`）也已有，两个方向都通。`health` 保持零 require。

### appkit：一行默认装配

```go
// WithHealth 装配健康检查：HTTP/gateway 双探针 + gRPC health 状态联动。
// 不声明则框架不挂任何探针（协议实现也不再兜底）。
func WithHealth(h *health.Health, opts ...HealthOption) BootstrapOption
```

`HealthOption`：`WithProbePaths(live, ready string)`（默认 `/healthz` `/readyz`）、`WithHealthPollInterval(d time.Duration)`（gRPC 侧，默认 2s）。

装配层另外导出两个零件，供手工装配（CLI 生成的 app、不用 `FromBootstrap` 的装配代码）复用：

```go
// appkit：在业务 handler 外层包一层探针 mux（live/ready 传空串用默认路径）。
func WithProbes(business http.Handler, h *health.Health, live, ready string) http.Handler

// bootstrap：把就绪状态推给 gRPC 标准健康服务，返回可直接交给 appkit.Servers 的 server。
func NewGRPCHealthServer(srv *grpcserver.GRPCServer, h *health.Health, interval time.Duration) transport.Server
```

装配动作三处：① `spec.httpHandler` 经 `withProbes` 包装后交给 `bootstrap.WithHTTPHandler`；② `spec.gatewayRegister` 的返回值同样包装后返回（网关模式复用同一函数）；③ gRPC 侧传 `bootstrap.WithGRPCHealth(h, interval)`。

### 业务的两条路

```go
// 路 A：一行装配（推荐）
appkit.WithHealth(h)

// 路 B：完全手工（不用 appkit 的探针装配，甚至不用 appkit）
mux.Handle("/readyz", health.NewHandler(h))
srv := httpserver.NewHTTPServer(cfg, mux)
```

---

## 理由与取舍

### 为什么探针不放进 health module？

`health` 的承诺是"零第三方依赖、纯标准库、实现 `http.Handler`"——它可以是任何服务形态的零件。若把"注册到 transport 内部 mux"塞进去，`health` 就得认识 transport（或反过来），零依赖承诺与"零件"定位同时破产。**零件与接线分开**：`health` 提供 handler，接线由装配层做。

### 为什么 gRPC health 服务不一起外移？

`grpc.health.v1` 是 gRPC 生态的标准服务（`google.golang.org/grpc/health` 官方包），注册它是"实现 gRPC 协议标配"的一部分，与 HTTP 侧那两个**没有任何标准**的 `/healthz` `/readyz` 性质不同。把标准服务注册留在协议层、把"判断就绪"移出去，既守住了"协议实现只做协议"，也没有把 gRPC 削成残缺实现。

### 为什么轮询放 bootstrap 而不是 health？

轮询要 import `grpc_health_v1` 才能 `SetServingStatus`，放 `health` 就破坏了它唯一的承诺（零第三方依赖）。`bootstrap` 已经依赖 grpc 与 transport，是天然宿主；`appkit` 的 gRPC 装配本来就经 `bootstrap.GrpcServerProvider`。

### 为什么默认装配放在 appkit 而不是协议层？

appkit 是"契约约定装配"层：可见（`WithHealth` 在 main.go 里）、可覆盖（`WithProbePaths`）、可绕过（业务自己挂）。协议层写死探针的代价已经显形——抢注业务路由、注释与实现相反、两套真相源。

### 被放弃的方案

| 方案 | 为什么不选 |
|---|---|
| 把 `transport/http` 的探针"提取"到 `health`（让 transport import health） | 传输叶子反向依赖健康域；响应体契约与 4 个测试要改；收益（JSON 明细）用包装就能拿到 |
| 保留探针在 transport，只加 `WithProbeHandlers` 可插拔 | 协议层仍持有"健康"概念，两套真相源不消失 |
| 只修注释、不动结构 | 越权注册路由的行为仍在，注释与实现的矛盾是结构性的 |
| 保留 `transport.ReadinessFunc` 作兼容别名 | Go 无包级转发别名，只能留双份定义或双份文档，重复不消失 |
| 完全不给默认装配，探针全手工 | K8s 探针 404 的运维事故概率过高（见「兼容性」） |

---

## 兼容性

破坏性变更集中在下表（bald 处于设计阶段，破坏可接受；跨仓消费者只有 bald-admin）：

| 位置 | 变更 | 迁移 |
|---|---|---|
| `transport/http.NewHTTPServer` | 第 3 参 `readiness` 删除 | 业务/装配层自挂探针；appkit 走 `WithHealth` |
| `transport/http.WithProbePaths` | 删除 | `appkit.WithProbePaths`（`HealthOption`） |
| `transport/grpc.NewGRPCServer` / `NewGRPCServerWithRegister` | 第 3/4 参 `readiness` 删除；`WithReadinessPollInterval` 删除 | `bootstrap.WithGRPCHealth(h, interval)` |
| `transport/grpc` 的 reflection | 不再注册 | 契约 `grpc.reflection=true` 时由 `bootstrap` 注册；业务勿重复注册（重复会 panic） |
| `transport/gateway.NewGatewayServer` | 第 4 参 `readiness` → `opts ...httpserver.HTTPServerOption` | 传探针包装在 handler 侧 |
| `transport.ReadinessFunc` / `transport.Ready` | 删除（文件 `transport/readiness.go` 一并删） | `health.PingFunc` / `health.Health` + `Health.Readiness` |
| `bootstrap.With{HTTP,GRPC}Readiness`、`bootstrap.WithProbePaths` | 删除 | `bootstrap.WithGRPCHealth`；HTTP 探针在 appkit/业务 |
| `pkg/appkit.WithReadiness` | 删除 | `appkit.WithHealth(h, ...)` |
| 契约 `Server.Grpc.reflection` | **保留**（实现位置变化，语义不变） | 无需改动 |
| 行为：默认探针 | 默认**不再存在**（除非 `appkit.WithHealth`） | 见下 |

**运维风险与缓解**：默认无探针时，K8s `livenessProbe` 拿到 404 会反复重启容器，`readinessProbe` 404 会一直 NotReady。缓解三条：①框架侧提供 `appkit.WithHealth(h)` 一行装配（默认路径 `/healthz` `/readyz`，与旧行为等价）；②文档与示例把标准装配片段固化；③迁移清单里把"挂探针"列为必做项。

---

## 实现与过渡

- [x] `health`：新增 `Health.Readiness(ctx) error`（`Result → error`，`health/readiness.go`），保持零依赖
- [x] `transport/http`：删探针常量/`WithProbePaths`/两个 handler/`readiness` 参数；删探针用例，改为"协议层无框架路由"断言
- [x] `transport/grpc`：删 `readiness` 参数与轮询、`WithReadinessPollInterval`；外移 reflection；新增 `HealthServer()` 访问器；精简 `Stop`
- [x] `transport/gateway`：签名收敛为 `NewGatewayServer(httpCfg, backend, register)`（不再透传就绪）
- [x] `transport`：删 `readiness.go` 与 `TestReady_*`（保留 `Extract` 用例）
- [x] `bootstrap`：删 `With{HTTP,GRPC}Readiness`/`WithProbePaths`/`WithGRPCPollInterval`；新增 `WithGRPCHealth(h, interval)` 与组合服务器 `NewGRPCHealthServer`（`bootstrap/health.go`）；reflection 注册按契约落在 provider
- [x] `appkit`：删 `WithReadiness`；新增 `WithHealth(h, opts...)` + 导出 `WithProbes`（`pkg/appkit/health.go`）；HTTP/gateway handler 包装 + gRPC 装配透传；探针路径相同启动期 fail-fast
- [x] 示例：`_example/bald`（走 `WithHealth` 默认装配）、`_example/bald-gin`（业务自挂 handler，演示路 B）、`internal/codegen` 生成的 app 模板
- [ ] 跨仓：bald-admin 同步（独立提交，待发版后进行）
- [x] 文档：本文件 + 《Bald 健康检查设计》Status/背景同步、《Bald 传输层设计》与《应用框架设计》相关篇目、契约总览、guide《Bald 健康检查》与 quickstart、README、代码生成/Bootstrap/AppKit 相关篇目（原《服务端设计》已删除，其探针/health/reflection 内容归位至本文件与传输层设计）

**验证**（已执行）：`health`、`transport`、`transport/{http,grpc,gateway}`、`bootstrap`、根模块（含 `internal/codegen` 生成物编译/运行用例）、`_example`、`_example/bald` 全部 `go build` + `go vet` + `go test` 通过；新增用例覆盖——协议层无框架路由（http/gateway）、gRPC 标准健康服务在位且状态可由外部推、装配层轮询驱动 SERVING⇄NOT_SERVING、reflection 按契约注册、探针包装不遮蔽业务路由（`_example/bald` e2e：`/healthz`、`/readyz` 200 且 `/v1/greet` 仍 200）。

---

## 附录

**为什么 readiness 探针还要 JSON 明细，K8s 只看状态码？**
排障。`checks` 里每个检查器的状态与失败消息一眼可见，不必进容器看日志（同《Bald 健康检查设计》FAQ）。

**`Server` 接口要不要加 `Health()`？**
不加。一是它与 Kratos `transport.Server` 方法集保持兼容（本包注释已承诺），二是协议能力不等价（cron/tcp 没有探针端点）。若将来需要接口化，应由 `health` 侧定义可选接口（`health.Provider`），传输层用类型断言探测，保持零依赖方向。

**探针路径还能配吗？**
能，但归位到 `appkit.WithProbePaths(live, ready)`（`HealthOption`）。协议层不再有路径概念。

**gRPC 侧还需要 readiness 参数吗？**
不需要。状态推送由 `bootstrap` 的组合服务器完成，数据源是 `*health.Health`（`Check` → `SetServingStatus`）；业务若不用 appkit，也可以在 `WithGRPCRegister` 回调里自行注册 health 服务并推状态（那时应断开框架注册，避免同一服务重复注册）。

**相关文档**：《Bald 健康检查设计》（`health` module 内部）、《Bald 注册中心设计》（同族的"契约零依赖 + 装配显式注册"判别）、《框架契约总览》§0（模块依赖层级）。
