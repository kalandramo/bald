---
id: appspec
title: AppSpec 声明纪律
level: 强制
category: codegen
sources:
  - docs/devel/zh-CN/应用框架设计.md
  - docs/devel/zh-CN/代码生成工具设计.md
checks: []
---
# AppSpec 声明纪律

AppSpec 是应用装配的 proto 单一真相源；本卡管「声明怎么写、装配怎么接」。生成树纪律见 codegen 卡。

- [appspec-r01] [强制] 生成器只做装配不做业务：业务路由/handler/gRPC 注册/wire 注入/组件工厂留 TODO 填充点，不由模板表达。
- [appspec-r02] [强制] 全部 Option 必须在 internal/bootstrap/assemble.gen.go 的 Options() 返回前 append 完、MustServe 一次性拿满；Options() 返回后再 append 是往没人看的切片里写。
- [appspec-r03] [强制] 服务器与组件一律作为 Option 传进装配链（WithHTTPContract/WithGRPC/mech.Components）；禁止装配期调 MountComponent（装配期返回 ErrNotRunning）。
- [appspec-r04] [强制] 生产 main 默认入口 appkit.MustServe；NewApp 不跑计划、不跑 L3、不消费 Domains——需要这些语义不得选它。
- [appspec-r05] [强制] app.name 必填：它决定 env 前缀与多环境配置文件名。
- [appspec-r06] [强制] AppSpec 表达不了的接线只写三个 seeded 钩子：customOptions(role)（internal/bootstrap/custom.go）、customVariantOpts(name) 与 customSubcommands()（cmd/<app>/app/custom.go）；钩子只能追加/覆盖 Option，不能新增生成物或改 plan 形状。
- [appspec-r07] [强制] 认证姿态是显式一行：seeded 链里写 bundle.Public() 或 bundle.Authn(...)；模板不替业务宣布「本服务公开」。
- [appspec-r08] [强制] 网关角色不挂 gin 链：登记 gatewaycontract.HTTPProvider + WithHTTP(nil)、不登记 WithHTTPContract，且该角色契约不写 server.http.middleware 段。
- [appspec-r09] [建议] WithHTTP 与 WithHTTPContract 互斥（双写报错）；要按契约构造中间件链就选 Contract 形态——自建形态（WithHTTP(router)）对 server.*.middleware 段不生效。
