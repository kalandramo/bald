---
id: observability
title: 可观测
level: 强制
category: ops
sources:
  - docs/devel/zh-CN/Bald 健康检查设计.md
  - docs/devel/zh-CN/Bald 指标设计.md
checks: []
---
# 可观测

health（探针）+ metrics（两套体系）的规则。装配入口与判据细节见两份母文档。

- [observability-r01] [强制] liveness 恒 200 绝不检查依赖（health.NewLivenessHandler()）；依赖故障唯一出口是 readiness（health.NewHandler，Down→503），禁止把 Health.Check 接进 liveness——重启解决不了依赖故障。
- [observability-r02] [强制] readiness 映射只有一条：Down→503，Up/Unknown→200，零检查器=Up（宁可乐观）；禁止把 Unknown/空表也判 503。
- [observability-r03] [强制] 健康状态三态（Unknown/Up/Down，零值 Unknown）；health.PingFunc 只能表达二态，需 Unknown 或 Details 时实现自定义 Checker。
- [observability-r04] [强制] health 出口是标准 http.Handler（gin 侧 gin.WrapH 适配），module 零第三方依赖——禁止引入 gRPC health 协议作为 HTTP 出口。
- [observability-r05] [强制] K8s 部署必须挂探针：appkit.WithHealth(h) 或业务自挂 handler，二者必有其一；配了 app.probe_live_path/probe_ready_path 却未声明 WithHealth 装配期 fail-fast。
- [observability-r06] [强制] gRPC 就绪：transport/grpc 只注册标准健康服务（HealthServer()），状态推送经 bootstrap.NewGRPCHealthServer（Down→NOT_SERVING，与 /readyz 同源）；判稳逻辑禁止下沉协议实现。
- [observability-r07] [强制] 场景分流：HTTP/gRPC 请求级指标走 pkg/metrics（AuditWithMetrics 中间件自动 emit，semconv v1.43.0）；TCP/MQ/WebRTC 消息路径走 metrics/ 三原语（WithMetrics 手工埋点）——禁止把 topic/queue 自由标签塞进 Recorder 固定维度。
- [observability-r08] [强制] 一个进程 otel 全局 MeterProvider 只能一个：metrics/otel.New 与 observability-otlp/metrics.Setup 互斥（装配计划期判红）；要双通道用 Setup 多 Reader。
- [observability-r09] [强制] 三原语埋点纪律：同一 name 的 label keys 恒定（prometheus 首见冻结）、values 低基数（错误取类别 timeout/unavailable 而非原始消息）；接口无返回值=旁路，禁止用埋点报错。
- [observability-r10] [建议] Recorder instruments 首次 Record 惰性创建（SetMeterProvider 之前创建的 instrument 永久 no-op，症状是指标可见但恒空）；metrics 段与 pkg/metricshttp（缺省 :9091 /metrics）不支持热更新。
