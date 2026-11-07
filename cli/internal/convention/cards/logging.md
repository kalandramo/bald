---
id: logging
title: 日志
level: 强制
category: contract
sources:
  - docs/devel/zh-CN/Bald 日志设计.md
checks: []
---
# 日志

log 契约层 + bslog 适配 + 可插拔后端的三层结构。完整设计与后端语义见母文档《Bald 日志设计》。

- [logging-r01] [强制] 业务与框架代码只 import bald/log 契约层（Logger 六方法）；框架核心永不 import 具体日志库，远端后端经各自 contract/ 子包由 logger 段 Provider 装配。
- [logging-r02] [强制] 打日志一律用包级函数 log.Info/Warn/Error(ctx, msg, kv...)；log.GetLogger().Xxx 链式写法已全量替换并禁止。
- [logging-r03] [强制] 参数用 key-value 结构化对（偶数下标为 key）；脱敏覆盖 kv 对、With 派生对、ctx 属性流三类来源。
- [logging-r04] [强制] 级别只用 Debug/Info/Warn/Error 四级；契约无 Fatal/Panic，需要终止进程用 log.Error + os.Exit。
- [logging-r05] [建议] 昂贵参数构造先经 log.Enabled(log.LevelDebug) 守卫再 log.Debug。
- [logging-r06] [强制] 请求级字段（trace_id 等）经 log.ContextWithAttrs 在请求入口挂载、请求范围内自动携带，不手工拼进每条日志；IO 不绑 ctx。
- [logging-r07] [强制] 全局脱敏用契约 logger.filter_keys 配置驱动，出口统一包一次 log.NewFilterLogger；禁止逐后端重复包装（重复掩码/漏包某后端）。
- [logging-r08] [强制] 后端接入选 logger.backends：配置写 type: loki 却无对应 Provider 时启动期 fail-fast，禁止静默降级成 slog 假装成功。
- [logging-r09] [强制] 新日志后端独立 module + contract/ 子包模式（导出 Type 常量 + Provider）；禁止 init() 自注册与 blank import——Provider 里的 import 就是依赖清单。
- [logging-r10] [参考] 日志系统自身故障用降级兜底不用崩溃：配置非法回退 info、文件路径失败回退 stdout、热更换后端失败只记错不杀进程。
