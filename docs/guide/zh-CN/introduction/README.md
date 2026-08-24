## 产品介绍

### bald 是什么

**bald** 是一个轻量的 Go 服务框架，融合三方成熟设计精华，帮助你用最少的样板代码
构建同时承载 HTTP / gRPC / gRPC-Gateway 的多协议服务，并内建优雅停机、服务注册、
多源配置等生产级能力。

设计来源：

- **onexstack/pkg/app**：启动期 Options + 配置理念（`--config` / viper 由调用方注入）。
- **Kratos**：`transport.Server` 契约与 `registry.Registrar` 接口（可插拔复用）。
- **go-lulu (`wind`)**：自研 App 层精髓——errgroup 并发启停、优雅停机防坑、
  崩溃级联停止、Run 防重入、可观察通道、`Endpoint()` 动态端口注册。

### 核心特性

| 特性 | 说明 |
| --- | --- |
| 统一 Server 契约 | `Server = Start / Stop / Endpoint`，HTTP / gRPC / Gateway 共用一份契约。 |
| 多协议同进程 | 一个 `AppKit` 可并发编排多个 Server，共享生命周期。 |
| 优雅停机 | 收到 SIGINT/SIGTERM 或 ctx 取消时，先反注册再优雅停机，避免流量打到已停服务。 |
| 动态端口注册 | `Endpoint()` 在 `Start` 后返回真实地址，`:0` 动态端口也能正确注册到服务发现。 |
| 可插拔注册中心 | 内置内存实现（开发/测试），通过桥接复用 kratos 生态的 etcd / consul / nacos。 |
| 多源配置 | 命令行 flag > 环境变量 > 本地文件 > 远程配置中心，支持热更新。 |
| 生命周期钩子 | `BeforeStart` / `AfterStart` / `BeforeStop` / `AfterStop` 精细控制。 |

### 适用场景

- 需要同时对外提供 HTTP REST 与 gRPC 接口的服务。
- 希望用 gRPC-Gateway 复用同一份 proto 同时暴露 REST 与 gRPC。
- 需要接入 etcd / consul / nacos 等服务发现，但不想被具体客户端绑死。
- 重视优雅停机、崩溃级联、动态端口等生产级细节。

### 架构概览

```
AppKit（组合层）
  ├── Server 契约：HTTP / gRPC / Gateway（并发启停 + 优雅停机）
  ├── Registrar 抽象：内存 / kratos(etcd|consul|nacos)
  └── 配置加载：flag > env > 本地文件 > 远程配置中心（热更新）
```

更详细的设计文档见 [开发手册](../devel/zh-CN/README.md)。
