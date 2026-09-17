## 开发手册

本目录收录 bald 框架的开发设计文档（中文）。

- [应用框架设计](./应用框架设计.md)：AppKit 生命周期（五阶段停机）、Server/Registrar/Component 契约、效应账本/能力声明/运行期挂载。
- [六域 Registry 迁入 bootstrap](./六域%20Registry%20迁入%20bootstrap.md)：bootstrap 与 appkit 合并评估（不可行：循环依赖 + 职责正交）与折中落地——Database/Cache/Storage/Ai/Workflow/Broker 六域 Registry 自 appkit 迁 bootstrap（构造期归装配层），With\* Option 与 AppKit 访问器留 appkit（运行期归编排层），含新插件归位判别规则。
- [配置中心设计](./配置中心设计.md)：配置四源优先级、proto 配置契约层（`pkg/options` 已废弃）。
- [Bald 配置系统设计](./Bald%20配置系统设计.md)：配置系统全景——源抽象（Reader/ValueWatcher/FallbackReader + 10 provider）、proto 配置契约、bootstrap 装配（Registry/层优先级/Build 回滚）；源层与契约层的深展开见各自专属篇。
- [Bald 配置源层设计](./Bald%20配置源层设计.md)：bconfig module 专属展开——字节进字节出的源层抽象（Reader/ValueWatcher/Decoder 能力轴 + 类型断言发现）、FallbackReader 级联回退与重算语义（事件值不可信）、10 个 provider 矩阵（双模式构造/推送与轮询分级/watch 父目录）、编写纪律 checklist。
- [Bald 配置契约设计](./Bald%20配置契约设计.md)：bconf module 专属展开——17 proto 契约布局、四 API（NewBootstrap 默认值/UnmarshalMap 合并桥接/Validate 形状校验/BindFlags 描述符 flag 绑定）、coerce 类型缓冲层、三坑防御（Duration/repeated/presence）、契约演进 v0.1.0→v0.5.0（含 v0.5.0 唯一化瘦身）。
- [服务端设计](./服务端设计.md)：HTTP / gRPC / Gateway Server 抽象与端口模型。
- [Bald 注册中心设计](./Bald%20注册中心设计.md)：注册中心域的设计决策与取舍——契约零依赖（Registrar/Discovery/Watcher + inmemory）、后端各自独立 module（`registry/<backend>` 直连 SDK）、`RegistrarRegistry` 显式注册与 `registry.type` 单选分发、注册/反注册生命周期时序；含下放布局论证、breaking 代价、迁移与发版记录。
- [Bald 健康检查设计](./Bald%20健康检查设计.md)：health module 专属展开——三态状态机（Unknown/Up/Down）、Checker 接口与 PingFunc 零适配、Health 并发聚合（Down 传染 > Unknown 传染、双层超时兜底）、readiness/liveness 双端点分离（Down→503、liveness 恒 200 不级联重启）、TCP/HTTP/All/Any 内置检查器；零第三方依赖纯标准库（2026-09-06 自 go-wind 移植；tag `health/v0.1.0` 已发，装配与归属见下条）。
- [Bald 健康检查装配设计](./Bald%20健康检查装配设计.md)：探针与就绪的归位——协议实现不含健康/就绪（HTTP 探针注册、gRPC 就绪轮询、`ReadinessFunc` 参数全部移除；gRPC 标准健康服务保留但只注册不判断）、能力落 `health` + 装配层（`bootstrap.NewGRPCHealthServer` / `appkit.WithHealth` 一行默认装配 + 业务自组装两条路）、reflection 外移装配层；含运维风险（默认无探针 → K8s 404）与破坏面清单。
- [Bald 日志设计](./Bald%20日志设计.md)：log 契约（6 方法接口/全局句柄/nop 默认/ctx 属性流/MultiLogger）、bslog 适配器（多输出/lumberjack 轮转/脱敏/OTel 桥接）、五远端后端独立 module + contract 模式、装配层（两级工厂/默认纯函数 + 教学报错/两阶段/热更新）、`logger.backends` 多后端广播；附录含 gookit/slog 评估决策与桥接适配器预留（并入自原《日志平面接口设计》）。
- [AppKit 日志装配设计](./AppKit%20日志装配设计.md)：AppKit 侧日志装配策略——两级工厂分发（WithLogRegistry 机制全权 / 默认纯函数零依赖）的使用场景、nil 双语义（机制层 fail-fast vs 阶段 A 回退）、装饰器 deco 生效范围分级、四分派、脱敏单层保证的结构性机制。
- [路由注册与绑定设计](./路由注册与绑定设计.md)：路由注册由业务用 gin 编写，pkg/web 提供强绑定 gin 的泛型绑定/校验/响应流水线，路径变量用 uri tag。
- [grpc-gateway 配置与 transcoding](./grpc-gateway%20配置与%20transcoding.md)：proto + google.api.http 注解、buf generate 生成、接线到 server.NewGRPCServerWithRegister / NewGatewayServer，gin 与 grpc-gateway 复用同一 biz 层。
- [Bald 错误模型设计](./Bald%20错误模型设计.md)：berrors module 专属展开——传输中立 Error（Code/Reason/Message/Details + cause/栈）、不可变 builder、按 Reason 匹配的 Is、构造即捕获栈；grpcerr（gRPC 双向 + ErrorInfo）/httperr（17 码 HTTP 投影）对等桥接子包；google.rpc.Status JSON 三面一份契约；决策①~⑨ 含 2026-09-15 合并评估否决（并入自原《错误模型设计》）。
- [认证与授权抽象设计](./认证与授权抽象设计.md)：P7 双接口——pkg/authn 认证抽象（Authenticator/AuthClaims）+ pkg/authz 授权抽象（Authorizer），零引擎耦合、桥接子模块外置，P9 归一化使 REST/gRPC 共用同一策略空间。
- [Bald 审计设计](./Bald%20审计设计.md)：审计域统一设计——pkg/audit 旁路契约（Auditor/AuditEvent/三层防线）、四类埋点来源（请求中间件/认证失败 D3 盲区/协调器/组件热插拔）、三后端（log 内置 + contrib/audit-{store,stream} 桥接与 fallback 降级）、契约/协调器双轨装配；并入自原《审计抽象设计》，含严格代码比对发现的 Time 契约缺口（已修复：store/stream 后端记录时兜底）。
- [Bald 指标设计](./Bald%20指标设计.md)：指标域统一设计——pkg/metrics 请求级 Recorder（semconv v1.43.0 对齐：http.server.request.duration / rpc.server.call.duration / http.server.active_requests + 正交审计指标 bald_audit_events_total，含装配链与惰性 instruments 修复史）+ 顶层 metrics/ 传输级三原语（Metrics 接口 + prometheus/otel/datadog 三后端，六传输埋点清单、零 tag 未发版事实、缺陷清单 D1-D8 与发版 checklist）；「两套体系总览」给入口判断。
- [校验设计](./校验设计.md)：pkg/validation 按请求类型名分发的 Validate 方法约定 + 规则式轻量校验，与 proto buf.validate 注解互补。
- [上下文契约设计](./上下文契约设计.md)：pkg/contextx 请求级元信息五个标准键（user/username/trace_id/request_id/tenant_id）的统一存取。
- [测试工具设计](./测试工具设计.md)：pkg/testkit e2e 复用工具的收编（P13），如 FreeAddr。
- [代码生成工具设计](./代码生成工具设计.md)：cmd/bald 官方开发工具 CLI（gen proto/store/app）、AppSpec 方言与 Requirement 结构化、模板装配纪律（P0）、生成物骨架边界、消费者 module 测试策略。
- [框架契约总览](./框架契约总览.md)：所有公开契约（接口/类型/函数/常量）速查表，按包分节，附桥接与依赖倒置接入说明。
- [数据存储设计](./数据存储设计.md)：数据访问层（DAL）设计——对比 go-crud（多引擎库）与 onexstack/store（GORM 封装）的取舍，给出 bald「核心定契约、引擎实现留独立子模块桥接」的方案与核心接口草案。
- [架构演进路线](./架构演进路线.md)：横向对比 bald/go-lulu/onexstack/go-crud/osbuilder 五个项目，提炼共识与差距，给出按优先级的架构演进路线（P0-P9 第一轮，均已完成）。
- [架构优化路线](./架构优化路线.md)：第二轮优化（P0–P9 之后）——基于 GoWind 设计哲学对比与 Cordis 论文《A Programming Paradigm for Spatiotemporal Composability》（时空可组合性）的诊断，提出 P10 bundle 门面 / P11 contrib 晋升 / T1 效应账本 / S1 能力解析 / C1 Component 统一 / A1 运行期挂载 / R1 key 级订阅，附防漂移清单与 agent-native 远期方向。
- [go-wind-admin 业务移植计划](./go-wind-admin%20业务移植计划.md)：将 go-wind-admin 精选业务子集（租户/用户/角色权限/菜单/字典/审计日志/文件/认证扩展 + Nacos）移植到独立仓库 [kalandramo/bald-admin](https://github.com/kalandramo/bald-admin)（前后端 monorepo），接入云端真实依赖（PostgreSQL/Redis/MinIO/Nacos/OTLP），含业务→bald 能力验证点映射、云端依赖确认清单、配置结构与里程碑 T0–T9（含 T9 可观测性契约化收官）。
- [示例前后端契约同步设计](./示例前后端契约同步设计.md)：proto 单一真相源 + buf v2 按消费者扇出（Go/TS/OpenAPI 各一份 gen 配置）——TS 客户端生成替换手写 API 层，消灭契约双写；含与 go-wind-admin 的差异点（响应信封/transport 保留）、独立时合 monorepo 的前置约束。已落地（2026-09-11，实施差异见文首状态行）。
