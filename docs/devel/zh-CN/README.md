## 开发手册

本目录收录 bald 框架的开发设计文档（中文）。

- [应用框架设计](./应用框架设计.md)：AppKit 生命周期、Server/Registrar 契约与编排。
- [配置中心设计](./配置中心设计.md)：配置四源优先级、业务配置层与 options 体系。
- [proto 配置契约设计](./proto 配置契约设计.md)：框架级配置用 Protobuf 作 schema（viper 仍是加载器）、渐进迁移路线与坑位记录。
- [服务端设计](./服务端设计.md)：HTTP / gRPC / Gateway Server 抽象与端口模型。
- [服务注册设计](./服务注册设计.md)：registry.Registrar 抽象、内存实现与 kratos 桥接。
- [日志设计](./日志设计.md)：pkg/log 极简契约、全局句柄、slog 后端与 OTel 可选桥接。
- [路由注册与绑定设计](./路由注册与绑定设计.md)：路由注册由业务用 gin 编写，pkg/web 提供强绑定 gin 的泛型绑定/校验/响应流水线，路径变量用 uri tag。
- [grpc-gateway 配置与 transcoding](./grpc-gateway 配置与 transcoding.md)：proto + google.api.http 注解、buf generate 生成、接线到 server.NewGRPCServerWithRegister / NewGatewayServer，gin 与 grpc-gateway 复用同一 biz 层。
- [错误模型设计](./错误模型设计.md)：pkg/berrors 零依赖核心错误类型，grpcerr（gRPC）/httperr（HTTP）两个对等桥接子包，不可变 builder 与双向传输闭环。
- [框架契约总览](./框架契约总览.md)：所有公开契约（接口/类型/函数/常量）速查表，按包分节，附桥接与依赖倒置接入说明。
- [数据存储设计](./数据存储设计.md)：数据访问层（DAL）设计——对比 go-crud（多引擎库）与 onexstack/store（GORM 封装）的取舍，给出 bald「核心定契约、引擎实现留独立子模块桥接」的方案与核心接口草案。
- [架构演进路线](./架构演进路线.md)：横向对比 bald/go-lulu/onexstack/go-crud/osbuilder 五个项目，提炼共识与差距，给出按优先级的架构演进路线（P0 生命周期 → P1 注册表 → P2 存储增强 → P3 横切 → P4 脚手架）。
