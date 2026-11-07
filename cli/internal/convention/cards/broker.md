---
id: broker
title: 消息代理
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald 消息代理设计.md
checks: []
---
# 消息代理

broker 契约 + 四后端（kafka/rabbitmq/redis/rocketmq）。幂等纪律（状态流转/去重/失败留痕）见 errors/logging 卡与母文档。

- [broker-r01] [强制] 业务只 import broker 契约 + 所选后端 module；换后端只改构造行，Publish/Subscribe 调用点不动；broker 根 module 只依赖 encoding。
- [broker-r02] [强制] broker.Message.Headers 只放随消息走线的 string 值；进程内元数据放 Metadata——把要跨进程序传的数据放 Metadata，订阅端永远读不到。
- [broker-r03] [强制] 类型化订阅必须 Binder 与 handler 配套：Subscribe[T] + TypedHandler[T]，或裸 Subscribe 时传 Binder func() any；禁止绕过 Binder 直接断言 Body。
- [broker-r04] [强制] 编解码 fail-fast：codec 缺失且 Body 非 []byte/string 时报 errNoCodec，禁止静默回退 gob；WithCodec 查表失败同样报错。
- [broker-r05] [强制] 契约不含 Request 方法（四后端无法干净实现请求-响应），禁止在 broker 之上模拟 RPC 语义。
- [broker-r06] [强制] 生命周期顺序固定 Init() → Connect()，关闭走 Disconnect()。
- [broker-r07] [强制] 后端实现根包（如 broker/kafka）零 bconf 依赖；契约映射只放 contract/ 子包，导出 Type + Provider，段缺失返回 error 不静默。
- [broker-r08] [强制] broker 契约不织入 tracer（可观测归 contrib/observability-otlp 装配层）；库代码把 LogFatal 降级为 log.Error，禁止 os.Exit。
- [broker-r09] [建议] 装配优先走 broker.<backend> 段 Provider（资源键 appkit.NewKey[broker.Broker](...)）；段存在而无 Provider 由 L3 覆盖检查启动期报错。
- [broker-r10] [建议] AutoAck 默认开；需自控重试/死信语义才关（kafka 下 Ack=commit offset，关闭则 rebalance 后重投），在 handler 内显式 event.Ack()。
