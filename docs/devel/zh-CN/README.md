## 开发手册

本目录收录 bald 框架的开发设计文档（中文）。

- [应用框架设计](./应用框架设计.md)：AppKit 生命周期（五阶段停机）、Server/Registrar/Component 契约、效应账本/能力声明/运行期挂载。
- [配置中心设计](./配置中心设计.md)：配置四源优先级、proto 配置契约层（`pkg/options` 已废弃）。
- [proto 配置契约设计](./proto 配置契约设计.md)：框架级配置用 Protobuf 作 schema（viper 仍是加载器）、渐进迁移路线与坑位记录。
- [服务端设计](./服务端设计.md)：HTTP / gRPC / Gateway Server 抽象与端口模型。
- [服务注册设计](./服务注册设计.md)：registry.Registrar 抽象、内存实现与 kratos 桥接。
- [日志设计](./日志设计.md)：pkg/log 极简契约、全局句柄、slog 后端与 OTel 可选桥接；§9 含能力边界与已否决选项（lumberjack 轮转已落地）。
- [路由注册与绑定设计](./路由注册与绑定设计.md)：路由注册由业务用 gin 编写，pkg/web 提供强绑定 gin 的泛型绑定/校验/响应流水线，路径变量用 uri tag。
- [grpc-gateway 配置与 transcoding](./grpc-gateway%20配置与%20transcoding.md)：proto + google.api.http 注解、buf generate 生成、接线到 server.NewGRPCServerWithRegister / NewGatewayServer，gin 与 grpc-gateway 复用同一 biz 层。
- [错误模型设计](./错误模型设计.md)：pkg/berrors 零依赖核心错误类型，grpcerr（gRPC）/httperr（HTTP）两个对等桥接子包，不可变 builder 与双向传输闭环。
- [认证与授权抽象设计](./认证与授权抽象设计.md)：P7 双接口——pkg/authn 认证抽象（Authenticator/AuthClaims）+ pkg/authz 授权抽象（Authorizer），零引擎耦合、桥接子模块外置，P9 归一化使 REST/gRPC 共用同一策略空间。
- [审计抽象设计](./审计抽象设计.md)：pkg/audit 零后端耦合的旁路审计——Auditor/AuditEvent/全局注入点，审计三元组与 P9 归一化同源，永不阻断业务请求。
- [指标抽象设计](./指标抽象设计.md)：pkg/metrics 可观测性第三支柱——otel 默认 no-op、与审计同源 emit，覆盖量/延迟/错误率三要素。
- [校验设计](./校验设计.md)：pkg/validation 按请求类型名分发的 Validate 方法约定 + 规则式轻量校验，与 proto buf.validate 注解互补。
- [上下文契约设计](./上下文契约设计.md)：pkg/contextx 请求级元信息五个标准键（user/username/trace_id/request_id/tenant_id）的统一存取。
- [测试工具设计](./测试工具设计.md)：pkg/testkit e2e 复用工具的收编（P13），如 FreeAddr。
- [代码生成工具设计](./代码生成工具设计.md)：cmd/bald 官方开发工具 CLI（gen proto/store/app）、AppSpec 方言与 Requirement 结构化、模板装配纪律（P0）、生成物骨架边界、消费者 module 测试策略。
- [框架契约总览](./框架契约总览.md)：所有公开契约（接口/类型/函数/常量）速查表，按包分节，附桥接与依赖倒置接入说明。
- [数据存储设计](./数据存储设计.md)：数据访问层（DAL）设计——对比 go-crud（多引擎库）与 onexstack/store（GORM 封装）的取舍，给出 bald「核心定契约、引擎实现留独立子模块桥接」的方案与核心接口草案。
- [架构演进路线](./架构演进路线.md)：横向对比 bald/go-lulu/onexstack/go-crud/osbuilder 五个项目，提炼共识与差距，给出按优先级的架构演进路线（P0-P9 第一轮，均已完成）。
- [架构优化路线](./架构优化路线.md)：第二轮优化（P0–P9 之后）——基于 GoWind 设计哲学对比与 Cordis 论文《A Programming Paradigm for Spatiotemporal Composability》（时空可组合性）的诊断，提出 P10 bundle 门面 / P11 contrib 晋升 / T1 效应账本 / S1 能力解析 / C1 Component 统一 / A1 运行期挂载 / R1 key 级订阅，附防漂移清单与 agent-native 远期方向。
