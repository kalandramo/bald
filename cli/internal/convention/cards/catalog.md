---
id: catalog
title: 能力包目录
level: 建议
category: core
sources:
  - docs/devel/zh-CN/框架契约总览.md
  - README.md
checks: []
---
# 能力包目录

「接什么用哪个包」的速查表（API 细节以母文档《框架契约总览》为准）。先查本卡再动手，是 reuse-r01 的操作入口。

- [catalog-r01] [建议] 应用编排/装配：pkg/appkit——MustServe + 段驱动 Provider + 装配计划（plan）/doctor/Drift/Reconcile + mech 高级机制面。
- [catalog-r02] [建议] 传输：transport/web（gin 流水线）、transport/grpc、transport/gateway（grpc-gateway）；应用侧 MCP 传输用 transport/mcp。
- [catalog-r03] [建议] 配置：契约层 bconf（proto 单一真相源）+ 配置源层 bconfig、bootstrap/config（层模型装载器）。
- [catalog-r04] [建议] 数据访问：pkg/store（泛型、零引擎依赖，where/paging 构建器）；引擎桥接 contrib/store-gorm 等。
- [catalog-r05] [建议] 认证/授权/审计：pkg/authn、pkg/authz、pkg/audit；实现桥接 contrib/authn-jwt、authz-casbin、audit-gorm、audit-stream。
- [catalog-r06] [建议] 消息：broker（kafka/rabbitmq/redis/rocketmq 四实现，契约 + 实现分离）。
- [catalog-r07] [建议] 注册发现：registry（inmemory 内置；etcd/consul/nacos/kubernetes 直连 SDK）。
- [catalog-r08] [建议] 韧性：ratelimit（bbr/sentinel/tokenbucket）、circuitbreaker（hystrix/sentinel/sres/vegas）、retry。
- [catalog-r09] [建议] 可观测：metrics（prometheus/otel/datadog）、health（healthz + readiness + checkers）、contrib/observability-otlp、pkg/metricshttp。
- [catalog-r10] [建议] 基础设施：cache（local/redis/loadable）、encoding（12 种编解码）、oss（minio/s3）。
- [catalog-r11] [建议] MCP/LLM：cobramcp（任意 Cobra CLI → MCP server）、transport/mcp（应用侧 MCP）、ai/{openai,langchaingo,eino}。
- [catalog-r12] [建议] 横切件：pkg/middleware（bundle 门面）、pkg/validation（命令式校验分发）、pkg/fieldmaskutil、pkg/contextx（Trace ID/Logger 上下文）。
