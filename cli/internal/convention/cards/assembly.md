---
id: assembly
title: 装配纪律
level: 强制
category: core
sources:
  - docs/devel/zh-CN/Bald 声明式装配设计.md
checks: []
---
# 装配纪律

Provider/Domain/变体的声明式装配规则。完整论证与 L1–L5 口径见母文档《Bald 声明式装配设计》。

- [assembly-r01] [强制] Provider 只有 v2 形态：Plan/Build 成对实现；Plan 必须是纯函数（不写文件、不发网络、不改全局、不读环境），Build 是唯一允许副作用的入口。
- [assembly-r02] [强制] Provider.Build 只在声明的段实际出现在契约时执行（段缺失=跳过，合法）；Build 内只读 ApplyScope.App 的资源面，不得写入。
- [assembly-r03] [强制] 资源访问唯一入口是类型化 appkit.Key[T].Resource(app)（Provider 导出自己的 Key，如 storegorm.SQL）；不按字符串取资源。
- [assembly-r04] [强制] 重复资源 Key 默认编译失败；覆盖必须恰一方标 Override: true 并给 Reason——双方都标 Override 同样编译失败。
- [assembly-r05] [强制] 装配排序只由 Phase → DependsOn 拓扑 → (Priority, Key) 决定，禁止手写排序表。
- [assembly-r06] [强制] 声明节点 Segments 只允许两种形态：BootstrapConfig 真实字段路径（如 server.http）或业务段 biz.<path>；拼错的段计划期报错。
- [assembly-r07] [强制] Domain.ApplyNode 与 Wire/Routes 互斥（双写装配期 fail-fast）；声明节点不得消费 server.grpc——服务注册只能走 Domain.Services。
- [assembly-r08] [强制] ReloadReconcile 只允许 bootstrap 阶段节点声明且必须有重建路径；database/cache/storage 一律 ReloadRestart。
- [assembly-r09] [强制] 未声明为变体的环境注入必须 WithAmbient 显式登记（Name 必填）；已声明变体的身份禁止再 WithAmbient；变体 go:build 实现与 AppSpec.variants 声明计划期对账。
- [assembly-r10] [建议] Opaque 节点守预算（默认 ≤3，超预算 WithOpaqueBudget 报错）；对外表述统一为「声明式装配（Lx）」并标注等级。
