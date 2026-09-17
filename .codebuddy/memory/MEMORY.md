# MEMORY — bald 长期记忆（跨设备同步）

> 本文件为跨设备同步副本，源在各自设备 workspace 根 `.codebuddy/memory/MEMORY.md`。
> 2026-09-10 两设备版本合并归一（macOS 侧截至 09-03 + Windows 侧截至 09-08 + 09-07~09-10 增量）。
> **2026-09-17 增量合并**（Windows 侧 09-11~09-17：registry 下放/契约瘦身/七域归位、指标双体系、berrors v0.1.0、两项评估裁定、bald-admin 独立仓库现状；旧条目被覆盖处已就地标注）。
> 精简保留长期稳定事实；每日明细见 `YYYY-MM-DD.md`。

## 用户协作偏好
- **改文档只改指定处**：不用 `write_to_file` 重写整篇、不顺带新增章节（越界重写被回退过两次）。
- **简洁优先（YAGNI 强偏好）**：否决 go-lulu `Driver` 抽象层、bconfig 适配器方案、删接口的减法。
- 关注 proto 配置契约、多项目复用对齐；渐进式改造，要求指明代价/风险；实现后记录文档。
- **验证类代码要保留**（e2e 入仓库 + Taskfile），勿一次性删除。
- 决策风格：给评估后说「需要/直接」即采纳，期待直接落地。
- **bald 处于「开发设计阶段」（2026-09-17 用户明示规则）**：任何重大/破坏性变更都接受，**不怕影响下游**——bald-admin 等下游可随时跟着改、跟着升版本。因此评估设计时**禁止把「破坏跨仓消费者 / 要改下游签名 / 需重发多个 module」当作否决理由**；破坏面只用于估算工作量与迁移顺序，不作裁定依据。裁定依据只有两条：**① 收益是否真实存在；② 是否与既有判别规则/分层原则自洽**。
- **不自动提交/推送/打 tag**，需用户明确指令（09-17 两次明确要求，均已执行）；`.git/COMMIT_MSG_TMP` 允许自动删除，因超时授权删不掉的临时文件可加 `.gitignore`。
- 设计文档落 `docs/`：`docs/devel/zh-CN/`（内部设计，决策式）+ `docs/guide/zh-CN/`（用户手册），README 维护索引。
- 跨设备开发：记忆需同步进 bald 仓库（本目录）以便 git 拉取。设备路径：macOS `/Users/moweilong/Workspace/go/src/github.com/kalandramo/konglingfei/`、Windows `d:\code\konglingfei\`。

## 工作区结构
workspace 根是多独立 Go module 集合（非单仓库）：`bald`、`go-lulu`、`go-wind-toolkit`、`cobrax`、`easyai`、`go-utils`、`kratos`、`miniblog`、`onex`、`onexstack`、`osbuilder`、`protoc-gen-defaults`。`bald` 是独立 git 仓库（公开 `github.com/kalandramo/bald`）。`_example`/`examples` 目录不参与 `go build/test ./...`——验证 example 用显式路径。
- **2026-09-17 视图（覆盖上文目录清单）**：`bald`、`bald-admin`、`bald-crud`、`bald-utils`、`cobrax`、`easyai`、`go-wind`/`go-wind-admin`/`go-wind-bootstrap`/`go-wind-plugins`、`kratos`、`miniblog`、`onex`、`onexstack`、`osbuilder`、`slog`、`v3-admin-vite`。**bald-admin 已从 bald 仓库内迁出为独立 git 仓库**（前后端 monorepo，`github.com/kalandramo/bald-admin`，本地 `d:\code\konglingfei\bald-admin`）；bald 根 `examples/` 已清理、根 `configs/` 已删。

## bald 架构共识（proto-first）
- 定位（AGENT.md）：**固执己见的个人微服务框架**，融合 onexstack/pkg/app（启动配置）、Kratos（transport.Server/registry 契约）、go-lulu wind（App 层：errgroup 并发启停、优雅停机防坑、Endpoint 动态端口）。
- **proto 单一真相源**：配置/API/类型全 proto 生成（`pkg/conf` 契约层 + `pkg/config` viper 四源加载器）；不借鉴 onexstack `IOptions`。**`pkg/options` 已废弃**。
- 配置键四源一致：`--http.addr` ⇔ `http.addr` ⇔ `BALD_DEMO_HTTP_ADDR` ⇔ 文件 `http.addr`；flag 经 `appkit.Bind(prefix, opt)` 注册。
- **核心零后端耦合**（grpc-gateway 不进核心、otel 仅 API、berrors 仅标准库）、**函数式 Option DI**（不用 wire/fx/dig）、**依赖倒置中间件/拦截器**。
- HTTP 栈仅 gin（`pkg/web` 强绑 `*gin.Context`）+ gRPC + gateway 转码；日志 slog 门面；`contrib/` 现有 authn-jwt、store-gorm、cache-redis、authz-casbin、observability-otlp；registry/config 桥接 kratos 生态。
- 演进 P0–P9 已落地（2026-08-30）：P0 三阶段停机、P1 泛型 Registry、P2 分页三策略/Mapper、P3 log/berrors/middleware、P4 codegen CLI、P5–P8 抽象层（P8 多租户 `store.RegisterTenant`+`Where.T`+写路径反射注入）、P9 授权归一化（`AuthzOption` resolver 模式，根治 REST/gRPC 双命名空间 RBAC）。
- 第二轮优化 P10–T1–P13、P11+S1+C1+R1+A1（2026-08-31 已落）：P10 `middleware/bundle` 双传输链序固化门面；T1 `appkit/effect.go` 效应账本（逆序回放、`UnregisterTenant` 对偶）；P11 三 contrib 晋升；S1 `appkit/capability.go`（Provides/Requires 启动期 fail-fast）；C1 `appkit/component.go`（stopAll 五阶段）；R1 `appkit/keywatch.go`（key 级订阅）；A1 `appkit/mount.go`（Mount/Unmount 可逆对偶+组件热插拔）。P12 codegen 已落（`gen app` 模板 + AppSpec 方言两步）。
- 设计文档 `docs/devel/zh-CN/架构优化路线.md`（剩余：管理面端点/R1 diff-apply）。
- **2026-09-17 修正（覆盖上文旧表述）**：配置契约层已从根模块 `pkg/conf` + `pkg/config` 迁出为**独立 module**——`bconf`（proto 契约 `bootstrapv1`，生成物 `bconf/gen/go/**` 入库，17 proto、四 API=NewBootstrap/UnmarshalMap/Validate/BindFlags）+ `bconfig`（源抽象 Reader/Closer/Watcher/ValueWatcher/Decoder）；`pkg/options` 早已废弃。顶层独立 module 现有：`berrors`、`bconf`、`bconfig`、`bootstrap`、`log`、`registry`、`transport`、`health`、`metrics`、`cache`、`encoding`、`oss`、`ai`、`broker`、`circuitbreaker`、`ratelimit`、`retry`、`workflow`、`contrib/*`、`transport/<协议>`（17 个）。

## bald 核心设计终态（决策记录）
- **bconfig**（09-03）：5 能力轴 Reader/Closer/Watcher/ValueWatcher/Decoder；`FallbackReader.WatchValue` 只认 ValueWatcher——注释已诚实声明，见《Bald 配置系统设计.md》§5。
- **路由/绑定**（08-28 终稿）：pkg/web 强绑 gin（`HandleJSON/Query/Uri/AllRequest[T,R]`，bindPB/writePB protojson）；引擎无关 `pkg/core` 已删；校验用 onexstack `pkg/validation`。文档《路由注册与绑定设计.md》。
- **错误模型**：`pkg/berrors`（零依赖不可变 Error）+ `berrors/grpcerr`（ToStatus/FromStatus）+ `berrors/httperr`（Code↔HTTP 双向映射）。已 Accepted。**决策⑧（09-10 已落地 `b37ce86`）**：HTTP 错误体 = google.rpc.Status JSON `{"code","message","details":[{"@type","reason","domain","metadata"}]}`，框架出口 `transport/web.ErrorResponse`（+`StatusOf` 单源构造器），与 gateway 转码**字节级同形**（空值语义对齐 protojson ""/{}/[]）；成功体裸 protojson（UseProtoNames snake_case）；否决 Kratos `{code,data,message}` 信封。**规则**：错误串只进 message 不进 reason；gin 面错误一律走 ErrorResponse/bindErr/writeBizErr，禁 c.JSON 直写。**包名（09-11 `b537495`）**：berrors 模块 `package errors`→`berrors`（与目录/模块同名），全仓 40 处 import 别名清零；不再遮蔽标准库 errors，同文件可共存（`errors.Is` stdlib、`berrors.Is` 按 Reason 跨栈匹配）。遗留：各域 reason 值风格统一、前端 axios 读 message+details[0].reason。
- **文档体系**：devel=`docs/devel/zh-CN/`（内部设计，决策式）；guide=`docs/guide/zh-CN/`（用户手册）。README.md 维护索引。
- **日志**：pkg/log 是 slog 适配层；轮转用 lumberjack。slog 路线并入《日志设计.md》§9。observability 中间件用 `pkg/middleware/tracing.go` 的 `LogTraceIDs(ctx)`（SpanContext 无效时随机 hex ID，no-op tracer 不再全零）。
- **布局判别**：bald 顶层目录（有独立 go.mod）=可独立发布的桥接/插件模块；`pkg/`=根模块核心包；pkg/{store,authn,authz,audit} **不移根目录**（契约依赖 pkg/contextx、bconf proto）。**transport/ 下 17 个子目录各自独立 module**（改依赖要动各自 go.mod）。
  - **registry 已下放为独立 module（2026-09-17）**：契约 `bald/registry`（零内部依赖、仅 `context`，含 inmemory）+ 四后端 `registry/{etcd,consul,nacos,kubernetes}`（各自独立 module，require registry+bconf，**依赖图不含主模块**）；动因=后端要契约却不能依赖主模块（否则拖入 gin/viper/otel/grpc 且成环）；同域旧文档已删，见《Bald 注册中心设计.md》。
  - **`pkg/contextx` 裁定：不下放（2026-09-17 评估）**：虽零依赖（仅 stdlib `context`），但 19 处 import 全在主模块内（store / authn / crudbridge / middleware gin+grpc），仓外唯一消费者 bald-admin 本就依赖主模块——**不存在「只要它、不要主模块」的消费方**（对比 registry 四后端），无依赖图收益。再评估触发条件=首个零主模块依赖的 module 需读写这五键（trace/tenant 透传类）。
- **2026-09-10 增量**：吸收 bald-utils 三包——`pkg/id`（仅 NewGUIDv4/v7）、`pkg/stringcase`（snake_case 最小闭包）、`pkg/fieldmaskutil`（NestedMask+FieldMask）；transport/{tcp,webrtc} 改用根模块 pkg/id，根治 bald-utils 全家桶依赖。吸收标准=「是否是框架契约或框架运行的一部分」。
- **bconf 契约层（2026-09-05~09-17 定稿）**：默认值与校验手写单点维护；「写错键名报错」的如实边界=已知键错值报错（类型 coerce/值域/枚举），**拼错键名与业务段同被 DiscardUnknown 静默放行**（刻意取舍）。**无实现/死源一律删字段并 `reserved` 号与名字**（config 09-05 砍 fs/redis/zookeeper/oss/polaris；registry 09-17 砍 zookeeper/polaris/eureka/service_comb）——契约只为已实现的东西承诺形状。
- **七域 Registry 归位（2026-09-15~17）**：Provider/Registry/段枚举/Build/**RegistrarRegistry** 归 bootstrap（`bootstrap/registrar.go`），With*Registry 胶水/访问器/生命周期编排留 appkit（`appkit.NewRegistrarRegistry` 已删、**不留别名**）。判别=纯契约段构造（仅 bconf/标准库/独立 module）→bootstrap；需根模块包或运行期编排→appkit（Tracer/Metrics 两类 Registry 因依赖 otel 留 appkit）。**bconf 契约层没有 registry 校验代码**，`type` 写错的 fail-fast 在装配层 `RegistrarRegistry` 查表。
- **registry 契约瘦身 + 后端版本统一（2026-09-17「第三刀」，已发）**：删四个无实现段（四个 message + Type 四个枚举值 + 四个字段；枚举号 4/5/6/8、字段号 5/6/7/9 与名字全 `reserved`），零消费者已查证。四后端 `require bald/bconf` v0.1.0 → **v0.7.2**；提交 `c272695`，tag `bconf/v0.7.2` 与 `registry/{consul,etcd,kubernetes,nacos}/v0.1.1` 同指该提交（主模块自身仍 require bconf v0.7.0）；发版记录已落《Bald 注册中心设计.md》（`09ce57a`）。
- **指标双体系（2026-09-15）**：①`pkg/metrics` Recorder=请求级（审计同源维度、otel 全局、契约链 bconf→appkit MetricsRegistry→observability-otlp/contract→Setup 多 Reader）；②顶层 `metrics/` 子模块=传输级三原语（Counter/Histogram/Gauge+Closer）+ 三后端（prometheus/otel/datadog），六传输 WithMetrics 注入，零 tag。两体系在 `otel.SetMeterProvider` 全局位互斥。《指标抽象设计.md》+《Bald 指标埋点后端设计.md》。
- **health vs transport readiness（2026-09-17 评估，裁定不删）**：`transport/readiness.go`（`ReadinessFunc` + `Ready`）不删。`ReadinessFunc` 是 `transport/{grpc,http,gateway}` 构造签名 + `bootstrap.With{GRPC,HTTP}Readiness` + `appkit.WithReadiness` + 跨仓 bald-admin main.go 3 处的公开契约；且 `transport` 根包 go.mod **零 require**（纯 stdlib 契约 module），不宜依赖实现型 `health` module。`health` 至今**未发 tag、根模块未 require、全仓零消费者**（设计文档自述「tag 未发、消费者未接」）。唯一重叠点是 `Ready`（聚合）↔ `health.Health/AllCheckers`，但 `Ready` 零生产消费者（仅 2 个测试引用）；其去留应与「health 收编（发 tag + 根模块 require + 新增 bootstrap health Registry + 决定谁拥有 `/readyz`）」同批决策——`health` 未提供 `Result→error` 适配，现在删 `Ready` 只会把适配推给业务。

## 认证/授权/租户/数据权限（P7/P8/P9）
- `pkg/authn`：`Authenticator` 接口 + `AuthClaims` + context 注入。实现必须校验 ExpiresAt（中间件不重复判断）。
- `pkg/authz`：`Authorizer` 接口（Func/AllowAll/DenyAll）；casbin 不入核；归一化在核心拦截器层（P9 resolver），桥接只做纯 Enforce。
- `pkg/store/tenant.go`：`RegisterTenant(key, fn)` + `Where.T(ctx)`；`tenant_id` 需显式注册才开启。`pkg/store/scope.go`：`RegisterDataScope`。
- gin/grpc 中间件：Bearer→`ContextWithToken`→`Authenticate`→`ContextWithAuthClaims`。

## 关键坑（bald 核心，改后必查）
- **gRPC 错误透传**：拦截器链最外层挂 `grpcmw.ErrorInterceptor()`，否则 `*berrors.Error` 兜底成空 Unknown。
- **多租户隔离**：Authn 认证后必须 `contextx.WithTenantID(ctx, claims.TenantID)`，否则隔离静默失效。
- **server 地址**：`Start`/`Endpoint` 实时读 `cfg.GetAddr()` 不缓存快照；Gateway 延迟到 Start 建 conn；e2e 地址用 `testkit.FreeAddr(t)`。
- **BindPFlags**：仅对 `flags.Changed==true` 的 flag 调用。**protovalidate**：managed mode `except`。**protojson/proto.Merge**：替换 vs append 语义（合并前 Clear）。**Duration**：`FormatFloat(d.Seconds(),'f',-1,64)+"s"`。
- **审计链序**（gRPC：Authn→Audit→Authz）；审计旁路 recover 会掩盖测试桩 bug——排查先怀疑测试桩。
- **OTLP 装配**：`resource.Merge` 对齐 semconv v1.43.0 schema URL；trace 退出前必须 shutdown。
- **CLI Options 命名**：领域字段 `Out`（string）会遮蔽内嵌 IOStreams.Out（io.Writer）——输出路径字段必须用 **OutDir/OutFile**（已记 devel 设计文档，新子命令必守）。

## cmd/bald CLI（2026-09-10 改造完成，ded6529）
- `internal/codegen/iostreams.go`：IOStreams（kubectl 迷你版）+ `CheckErr` 唯一错误出口 + `writeFileGuarded`（**目标文件存在即拒绝，--force 显式覆盖**——不静默改动原则）。
- proto/store/app 三叶子 Options 三阶段化（Complete/Validate/Run，Run 不读 flag）；main.go 接 CheckErr + SilenceErrors。

## go-wind-admin 业务移植（示例 go-bald-admin，主计划 T0–T10）
- 计划文档：`bald/docs/devel/zh-CN/go-wind-admin 业务移植计划.md`（README 已索引）。范围=精选子集 9 项，里程碑 T0–T8 + F1/T10 扩展。**§0 硬性契约：外部依赖禁止 fake/mock/stub、禁止硬编码返回值**。
- **进度（09-10 收官）**：T0–T10 全部落地。T5 buf+REST、T6 成熟库、T7 Nacos（registry 09-08 + config 双通道认证 09-10）、T8 审计+指标（缺省 :9091 根治抢端口）、T9 OTLP 终验（trace Jaeger 闭环 `b8488ab`/`589ada9`；metrics VictoriaMetrics 命中 `3afb0bc`）；F1 store-gorm Open 上提、T10 目录对齐；前端 go-bald-admin-web F0–F7 验证完成。
- **T2 决策（必守）**：① proto 全收敛 api/（buf 根=api/，生成物 api/gen/<域>/v1/）；② REST 路径单数与 gRPC 归一同源；③ gin handler 用 bindPB/writePB（protojson），禁 c.JSON 直序列化 proto；④ 列表 total 用 uint32。
- **T3 决策（必守）**：D3 策略数据化（RolePolicy 表，主键 `role:object:action`）；动作六元组 get/post/put/delete/list/write；Menu.ParentID 空串=根；contrib/store-gorm toMapExcludeKey 已修 `gorm:"-"`；contrib/authz-casbin 容错空策略（fail-closed）；e2e 解码 writePB 输出必须用 protojson。
- **T4 决策（必守）**：DictType.ID=type_code、DictEntry.ID=`<type_code>:<entry_value>`；字典=租户级实体，缓存键 `dict:entries:<tenant>:<typeCode>`；contrib/cache-redis Get 降级语义（Redis 非 Nil 错误→直连 loader）。
- **时序约定（必守）**：biz 层引用 bootstrap 包级桥接一律请求期读取，禁止 wire 构造期快照（InitializeBiz 先于 InitBridges，快照必 nil）。
- **git 结构**：示例项目纳入 bald 根仓库（.gitignore 忽略规则已删）；真实凭证 `configs/go-bald-admin.yaml` 不入库，占位模板入库；**go-bald-admin-web 是独立 git 仓库**（根仓库 .gitignore 忽略，提交需进该目录单独做）。
- **环境事实**：云端 PG 10.82.138.249:31068/db=go-bald-admin、Redis :30967 db8、MinIO 10.82.69.251:30658、Nacos 10.82.130.200:30000（config 通道强制鉴权，凭据在 registry/config 契约段，namespace 填命名空间 UUID）；共享 PG 跑 e2e 时数据敏感断言会因历史数据失败（先清库或 stash 对照证明非回归）。
- **Insight 可观测（09-10 实测）**：collector 入口 10.82.138.249:32414（NodePort 4318 HTTP，trace/metrics 同端口）；traces→collector `otlp/global` 10.82.49.246:4317（Jaeger UI 可查）；metrics→collector `prometheusremotewrite`→insight-agent Prometheus（缓冲）→全局 **VictoriaMetrics**（UI 指标查询数据源）。**查询按 `job` 标签过滤**（OTLP service.name→Prometheus job 转换，按 service_name 过滤=假阴性）；metrics 无 k8s 来源标签（resource processor 只挂 traces）；OTel 指标首次记录才有数据点（不压流量 bald_* 恒空）。
- 源项目事实：38 service、Ent schema 唯一真源、种子 admin/Abcd@1234（示例项目自定 admin/admin123、alice|bob/alice123）、casbin 从 DB 装载。

## go-bald-admin（reference example 五支柱闭环，已终态）
- 里程碑 M0 脚手架→M1 JWT+RBAC→M2 store+gRPC→M3 bcrypt+多租户→M4 租户注入→M5 buf+REST 转码→M6 成熟库（casbin/redis/wire/JWT 非对称/DSN scheme/gateway）→M7 审计→M8 指标→M9 OTLP+审计落库+Redis Stream。
- 桥接扩展模式：新后端仅新增接口实现，核心零改动；`InitBridges` 幂等。
- 有 README.md + Taskfile.yml。不接 asynq/minio-sdk 直用/ent/opa/kratos 全家桶。
- **2026-09-17 现状（覆盖上文「git 结构」旧表述）**：示例已迁出为**独立仓库** `github.com/kalandramo/bald-admin`（本地 `d:\code\konglingfei\bald-admin`，前后端 monorepo），T0–T10 完成；同日完成对 bald 重构的适配（`e9931d1` 已推送）：升 bald v0.7.0 / bootstrap v0.7.2 / bconf v0.7.1，registry 系 import 与 `registrarRegistry()` 改新家（`bootstrap.NewRegistrarRegistry()`）。**零 replace（纯 tag 依赖），是 bald 发版「外部可构建性」的实证工具**——升级后 tidy+build 通过即证明 tag 自洽。
- **bald-admin 存留决策**：proto 收敛 `api/`（源 `api/protos/<域>/v1/`，生成物 `api/gen/go/<域>/v1/`）；REST 路径单数；gin handler 用 bindPB/writePB（protojson + UseProtoNames → 前端全 snake_case）；wire 构造期禁止快照 bootstrap 桥接（请求期读取）；casbin 策略数据化（`role:object:action` 主键）；字典=租户级实体；OpenAPI v3 内嵌（gnostic + go:embed + `GET /openapi.yaml`）。环境：本机无 gcc（SQLite 用 glebarez）；metrics 与 gRPC 抢 :9090（跑 appkit 用 :9091）；种子 admin/admin123。

## 前后端联调与契约同步
- **writePB** 用 `protojson.MarshalOptions{UseProtoNames: true}` 输出 snake_case（前端 types 全按 snake_case 建模）；bindPB 两种命名都接受。
- **biz 纯 Go 结构被 `c.JSON` 序列化必须带 json tag**；proto 消息才走 writePB。
- 前端 axios `baseURL: VITE_BASE_URL || "/"` 必须绝对根路径（相对 URL 在嵌套路由页打不中 vite proxy）。
- 文件按 MIME 分桶（image→images/text 与文档→docs/其余→`file.bucket` 兜底桶——不是全局桶名）；MinIO 对象键=`目录/uuid32+扩展名`，原始文件名存 DB.FileName。
- **契约同步方案（09-10 定稿，文档《示例前后端契约同步设计.md》）**：按消费者扇出 buf 配置（Go/OpenAPI/前端 TS 各一份），生成物跨目录直出前端；**独立迁移前置约束：前后端必须合 monorepo**；第一步 buf v2 + TS 生成，OpenAPI 二步按需；hardcode 差异不可盲抄（bald 裸 protojson 无信封）。
- **错误响应契约（决策⑧）**：前端 axios 拦截器展示读 `message`、程序化读 `details[0].reason`（落地时替换现有 `errorData?.error` 双 key hack）。

## proto 生成 & 模块约定（易踩坑，务必遵守）
- **proto 生成只能走 Taskfile**：`task proto-config`（核心契约）、`task proto-example`（_example）。**勿手敲 `buf generate --path`**——部分子包静默不生成。改 .proto 后必须重跑再提交。
- **生成代码进仓库**：`bconf/gen/go/**` 提交进 git（与 onexstack 不同）。**2026-09-17 命令现状**：核心契约=`cd bconf && buf generate`（buf 在 `D:\gopath\bin`，remote plugin 可命中缓存）；`_example/bald/gen` 重建=`buf generate proto --template proto/buf.gen.yaml`（`protoc-gen-defaults` 版本 pin 在 Taskfile.yml）。**生成器版本漂移会顺带改无关文件**（注释空格 `// （`→`//（`、纯行尾变化）——先 `git checkout --` 回退，只留目标协议；`go build ./...` 在只有一个 main 包的目录会落可执行文件（如 `_example/bald-gin.exe`），收尾要删。
- **_example 是独立 module**：`_example/bald`，模块路径 `github.com/kalandramo/bald/example/bald`，`replace ../..`；验证须 `cd _example/bald && GOFLAGS=-mod=mod go build/test`。
- **appkit API 形状**：① `New(opts...)` variadic 在末位；② `MountComponent(ctx, comp)` 双参；③ `Servers(...)` 是 Option 非方法；④ 日志 `baldlog.SetLogger(baldlog.NewSlogLogger(baldlog.NewOptions()))`。
- **T10 后路径**：gRPC handler 在 `internal/apiserver/handler/grpc/`；e2e 在 `internal/apiserver/e2e/` 独立包；`RegisterRoutes(e, *BizSet)` 直传（BizSet 在 `internal/apiserver/bizset.go`）。
- gateway REST 路径复数（proto annotation 定 `/v1/secrets/{id}`）、gin 单数（`/v1/secret/:id`）——冒烟别用错。

## 工具链与发布
- 工具链：protoc 35.1、buf 1.57.2（`D:\gopath\bin`）、go **1.27.1**（09-17 现状，旧记 1.26.5）、go-task 3.53.1（Windows 跨平台）。`protoc-gen-defaults` pin 在 Taskfile vars（v0.0.2）。
- **警惕编造的 pseudo-version**：新增依赖前用 `go list -m -versions <mod>` 核实。
- **发布**：仓库公开 `github.com/kalandramo/bald`，tag `v0.1.0` 已推；contrib/* submodule 发布需 `contrib/<name>/v<N>` 单独打 tag；install 一律 `@latest`；sumdb 收录延迟数小时（404 正常，GONOSUMDB 绕过）。
- **发版顺序（2026-09-17 定稿）**：①兄弟模块 go.mod require 升**真实版本**（replace 仅本地开发用）→ ②lightweight tag 指向该提交（**tag 内容必须带真实版本，`v0.0.0 + replace` 外部不可构建**）→ ③build 验证 → ④推送 main + tag。**同一提交可打多个 tag**（如 `bootstrap/v0.7.2` 与主模块 `v0.7.0` 同指一提交）。
- **发版前必做两查**：①tag 自洽 `git show <tag>:<module>/go.mod`（不得是 v0.0.0）；②外部可构建性——本地 replace 会掩盖子依赖版本错配（四后端曾 `require bconf v0.1.0` 而主模块 v0.7.0），**跨仓真消费者 bald-admin 升级 tidy+build 是最强实证**。
- **proxy 行为与新 tag 拉取**：goproxy.cn 对新 tag 按需回源，刚推完可能 404 / `stream ID 1; INTERNAL_ERROR`（09-17 实测 `@v/v0.7.2.info` 数分钟仍 404），**先重试 1–2 次**；仍不行就临时 `GOPROXY=direct` + `GIT_CONFIG_COUNT=1` / `GIT_CONFIG_KEY_0='url.git@github.com:.insteadOf'` / `GIT_CONFIG_VALUE_0='https://github.com/'` 把 HTTPS 重写为 SSH 拉取（**不落盘全局 git config**），再 `GOSUMDB=off` / `GONOSUMDB='*'`，最后 `GOPROXY=off` 走缓存完成 tidy/build。
- **本地 replace 不跨 module 传递**：主模块或子模块新增 require/公开面后，所有 replace 主模块的下游会报 `updates to go.mod needed` 或 `undefined: 符号`。修法：手工补对应 replace，再在各目录跑 `go build -mod=mod ./...` 让 Go 自动补 require，然后 `go test ./...` 复验。09-17 三次波及：registry 下放（10 模块）、bootstrap 新增 `RegistrarRegistry`（contrib/{audit-store,audit-stream,observability-otlp}）、根 require 升 bootstrap v0.7.2（上述 3 个 + `_example` + `_example/bald`）。
- **最新版本（2026-09-17，覆盖上文「tag v0.1.0 已推」）**：根 **v0.7.0**（`e1163a7`，三项 breaking：pkg/registry 下放、七域 Registry、RegistrarRegistry 迁 bootstrap）；registry 系：契约 v0.1.0 + 四后端 v0.1.1（第三刀）；bootstrap v0.7.2；bconf v0.7.2（含契约瘦身）；log v0.5.1；health v0.1.0（**未发 tag**）；berrors/bconfig/transport×5/contrib×6/encoding v0.1.0；log/{aliyun,charm,loki,sentry,tencent} v0.2.0；oss/minio v0.1.0；cache 系 v0.1.x。**metrics 系四模块零 tag**；transport/{tcp,webrtc} require `metrics v0.0.1` 是**编造版本**（tag 不存在），kafka/rabbitmq/redis/rocketmq 用伪版本兜底——**新增 require 前先 `git tag -l` 核实**。
- nacos 验证资产在 `_example/bald`（`//go:build nacos`）。

## 环境事实（双设备）
- **Windows/PowerShell**：写提交信息必须 `[IO.File]::WriteAllText(path, text, [Text.UTF8Encoding]::new($false))`——`Set-Content -Encoding UTF8` 带 BOM 污染 commit 标题；`git commit -m` 长中文多段信息可能静默不执行（用 `.git/COMMIT_MSG_TMP` + `git commit -F`，用后即删）；.NET File API 相对路径基于进程目录，批量改文件用绝对路径；gofmt -l 全列表现象 = autocrlf CRLF 噪音（实质检查需 LF 规范化）；go test 挂死先查残留 compile 进程（持有 build 缓存锁，Stop-Process 清掉）；`Invoke-RestMethod` 单元素数组渲染成标量（用 `Invoke-WebRequest` 取原始 Content）；本机无 gcc，SQLite 用 glebarez 纯 Go。
- **Windows 补充（2026-09-17）**：批量 `Move-Item` 到已存在目录会**双层嵌套**（结构性操作前先提交/备份）；go.mod 等文本改动用 `replace_in_file`（PowerShell 管道改文件被 Craft 拦截，`Get-Content -Raw` 不带 `-Encoding` 也会被拦）；GBK 损坏文件用 gb2312 读 + UTF8 无 BOM 写（用 U+FFFD 计数排查）。
- **参考实现**：onexstack `pkg/core`（泛型流水线）+ `pkg/binding`（多源绑定）；osbuilder 模板 `internal/apiserver/`。
- **macOS**：无 `timeout`，冒烟用 `cmd & PID=$!; sleep N; kill -INT $PID`。
- **GBK 编码损坏**（go-bald-admin-web 曾踩）：Windows 编辑器 GBK 保存 `.vue/.ts` 导致中文乱码；纯 GBK 用 `[Text.Encoding]::GetEncoding('gb2312')` 转码，双重编码「锟斤拷」直接对照模板重写（逆向转码丢字不可逆）；扫描范围含根目录配置文件。
- 通用：`gofmt -l` 既有未过文件只格式化自己新建/改的文件；`golang-jwt/v5` ParseWithClaims 默认校验 exp。
