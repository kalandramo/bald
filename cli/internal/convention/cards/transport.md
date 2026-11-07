---
id: transport
title: 传输层
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald 传输层设计.md
checks: []
---
# 传输层

transport.Server 契约 + 19 个后端 module（一后端一 module，用哪个引哪个）。协议形状判据见 judgments-r06。

- [transport-r01] [强制] 新传输后端必须实现 transport.Server 三方法契约（Start/Stop/Endpoint）并补编译期断言 var _ transport.Server = ...（既有 12 个后端缺断言是已知缺口，新代码不得延续）。
- [transport-r02] [强制] 需要服务注册的后端 Endpoint 必须经 transport.Extract(hostPort, ln) 解析通配符/动态端口（:0），禁止退回裸 ln.Addr().String()。
- [transport-r03] [强制] broker 家族后端 Endpoint() 返回 ""（客户端非监听服务，appkit.waitForEndpoints 跳过空值），禁止伪造地址。
- [transport-r04] [强制] 协议实现只承诺协议形状（判据 J6）：NewHTTPServer/NewGRPCServer 不接受探针参数、不注册框架路由与健康域逻辑。
- [transport-r05] [强制] transport/web 是 gin handler 层（非 Server）：多源绑定用 web.ShouldBindAll（URI→Query→JSON，后者覆盖前者），绑定完成后 handler 内一次 validator.Validate(ctx, req)——先 Bind 后一次性校验。
- [transport-r06] [强制] Stop(ctx) 必须使用调用方传入的 ctx（含超时），禁止用 context.Background() 调 Shutdown 丢弃超时。
- [transport-r07] [强制] 不复制已知缺陷：Endpoint() 原样返回配置地址不解析 :0、订阅簿部分失败不回滚——这两类形状新代码禁止同构复制。
- [transport-r08] [建议] 同协议独立实现 SessionManager 可接受，但必须满足不变量：observer 读写有锁、add/remove 幂等、rangeSessions 以 true=继续为语义。
- [transport-r09] [建议] gateway 用法：transport/gateway/contract.HTTPProvider 是 server.http 段的另一个 Provider（同段只跑一个面、段路径末者胜），准入判据在转码面构造期报错。
- [transport-r10] [参考] 6 个后端（tcp/websocket/graphql/sse/webrtc/mcp）Endpoint 映射 localhost 的语义尚未统一，属待设计项，评审不据此打回。
