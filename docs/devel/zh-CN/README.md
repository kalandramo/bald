## 开发手册

本目录收录 bald 框架的开发设计文档（中文）。

- [应用框架设计](./应用框架设计.md)：AppKit 生命周期、Server/Registrar 契约与编排。
- [配置中心设计](./配置中心设计.md)：配置四源优先级、业务配置层与 options 体系。
- [服务端设计](./服务端设计.md)：HTTP / gRPC / Gateway Server 抽象与端口模型。
- [服务注册设计](./服务注册设计.md)：registry.Registrar 抽象、内存实现与 kratos 桥接。
- [日志设计](./日志设计.md)：pkg/log 极简契约、全局句柄、slog 后端与 OTel 可选桥接。
- [路由注册与绑定设计](./路由注册与绑定设计.md)：路由注册由业务用 gin 编写，pkg/web 提供强绑定 gin 的泛型绑定/校验/响应流水线，路径变量用 uri tag。
- [grpc-gateway 配置与 transcoding](./grpc-gateway 配置与 transcoding.md)：proto + google.api.http 注解、buf generate 生成、接线到 server.NewGRPCServerWithRegister / NewGatewayServer，gin 与 grpc-gateway 复用同一 biz 层。
- [错误模型设计](./错误模型设计.md)：pkg/errors 零依赖核心错误类型与 gRPC 桥接子包，不可变 builder 与双向传输闭环。
