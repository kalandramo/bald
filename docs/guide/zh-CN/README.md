## 用户手册

本手册介绍如何安装、使用 bald 框架构建并运行 Go 服务。

- [产品介绍](./introduction/README.md)
- [快速入门](./quickstart/README.md)
- [用 bald CLI 起步新服务](./用%20bald%20CLI%20起步新服务.md)：安装官方 CLI，用 `bald gen app --spec` 从零生成并运行一个可编译的服务骨架（含 AppSpec 参考、配置、生成物解读、常见问题）
- [Bald 错误处理](./Bald%20错误处理.md)：错误构造（11 工厂 + With\* 链 + sentinel 派生安全）、按 Reason 匹配、HTTP/gRPC 边界收口接线（拦截器最外层/客户端还原）、响应体形状与 FAQ
- [Bald 日志使用](./Bald%20日志使用.md)：日志默认行为（零代码 slog）、启用其他后端三步（引依赖 + 注册 + 配置声明）、五后端速查、多后端广播与报错解读
- 最佳实践（待补充）
