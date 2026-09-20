## 用户手册

本手册介绍如何安装、使用 bald 框架构建并运行 Go 服务。

- [产品介绍](./introduction/README.md)
- [快速入门](./quickstart/README.md)
- [用 bald CLI 起步新服务](./用%20bald%20CLI%20起步新服务.md)：安装官方 CLI，用 `bald gen app --spec` 从零生成并运行一个可编译的服务骨架（含 AppSpec 参考、配置、生成物解读、常见问题）
- [Bald 错误处理](./Bald%20错误处理.md)：错误构造（11 工厂 + With\* 链 + sentinel 派生安全）、按 Reason 匹配、HTTP/gRPC 边界收口接线（拦截器最外层/客户端还原）、响应体形状与 FAQ
- [Bald 配置系统](./Bald%20配置系统.md)：四源优先级链（flag > env > 本地文件 > 契约源层 > 远程桥）、配置键即 proto 契约字段路径、环境变量命名规则（应用名前缀）、多环境文件名查找、两条装配路径的选项差异（FromBootstrap / New）、热更新四种粒度与支持范围、报错解读与排错清单
- [Bald 日志使用](./Bald%20日志使用.md)：日志默认行为（零代码 slog）、启用其他后端三步（引依赖 + 注册 + 配置声明）、五后端速查、多后端广播与报错解读
- [Bald 健康检查](./Bald%20健康检查.md)：K8s 双探针接线（readiness 依赖检查 Down→503 / liveness 恒 200）、五种检查器形态速查（PingFunc/TCP/HTTP/All/Any）、三态聚合规则与超时对齐、自定义 Checker
- [Bald 指标埋点使用](./Bald%20指标埋点使用.md)：两套指标体系选型（请求级 Recorder vs 传输级三原语）、三后端速查（prometheus/otel/datadog）、transport WithMetrics 注入与既有埋点清单、自定义埋点与跨后端边界（Gauge 语义分歧、MeterProvider 互斥）
- [Bald 审计使用](./Bald%20审计使用.md)：审计后端三步启用（log 内置零注册 / store、stream 引依赖注册）、自动埋点四类与中间件挂载、auditor 绑定时机（构造快照语义与动态转发器）、运行期热切协调器、报错解读与迁移指引（bconf v0.6.0 type → v0.7.0 backends）
- [Bald 缓存使用](./Bald%20缓存使用.md)：缓存后端三步启用（local/redis 可并存装配）、8 方法契约语义（[]byte 边界 / GetMulti 对齐规则 / SetNX 锁原语）、loadable 回源组合器（singleflight 合并防击穿 + best-effort 回填 + 业务级负缓存）、取实例时机与报错解读
- [Bald Cobra MCP 桥接使用](./Bald%20Cobra%20MCP%20桥接使用.md)：把 Cobra CLI 接成 MCP 服务的操作手册——四种形态选型（子进程 stdio/SSE/REST + 进程内 SSE）各带完整样例、自带 `mcp start/stream/rest/tools` 与编辑器 `vscode/cursor enable` 参数速查、工具打磨（命名/位置参数注解/MCP 注解/扁平 schema）、selector 收窄暴露面与 middleware、SSE/REST 认证（自定义中间件 + 静态 token + OAuth 元数据）、编辑器配置路径与写入/备份陷阱、报错排查
- 最佳实践（待补充）
