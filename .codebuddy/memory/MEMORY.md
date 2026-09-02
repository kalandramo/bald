# MEMORY（bald 仓库内同步副本）

> 本文件为跨设备同步副本，源在 workspace 根 `.codebuddy/memory/MEMORY.md`。
> 精简保留长期稳定事实；每日明细见 `YYYY-MM-DD.md`。

## 工作区结构
根路径 `/Users/moweilong/Workspace/go/src/github.com/kalandramo/konglingfei/` 是多个**独立 Go module** 集合（非单仓库）：`bald`、`go-lulu`、`go-wind-toolkit`、`cobrax`、`easyai`、`go-utils`、`kratos`、`miniblog`、`onex`、`onexstack`、`osbuilder`、`protoc-gen-defaults`。彼此不一定互相依赖。`bald` 是其中独立 git 仓库，路径 `konglingfei/bald/`。`_example`/`examples` 目录不参与 `go build/test ./...`——验证 example 用显式路径。

## bald 架构共识（proto-first）
- 定位（AGENT.md）：**固执己见的个人微服务框架**，融合 onexstack/pkg/app（启动配置）、Kratos（transport.Server/registry 契约）、go-lulu wind（App 层：errgroup 并发启停、优雅停机防坑、Endpoint 动态端口）。
- **proto 单一真相源**：配置/API/类型全 proto 生成（`pkg/conf` 契约层 + `pkg/config` viper 四源加载器）；不借鉴 onexstack `IOptions`。**`pkg/options` 已废弃**。
- 配置键四源一致：`--http.addr` ⇔ `http.addr` ⇔ `BALD_DEMO_HTTP_ADDR` ⇔ 文件 `http.addr`；flag 经 `appkit.Bind(prefix, opt)` 注册，server 直接消费 `confv1.Http/Grpc` 指针。
- **核心零后端耦合**（grpc-gateway 不进核心、otel 仅 API、berrors 仅标准库）、**函数式 Option DI**（不用 wire/fx/dig）、**依赖倒置中间件/拦截器**。
- HTTP 栈仅 gin（`pkg/web` 强绑定 `*gin.Context`）+ gRPC + gateway 转码；日志 slog 门面；`contrib/` 现有 authn-jwt、store-gorm、cache-redis、authz-casbin、observability-otlp（后三个 P11 晋升：模型内嵌/策略调用方注入、env 读取上移调用方、`SecretKey`→泛化 `Key`）；registry/config 桥接 kratos 生态。
- 演进 P0-P9 已落地（2026-08-30）：P0 三阶段停机、P1 泛型 Registry、P2 分页三策略/Mapper、P3 log/berrors/middleware、P4 codegen CLI、P5-P8 抽象层（P8 多租户 `store.RegisterTenant`+`Where.T`+写路径反射注入）、P9 授权归一化（`AuthzOption` 的 `WithObjectResolver/ActionResolver/SubjectResolver`，默认零破坏，可选 `DefaultGRPC/HTTPObject/Action` 传输中立归一化，根治 REST/gRPC 双命名空间 RBAC）。P2.3 多引擎子模块（mongo/ent/redis）暂不新建（gorm 已落作范本）。
- **第二轮优化 P10-T1-P13（2026-08-31 已落）**：P10 `pkg/middleware/bundle` 双传输链序固化门面（`Normalized()` 一键 P9 归一化，7 契约测试）；T1 `pkg/appkit/effect.go` 效应账本（`Effect(name,undo)`+`UndoEffects` 逆序回放幂等/panic 隔离/独立超时，stopAll 阶段 0 + 失败路径回放，`store.UnregisterTenant` 对偶）；P13 删孤儿 replace + `pkg/testkit.FreeAddr`。
- **第二轮优化 P11+S1+C1+R1+A1（2026-08-31 已落，含收尾）**：P11 三 contrib 晋升（见上 contrib 行）；S1 `pkg/appkit/capability.go`（`Provides/Requires`+`Resolve` 启动期 fail-fast，范例接入 `Requires("audit.store","db")`）；C1 `pkg/appkit/component.go`（`Component{Name,Start,Dispose}`+`ComponentFunc` 适配，stopAll 五阶段：0 效应→1 BeforeStop→2 Server.Stop→3 AfterStop→4 组件逆序 Dispose；范例 `traceShutdownFn` 已迁移为组件）；R1 第一子集 `pkg/appkit/keywatch.go`（`OnKeyChange` key 级订阅：diff 才触发、与全量 OnConfigChange 共存、基线 loadConfig 后武装）；A1 `pkg/appkit/mount.go`（`Registry.Mount/Unmount` 运行期可逆对偶 + `AppKit.MountComponent/UnmountComponent` 组件热插拔（disposed 竞态闭环）+ 重组审计 `AuditEvent{Object:"component"}`）；收尾：`框架契约总览.md` 补录全部新契约（§0 树/§1 appkit 新 API/§9.3 bundle/§11.6-11.10 五支柱/§12 contrib 表/§13 速记）、`README.md` 包树更新（旧 pkg/errors→berrors+contrib+五支柱）。P12 设计固化（`gen app` 装配模板生成器两步走，复绪条件=模板模式跑过一个完整里程碑）。设计文档 `docs/devel/zh-CN/架构优化路线.md`（剩余：管理面端点/R1 diff-apply，复绪待实践；防漂移清单 §4）。

