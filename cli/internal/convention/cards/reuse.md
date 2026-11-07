---
id: reuse
title: 复用优先
level: 强制
category: core
sources:
  - README.md
  - docs/devel/zh-CN/框架契约总览.md
checks: []
---
# 复用优先

写任何新代码之前，先确认 bald 是否已经提供（onexai `onexstack-reuse` 的 bald 对应物）。
能力地图见母文档《框架契约总览》与 catalog 卡。

- [reuse-r01] [强制] 落笔前先查能力面：pkg/、contrib/ 与各能力 module（broker/registry/cache/ratelimit/circuitbreaker/retry/metrics/health/oss/encoding/transport）已有的一律复用，不写同形实现。
- [reuse-r02] [强制] 不复制、不 vendor bald 库代码进业务项目：始终以 Go module 依赖引入（`go get github.com/kalandramo/bald/pkg@vX.Y.Z`）——复制让升级与安全修复变得非常困难。
- [reuse-r03] [强制] 装配知识不进业务目录：装配写成 appkit 的段驱动 Provider 契约，业务代码只声明业务；出现第二份装配判断时回 appkit 收闸门（判据 J2）。
- [reuse-r04] [强制] 错误统一走 berrors（传输中立），gRPC/HTTP 边界转换分别用 berrors/grpcerr、berrors/httperr，不自定义错误结构体、不各 handler 自拼错误码。
- [reuse-r05] [强制] 日志走 log 契约层 + log/bslog 适配（KV 结构化、英文、脱敏），不自封装 logger、不在库代码里打业务日志。
- [reuse-r06] [强制] 横切中间件用 pkg/middleware（bundle 门面：认证/日志/恢复/限流等），不在 handler 或 interceptor 里手写横切件。
