## 开发手册

本目录收录 bald 框架的设计文档。目标是：**一个新接手的人，只读本目录就能建立正确的心智模型，并在改动前知道该看哪几篇。**

### 怎么读

| 你关心的事 | 先读 |
|---|---|
| 框架整体怎么跑起来 | [应用框架设计](./应用框架设计.md) → [Bald Bootstrap 设计](./Bald%20Bootstrap%20设计.md) |
| 装配系统（计划、阶段、Provider、收敛） | [Bald 声明式装配设计](./Bald%20声明式装配设计.md) |
| 某个包对外承诺了什么 | [框架契约总览](./框架契约总览.md) |
| 「保留还是删除」「要不要 fail-closed」这类判断 | [设计判据](./设计判据.md) |
| 发版、看某版改了什么 | [多模块发布与变更日志](./多模块发布与变更日志.md) |
| 新增一个能力后端怎么接进装配 | guide 的 [Provider 接入](../../guide/zh-CN/Provider%20接入%20-%20contract.AppProvider%20与段%20Provider.md) |

### 装配与配置

- [应用框架设计](./应用框架设计.md)：AppKit 的进程模型——阶段化的启动/停机、效应账本（T1）、能力声明（S1）、组件生命周期、运行期挂载与配置协调；Server/Registrar/Component 三类契约的边界。
- [Bald 声明式装配设计](./Bald%20声明式装配设计.md)：装配系统的核心设计——`Plan` IR（纯函数、可打印、可哈希）、Provider v2 的 `Plan`/`Build` 分离、`Priority`/`DependsOn`/`TeardownAfter` 取代手工排序、`Key[T]` 类型化资源面、节点级 `ReloadPolicy` 与 `Drift`、L1–L5 声明式等级与验收口径；末章是 Q1–Q9 的决策记录（路由清单、REST 绑定生成、变体、biz 段、自定义接线钩子）。
- [Bald Bootstrap 设计](./Bald%20Bootstrap%20设计.md)：bootstrap 只做两件事——契约装载内核与纯构造层；不负责段的消费顺序（那是 appkit 的计划），也不持有全局注册表。
- [Bald 配置系统设计](./Bald%20配置系统设计.md)：配置系统全景——源抽象、proto 契约、层合并与装配；源层与契约层的深展开见下面两篇。
- [Bald 配置源层设计](./Bald%20配置源层设计.md)：字节进字节出的源层抽象（Reader/ValueWatcher/Decoder 能力轴 + 类型断言发现）、FallbackReader 级联回退、十个 provider 的双模式构造与 watch 语义。
- [Bald 配置契约设计](./Bald%20配置契约设计.md)：bconf 的 proto 契约布局与四 API（默认值/合并桥接/形状校验/flag 绑定）、coerce 类型缓冲层。
- [AppKit 日志装配设计](./AppKit%20日志装配设计.md)：为什么日志要走两阶段引导（契约装载前就要可用）、`logger` 段 Provider 的分派语义、热更新时旧后端的冲刷顺序。
- [代码生成工具设计](./代码生成工具设计.md)：`bald gen proto/store/app/domain` 的定位与边界——生成物只依赖 `AppSpec + proto`，`--check` 零写入可判；owned/seeded/user 三态与 seeded 钩子。
- [路由注册与绑定设计](./路由注册与绑定设计.md)：路由由业务用 gin 写、`transport/web` 提供绑定/校验/响应流水线；路径变量用 uri tag，REST 绑定可由 `gen domain` 从注解生成。

### 请求路径

- [Bald 传输层设计](./Bald%20传输层设计.md)：一条极窄契约 `transport.Server` + `transport.Extract`；各传输各自独立 module，监听与非监听服务（broker 家族）的差别。
- [Bald 中间件设计](./Bald%20中间件设计.md)：gin/gRPC 双传输的横切装配与链序固化——Audit 夹在 Authn 与 Authz 之间的双约束、Error 最外层收口、shared 子包承载传输无关核心、显式注入不吃全局。
- [认证与授权抽象设计](./认证与授权抽象设计.md)：`pkg/authn`（Authenticator/AuthClaims）与 `pkg/authz`（Authorizer）的双接口、引擎桥接外置，P9 归一化让 REST/gRPC 共用同一策略空间。
- [Bald 错误模型设计](./Bald%20错误模型设计.md)：`berrors` 传输中立错误（Code/Reason/Message/Details + cause/栈）与 grpc/HTTP 对等桥接；不可变 builder 与按 Reason 匹配。
- [校验设计](./校验设计.md)：按请求类型名分发的 `Validate` 约定与轻量规则校验，与 proto `buf.validate` 注解互补。
- [上下文契约设计](./上下文契约设计.md)：`pkg/contextx` 请求级元信息的五个标准键与统一存取。
- [Bald 字段掩码设计](./Bald%20字段掩码设计.md)：`pkg/fieldmaskutil` 对 proto FieldMask 的过滤/剪除/覆写/校验，两层 API 与四类字段形态。
- [grpc-gateway 配置与 transcoding](./grpc-gateway%20配置与%20transcoding.md)：一份 proto 同时定义 gRPC 与 REST 映射；`server.http.driver=grpc-gateway` 时网关就是 `server.http` 端口，gin 与网关共用 biz 层。

### 数据与存储