## 认证/授权/租户/数据权限（P7/P8/P9）
- `pkg/authn`：`Authenticator` 接口 + `AuthClaims`（Subject/TenantID/Name/Scopes/Roles/ExpiresAt/Issuer）+ context 注入。**实现必须校验 ExpiresAt 过期**（中间件不再重复判断）。
- `pkg/authz`：`Authorizer` 接口（Func/AllowAll/DenyAll）；不引入 casbin 入核；归一化在核心拦截器层（P9），桥接只做纯 Enforce。
- `pkg/store/tenant.go`：`RegisterTenant(key, fn)` + `Where.T(ctx)`（深拷贝，业务已写该列任意 Op 则不重复注入）；`tenant_id` **需显式注册** `DefaultTenantFunc` 才开启（不在 init 隐式注册）；`UnregisterTenant(key)` 为 T1 逆操作对偶。
- `pkg/store/scope.go`：`RegisterDataScope(fn(ctx, *AuthClaims) []*FilterCondition)`；`mergeDataScope` 过滤 nil/空字段条件。
- gin/grpc 中间件：`middleware/{gin,grpc}` 用 `Authenticator`/`Authorizer`，Bearer→`ContextWithToken`→`Authenticate`→`ContextWithAuthClaims`。

## 关键坑（bald 核心，改后必查）
- **gRPC 错误透传**：拦截器链**最外层**挂 `grpcmw.ErrorInterceptor()`（调 `grpcerr.ToStatus`），否则 `*berrors.Error` 兜底成空 Unknown。
- **多租户隔离**：`AuthnInterceptor` 认证后必须 `contextx.WithTenantID(ctx, claims.TenantID)`，否则 `Where.T` 取空、隔离静默失效。
- **server 地址**：`Start`/`Endpoint` 实时读 `cfg.GetAddr()`，不能缓存构造期快照；Gateway 延迟到 `Start` 建 conn 且 attach 完立即释放 `mu` 防死锁；e2e 后端地址用 `testkit.FreeAddr(t)` 非 `:0`。
- **BindPFlags**：仅对 `flags.Changed==true` 的 flag 调用（零值 flag 压过 env/文件）。
- **protovalidate**：validate.proto 用 managed mode `except`，不生成本地副本。
- **protojson/proto.Merge**：protojson 是替换语义，`proto.Merge` 的 repeated 是 append（合并前 Clear）。
- **Duration 序列化**：`FormatFloat(d.Seconds(),'f',-1,64)+"s"`，不能用 `time.Duration.String()`。
- **授权归一化在核心层**（resolver 模式），桥接 `Authorizer.Authorize` 只做纯 Enforce，勿重复归一化 FullMethod。
- **审计拦截器链序（gRPC：Authn→Audit→Authz）**：Audit 在 Authn 内侧读 subject/tenant、Authz 外侧捕 deny/error；gin 用 `router.Use`。审计旁路不阻断，Auditor panic/err 降级记日志。
- **审计旁路会掩盖测试桩 bug**：Auditor 桩漏初始化字段导致 nil deref panic 时，被 `recordSafely` 的 recover 静默吞掉，表象是「audit 不触发」——排查先怀疑测试桩再怀疑框架（P10 落地时踩过）。
- **observability 装配序**：e2e `issueToken` 依赖 `bootstrappkg.Signer`，须在 `InitBridges` 之后调用；`InitBridges` 幂等。
- **OTLP 装配坑**：`metric.WithReader` 非可变参（逐项 append）；`resource.Merge` 须对齐 semconv v1.43.0 schema URL 否则 `conflicting Schema URL`；裸 host:port 用 `WithEndpoint+WithInsecure`、`http(s)://` 用 `WithEndpointURL`；trace 退出前必须 shutdown 否则丢尾批 span。

