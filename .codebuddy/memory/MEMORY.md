# MEMORY — bald 长期记忆（跨设备同步）

> 本文件为跨设备同步副本，源在各自设备 workspace 根 `.codebuddy/memory/MEMORY.md`。
> 2026-09-10 两设备版本合并归一（macOS 侧截至 09-03 + Windows 侧截至 09-08 + 09-07~09-10 增量）。
> 精简保留长期稳定事实；每日明细见 `YYYY-MM-DD.md`。

## 用户协作偏好
- **改文档只改指定处**：不用 `write_to_file` 重写整篇、不顺带新增章节（越界重写被回退过两次）。
- **简洁优先（YAGNI 强偏好）**：否决 go-lulu `Driver` 抽象层、bconfig 适配器方案、删接口的减法。
- 关注 proto 配置契约、多项目复用对齐；渐进式改造，要求指明代价/风险；实现后记录文档。
- **验证类代码要保留**（e2e 入仓库 + Taskfile），勿一次性删除。
- 决策风格：给评估后说「需要/直接」即采纳，期待直接落地。
- 跨设备开发：记忆需同步进 bald 仓库（本目录）以便 git 拉取。设备路径：macOS `/Users/moweilong/Workspace/go/src/github.com/kalandramo/konglingfei/`、Windows `d:\code\konglingfei\`。

## 工作区结构
workspace 根是多独立 Go module 集合（非单仓库）：`bald`、`go-lulu`、`go-wind-toolkit`、`cobrax`、`easyai`、`go-utils`、`kratos`、`miniblog`、`onex`、`onexstack`、`osbuilder`、`protoc-gen-defaults`。`bald` 是独立 git 仓库（公开 `github.com/kalandramo/bald`）。`_example`/`examples` 目录不参与 `go build/test ./...`——验证 example 用显式路径。

## bald 架构共识（proto-first）
- 定位（AGENT.md）：**固执己见的个人微服务框架**，融合 onexstack/pkg/app（启动配置）、Kratos（transport.Server/registry 契约）、go-lulu wind（App 层：errgroup 并发启停、优雅停机防坑、Endpoint 动态端口）。
- **proto 单一真相源**：配置/API/类型全 proto 生成（`pkg/conf` 契约层 + `pkg/config` viper 四源加载器）；不借鉴 onexstack `IOptions`。**`pkg/options` 已废弃**。
- 配置键四源一致：`--http.addr` ⇔ `http.addr` ⇔ `BALD_DEMO_HTTP_ADDR` ⇔ 文件 `http.addr`；flag 经 `appkit.Bind(prefix, opt)` 注册。
- **核心零后端耦合**（grpc-gateway 不进核心、otel 仅 API、berrors 仅标准库）、**函数式 Option DI**（不用 wire/fx/dig）、**依赖倒置中间件/拦截器**。
- HTTP 栈仅 gin（`pkg/web` 强绑 `*gin.Context`）+ gRPC + gateway 转码；日志 slog 门面；`contrib/` 现有 authn-jwt、store-gorm、cache-redis、authz-casbin、observability-otlp；registry/config 桥接 kratos 生态。
- 演进 P0–P9 已落地（2026-08-30）：P0 三阶段停机、P1 泛型 Registry、P2 分页三策略/Mapper、P3 log/berrors/middleware、P4 codegen CLI、P5–P8 抽象层（P8 多租户 `store.RegisterTenant`+`Where.T`+写路径反射注入）、P9 授权归一化（`AuthzOption` resolver 模式，根治 REST/gRPC 双命名空间 RBAC）。
- 第二轮优化 P10–T1–P13、P11+S1+C1+R1+A1（2026-08-31 已落）：P10 `middleware/bundle` 双传输链序固化门面；T1 `appkit/effect.go` 效应账本（逆序回放、`UnregisterTenant` 对偶）；P11 三 contrib 晋升；S1 `appkit/capability.go`（Provides/Requires 启动期 fail-fast）；C1 `appkit/component.go`（stopAll 五阶段）；R1 `appkit/keywatch.go`（key 级订阅）；A1 `appkit/mount.go`（Mount/Unmount 可逆对偶+组件热插拔）。P12 codegen 已落（`gen app` 模板 + AppSpec 方言两步）。
- 设计文档 `docs/devel/zh-CN/架构优化路线.md`（剩余：管理面端点/R1 diff-apply）。

## bald 核心设计终态（决策记录）
- **bconfig**（09-03）：5 能力轴 Reader/Closer/Watcher/ValueWatcher/Decoder；`FallbackReader.WatchValue` 只认 ValueWatcher——注释已诚实声明，见《Bald 配置系统设计.md》§5。
- **路由/绑定**（08-28 终稿）：pkg/web 强绑 gin（`HandleJSON/Query/Uri/AllRequest[T,R]`，bindPB/writePB protojson）；引擎无关 `pkg/core` 已删；校验用 onexstack `pkg/validation`。文档《路由注册与绑定设计.md》。
- **错误模型**：`pkg/berrors`（零依赖不可变 Error）+ `berrors/grpcerr`（ToStatus/FromStatus）+ `berrors/httperr`（Code↔HTTP 双向映射）。已 Accepted。**决策⑧（09-10 已落地 `b37ce86`）**：HTTP 错误体 = google.rpc.Status JSON `{"code","message","details":[{"@type","reason","domain","metadata"}]}`，框架出口 `transport/web.ErrorResponse`（+`StatusOf` 单源构造器），与 gateway 转码**字节级同形**（空值语义对齐 protojson ""/{}/[]）；成功体裸 protojson（UseProtoNames snake_case）；否决 Kratos `{code,data,message}` 信封。**规则**：错误串只进 message 不进 reason；gin 面错误一律走 ErrorResponse/bindErr/writeBizErr，禁 c.JSON 直写。**包名（09-11 `b537495`）**：berrors 模块 `package errors`→`berrors`（与目录/模块同名），全仓 40 处 import 别名清零；不再遮蔽标准库 errors，同文件可共存（`errors.Is` stdlib、`berrors.Is` 按 Reason 跨栈匹配）。遗留：各域 reason 值风格统一、前端 axios 读 message+details[0].reason。
- **文档体系**：devel=`docs/devel/zh-CN/`（内部设计，决策式）；guide=`docs/guide/zh-CN/`（用户手册）。README.md 维护索引。
- **日志**：pkg/log 是 slog 适配层；轮转用 lumberjack。slog 路线并入《日志设计.md》§9。observability 中间件用 `pkg/middleware/tracing.go` 的 `LogTraceIDs(ctx)`（SpanContext 无效时随机 hex ID，no-op tracer 不再全零）。
- **布局判别**：bald 顶层目录（有独立 go.mod）=可独立发布的桥接/插件模块；`pkg/`=根模块核心包；pkg/{audit,authn,authz} 不移根目录。**transport/ 下 17 个子目录各自独立 module**（改依赖要动各自 go.mod）。
- **2026-09-10 增量**：吸收 bald-utils 三包——`pkg/id`（仅 NewGUIDv4/v7）、`pkg/stringcase`（snake_case 最小闭包）、`pkg/fieldmaskutil`（NestedMask+FieldMask）；transport/{tcp,webrtc} 改用根模块 pkg/id，根治 bald-utils 全家桶依赖。吸收标准=「是否是框架契约或框架运行的一部分」。

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

## 前后端联调与契约同步
- **writePB** 用 `protojson.MarshalOptions{UseProtoNames: true}` 输出 snake_case（前端 types 全按 snake_case 建模）；bindPB 两种命名都接受。
- **biz 纯 Go 结构被 `c.JSON` 序列化必须带 json tag**；proto 消息才走 writePB。
- 前端 axios `baseURL: VITE_BASE_URL || "/"` 必须绝对根路径（相对 URL 在嵌套路由页打不中 vite proxy）。
- 文件按 MIME 分桶（image→images/text 与文档→docs/其余→`file.bucket` 兜底桶——不是全局桶名）；MinIO 对象键=`目录/uuid32+扩展名`，原始文件名存 DB.FileName。
- **契约同步方案（09-10 定稿，文档《示例前后端契约同步设计.md》）**：按消费者扇出 buf 配置（Go/OpenAPI/前端 TS 各一份），生成物跨目录直出前端；**独立迁移前置约束：前后端必须合 monorepo**；第一步 buf v2 + TS 生成，OpenAPI 二步按需；hardcode 差异不可盲抄（bald 裸 protojson 无信封）。
- **错误响应契约（决策⑧）**：前端 axios 拦截器展示读 `message`、程序化读 `details[0].reason`（落地时替换现有 `errorData?.error` 双 key hack）。

## proto 生成 & 模块约定（易踩坑，务必遵守）
- **proto 生成只能走 Taskfile**：`task proto-config`（核心契约）、`task proto-example`（_example）。**勿手敲 `buf generate --path`**——部分子包静默不生成。改 .proto 后必须重跑再提交。
- **生成代码进仓库**：`pkg/conf/gen/go/**` 提交进 git（与 onexstack 不同）。
- **_example 是独立 module**：`_example/bald`，模块路径 `github.com/kalandramo/bald/example/bald`，`replace ../..`；验证须 `cd _example/bald && GOFLAGS=-mod=mod go build/test`。
- **appkit API 形状**：① `New(opts...)` variadic 在末位；② `MountComponent(ctx, comp)` 双参；③ `Servers(...)` 是 Option 非方法；④ 日志 `baldlog.SetLogger(baldlog.NewSlogLogger(baldlog.NewOptions()))`。
- **T10 后路径**：gRPC handler 在 `internal/apiserver/handler/grpc/`；e2e 在 `internal/apiserver/e2e/` 独立包；`RegisterRoutes(e, *BizSet)` 直传（BizSet 在 `internal/apiserver/bizset.go`）。
- gateway REST 路径复数（proto annotation 定 `/v1/secrets/{id}`）、gin 单数（`/v1/secret/:id`）——冒烟别用错。

## 工具链与发布
- 工具链：protoc 35.1、buf 1.57.2、go 1.26.5、go-task 3.53.1（Windows 跨平台）。`protoc-gen-defaults` pin 在 Taskfile vars（v0.0.2）。
- **警惕编造的 pseudo-version**：新增依赖前用 `go list -m -versions <mod>` 核实。
- **发布**：仓库公开 `github.com/kalandramo/bald`，tag `v0.1.0` 已推；contrib/* submodule 发布需 `contrib/<name>/v<N>` 单独打 tag；install 一律 `@latest`；sumdb 收录延迟数小时（404 正常，GONOSUMDB 绕过）。
- nacos 验证资产在 `_example/bald`（`//go:build nacos`）。

## 环境事实（双设备）
- **Windows/PowerShell**：写提交信息必须 `[IO.File]::WriteAllText(path, text, [Text.UTF8Encoding]::new($false))`——`Set-Content -Encoding UTF8` 带 BOM 污染 commit 标题；`git commit -m` 长中文多段信息可能静默不执行（用 `.git/COMMIT_MSG_TMP` + `git commit -F`，用后即删）；.NET File API 相对路径基于进程目录，批量改文件用绝对路径；gofmt -l 全列表现象 = autocrlf CRLF 噪音（实质检查需 LF 规范化）；go test 挂死先查残留 compile 进程（持有 build 缓存锁，Stop-Process 清掉）；`Invoke-RestMethod` 单元素数组渲染成标量（用 `Invoke-WebRequest` 取原始 Content）；本机无 gcc，SQLite 用 glebarez 纯 Go。
- **macOS**：无 `timeout`，冒烟用 `cmd & PID=$!; sleep N; kill -INT $PID`。
- **GBK 编码损坏**（go-bald-admin-web 曾踩）：Windows 编辑器 GBK 保存 `.vue/.ts` 导致中文乱码；纯 GBK 用 `[Text.Encoding]::GetEncoding('gb2312')` 转码，双重编码「锟斤拷」直接对照模板重写（逆向转码丢字不可逆）；扫描范围含根目录配置文件。
- 通用：`gofmt -l` 既有未过文件只格式化自己新建/改的文件；`golang-jwt/v5` ParseWithClaims 默认校验 exp。
