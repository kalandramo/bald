---
id: context
title: 上下文契约
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/上下文契约设计.md
checks: []
---
# 上下文契约

pkg/contextx 是请求级元信息的唯一存取面。母文档《上下文契约设计》（全文很短，读它即读卡）。

- [context-r01] [强制] 请求级元信息存取统一走 pkg/contextx 五个标准键（user/username/trace_id/request_id/tenant_id），禁止各包自定义私有 context 键。
- [context-r02] [强制] 每个键只用配套 With/From 对：WithUserID/UserIDFromContext、WithUsername、WithTraceID、WithRequestID、WithTenantID/TenantIDFromContext。
- [context-r03] [强制] From 读取缺失一律返回空串：不 panic、不区分「未设置」与「设置为空」；需强制存在的场景由业务自行校验。
- [context-r04] [强制] contextx 只存标量字符串标识：完整 Claims 走 authn.ContextWithAuthClaims，span 走 otel trace 上下文，不往 contextx 塞结构化数据。
- [context-r05] [参考] 写入方分工：user_id/username/tenant_id 由认证中间件经 ContextWithAuthClaims 同步写入；trace_id/request_id 由 RequestID/Observability 中间件写入。tenant_id 的消费方是 pkg/store 多租户查询（自动注入隔离条件），业务 handler 不自行读 tenant 做过滤。