## go-bald-admin（examples/go-bald-admin，bald 官方 reference example，已终态）
- 里程碑链：M0 脚手架→M1 JWT+RBAC 双侧闭环→M2 store+真实 gRPC→M3 bcrypt+多租户→M4 写路径租户注入+DB 配置化→M5 buf 生成+REST transcoding→M6 成熟库接入（casbin 桥接/redis cache-aside/wire 业务装配/JWT 非对称/DSN scheme 路由/gateway 生产化/CR 闭环）→M7 审计（核心 `pkg/audit`+双侧拦截器+`AuditOption`）→M8 指标（核心 `pkg/metrics` Recorder+`AuditWithMetrics`；范例 Prometheus :9090）→M9 OTLP 直推（metrics+trace 单一 `BALD_ADMIN_OTLP_ADDR` 驱动）+审计落库（StoreAuditor）+Redis Stream（StreamAuditor 非阻塞+MultiAuditor 组合）。五支柱闭环：认证/授权/审计(日志+落库+流)/指标(Prometheus+OTLP)/trace(OTLP)。
- 桥接扩展模式：新后端仅新增接口实现（casbin→`Authorizer`、Store/Stream→`Auditor`），核心零改动；`InitBridges` 幂等。
- 有 README.md + Taskfile.yml（build/vet/test/run/run:otlp/verify）。不接 asynq/minio/ent/opa/kratos 全家桶。
- **§0 实现契约（设计文档硬性要求）**：外部依赖禁止 fake/mock/stub、禁止硬编码返回值、禁止占位桥接；允许 SQLite 内存库/内存 RBAC（须显式标注）。优先于"先跑通"简化诉求。

## 用户偏好
- 关注 proto 配置契约、多项目复用对齐；渐进式改造，要求指明代价/风险；实现后记录文档。
- **验证类代码要保留**（e2e 入仓库 + Taskfile），勿一次性删除。
- 决策风格：给评估后说「需要/直接」即采纳，期待直接落地。
- 跨设备开发：记忆需同步进 bald 仓库（`bald/.codebuddy/memory/`）以便 git 拉取。

## 环境/工具
- 工具链：protoc 35.1、buf 1.57.2、go 1.26.5、go-task 3.53.1（Taskfile.yml，原 Makefile 已删为 Windows 跨平台）。
- macOS 无 `timeout`，冒烟用 `cmd & PID=$!; sleep N; kill -INT $PID`。
- `gofmt -l` 既有未过文件：只格式化自己新建/改的文件，避免无关 diff。
- `golang-jwt/v5` ParseWithClaims 默认校验 exp。

## proto 生成 & 模块约定（易踩坑，务必遵守）
- **proto 生成只能走 Taskfile**：`task proto-config`（核心契约 api/proto → pkg/conf/gen/go）、`task proto-example`（_example gRPC/gateway）。**勿手敲 `buf generate --path`**——部分子包会静默不生成（config/store 曾踩过）。改任何 .proto 后必须重跑再提交。
- **生成代码进仓库**：`pkg/conf/gen/go/**`（config/store/appspec）**提交进 git，非 gitignore**（与 onexstack 不同）。`api/proto/bald/appspec/v1/appspec.proto` 是 P12 第二步的 AppSpec 方言。
- **_example 是独立 module**：目录 `_example/bald`，模块路径 `github.com/kalandramo/bald/example/bald`（与代码 import 前缀一致！），`replace github.com/kalandramo/bald => ../..`。下划线目录被 Go 工具链忽略，验证须 `cd _example/bald && GOFLAGS=-mod=mod go build/test`（或 `task example-build`）。
- **appkit 关键 API 形状**（生成模板曾错）：① `appkit.New(opts...)` 的 variadic 展开 `capOpts...` 必须在末位；② `MountComponent(ctx, comp)` 双参（名取自 `comp.Name()`）；③ `Servers(servers ...server.Server)` 是 **Option** 非方法；④ 日志 `baldlog.SetLogger(baldlog.NewSlogLogger(baldlog.NewOptions()))`。
- **P12 codegen 入口**：`_example/bald/codegen` 的 `gen app <name>`（模板骨架，P12 第一步）+ `gen app <name> --spec <AppSpec.json>`（AppSpec 方言驱动，P12 第二步）；生成物验收标准=在 bald module 内 `go build` 通过（测试已覆盖 http+grpc / grpc-only 两分支）。