- [Bald 存储设计](./Bald%20存储设计.md)：`pkg/store` 的三层契约（`Store[T]` 门面 / `Queryable[T]` 后端契约 / `DBProvider[T]` 句柄注入）、`Where` 双通道（隔离条件不可被业务 OR 吞掉）、四策略分页、多租户读写对称闭环、写操作幂等语义、数据权限布尔树翻译、inmemory 实现。
- [Bald 缓存设计](./Bald%20缓存设计.md)：契约层守住 `[]byte` 边界、不放泛型；`cache/loadable` 用 singleflight 合并回源并 best-effort 回填；后端进契约装配、组合器由代码构造。
- [Bald CRUD 桥接设计](./Bald%20CRUD%20桥接设计.md)：bald 身份体系到 bald-crud viewer 体系的桥接、互斥三态视图、五级数据范围到 `storev1.FilterExpr` 的 fail-closed 翻译。
- [Bald 对象存储设计](./Bald%20对象存储设计.md)：`oss/minio` 与 `oss/s3` 的类型化门面 + `SDK()` 双出口；不抽公共接口的理由与契约映射单独成包。
- [Bald 编解码设计](./Bald%20编解码设计.md)：`encoding` 的 `Codec` 三方法契约与全局注册表、「名字取用 + 代码装配」两段式、13 个 module 的布局与特殊签名格式的桥接。

### 可观测、旁路与韧性

- [Bald 日志设计](./Bald%20日志设计.md)：瘦契约（6 方法）+ nop 默认 + 装配期注入；bslog 适配器、五个远端后端独立 module、`logger.backends` 多后端广播与出口统一脱敏。
- [Bald 指标设计](./Bald%20指标设计.md)：请求级 Recorder（semconv 对齐的 HTTP/gRPC 指标 + 审计三元组投影）与传输级三原语两套体系的分工；装配路径与暴露面归属。
- [Bald 审计设计](./Bald%20审计设计.md)：旁路契约（`Auditor.Record` + 八字段事件）、四类埋点来源（请求中间件/认证失败/协调器/组件热插拔）、三层「永不阻断业务」防线、log 内置 + store/stream 桥接后端与 fallback 降级。
- [Bald 健康检查设计](./Bald%20健康检查设计.md)：三态状态机与并发聚合、readiness/liveness 双端点分离、内置检查器；零第三方依赖。
- [Bald 重试设计](./Bald%20重试设计.md)：`Backoff`/`Jitter`/`Classifier` 三组正交策略、`Retrier.Do` 单入口、三条终止线与 `errors.Join` 包装。
- [Bald 限流设计](./Bald%20限流设计.md)：`Limiter` 三方法契约与 tokenbucket/bbr/sentinel 三实现；`InflightLimiter` 为何拆开。
- [Bald 熔断设计](./Bald%20熔断设计.md)：三态契约与成对上报；sres/vegas/hystrix/sentinel 四实现的判决依据与已知边界。

### 集成

- [Bald 消息代理设计](./Bald%20消息代理设计.md)：`Message.Body` 装任意类型、`Binder` 负责落地、三层类型化糖；四个后端独立 module、双装配路径（transport Server 包装 / `broker.<backend>` 段 Provider）。
- [Bald 注册中心设计](./Bald%20注册中心设计.md)：契约零依赖的 Registrar/Discovery/Watcher、后端独立 module、单选的 `registry.type` 与各后端 `AppProvider`；注册/反注册生命周期时序。
- [Bald 工作流设计](./Bald%20工作流设计.md)：`workflow/argo` 手写 REST 客户端与生命周期方法；四段声明只实现 argo，未实现段由 L3 覆盖检查 fail-closed。
- [Bald 智能模型设计](./Bald%20智能模型设计.md)：openai/langchaingo/eino 三个独立 module，能力面刻意不统一；契约段三声明 = 三实现。
- [Bald Cobra MCP 桥接设计](./Bald%20Cobra%20MCP%20桥接设计.md)：`cobramcp`（工具从哪来）与 `transport/mcp`（工具怎么对外）的分层、进程内/子进程双模型、宿主装配入口 `appkit.WithExtraServerFunc`。
- [示例前后端契约同步设计](./示例前后端契约同步设计.md)：proto 单一真相源 + buf 按消费者扇出（Go/TS/OpenAPI），消灭契约双写。

### 发布与维护

- [多模块发布与变更日志](./多模块发布与变更日志.md)：`bald release` 的 plan / sync / changelog / tag 四步、版本推导规则、tag 守卫与一次完整发布的命令序列。

### 质量与记录

- [框架契约总览](./框架契约总览.md)：所有公开契约（接口/类型/函数/常量）速查表，按包分节，附桥接与依赖倒置说明。
- [设计判据](./设计判据.md)：跨模块复用的裁决原则（零调用与死代码的区分、重复税、身份缺失 fail-closed、默认超时、协议只承诺协议形状等），新代码按此判断。
- [性能基准](./性能基准.md)：装配期与运行期关键路径的基准口径与数字。
- [测试工具设计](./测试工具设计.md)：`pkg/testkit` 的 e2e 复用工具。
- [架构演进路线](./架构演进路线.md)、[架构优化路线](./架构优化路线.md)：第一轮（P0–P9）与第二轮（P10–A1）的**历史决策记录**，用于解释「为什么现在长这样」；描述现状时以各专题文档为准。
