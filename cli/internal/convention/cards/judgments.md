---
id: judgments
title: 设计判据（J1–J9）
level: 强制
category: core
sources:
  - docs/devel/zh-CN/设计判据.md
checks: []
---
# 设计判据（J1–J9）

bald 跨模块复用的裁决原则：设计文档做「保留 / 删除 / 收敛 / fail-closed」类决定时引用这些条目。
遇到取舍问题，先对照本卡；完整论证与已落地形态见母文档《设计判据》。

- [judgments-r01] [强制] 框架能力零调用不是删除依据：导出 API 零调用且无测试 → 补契约测试，不删除；只有无调用且无测试的未导出内部代码才删。
- [judgments-r02] [强制] 重复税：同一不变量出现第二份同形实现时，抽成单一闸门（单一真相源），不「记得修两处」。
- [judgments-r03] [强制] 出站客户端必须设默认超时：所有出站 HTTP 客户端显式设默认超时；调用方 ctx 是附加约束，不是超时来源。
- [judgments-r04] [强制] 身份不完整即 fail-closed：非平台、非系统且租户为空 → 拒绝（ErrMissingViewer），不猜测、不注入空租户条件。
- [judgments-r05] [强制] 显式声明，不基于空值推断：跨租户/平台能力由调用方显式标记（contextx.WithPlatform 等），框架不把空值解释成特权语义。
- [judgments-r06] [强制] 协议实现只承诺协议形状：传输层只管监听/TLS/Start/Stop/Endpoint/注册，健康检查等业务语义归 health 与装配层（gRPC grpc.health.v1 标准服务是唯一例外：只注册、不判断）。
- [judgments-r07] [强制] 未实现配置段 fail-fast：契约声明了却无实现的段直接报错，不静默丢弃；逃生舱是显式白名单（WithUnimplementedSegments）。
- [judgments-r08] [强制] 契约只为已实现的东西承诺形状：proto 契约字段必须有真实消费者，不预留「将来也许」的字段。
- [judgments-r09] [强制] 配置取值只有契约一条通道：链外输入（环境变量、手写 flag）必须接进契约或登记理由，不许静默存在——docscheck 与 bootstrap 的对账闸门会判红未登记路由。
