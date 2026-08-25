## 开发手册

本目录收录 bald 框架的开发设计文档（中文）。

- [应用框架设计](./应用框架设计.md)：AppKit 生命周期、Server/Registrar 契约与编排。
- [配置中心设计](./配置中心设计.md)：配置四源优先级、业务配置层与 options 体系。
- [服务端设计](./服务端设计.md)：HTTP / gRPC / Gateway Server 抽象与端口模型。
- [服务注册设计](./服务注册设计.md)：registry.Registrar 抽象、内存实现与 kratos 桥接。
- [日志设计](./日志设计.md)：pkg/log 极简契约、全局句柄、slog 后端与 OTel 可选桥接。
- [路由注册与绑定设计](./路由注册与绑定设计.md)：pkg/web 路由分组与中间件、Handler.ApplyTo 注册、泛型绑定流水线与统一响应。
- [错误模型设计](./错误模型设计.md)：pkg/errors 零依赖核心错误类型与 gRPC 桥接子包，不可变 builder 与双向传输闭环。
