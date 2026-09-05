// Package log 是 bald 日志体系的契约层：仅声明 Logger 接口、级别枚举、
// 全局注册表（SetLogger/GetLogger）、上下文属性流（ContextWithAttrs/ContextAttrs）
// 与通用装饰器（MultiLogger），不携带任何具体日志后端——
// 后端实现见子包 slog（对齐 transport：顶层契约 + 子包实现），契约驱动的装配见 bootstrap。
//
// 默认后端为 nop（静默零成本），由进程入口在装配期显式注入：
//
//	logger, cleanup, err := bootstrap.BuildLogger(ctx, cfg.GetLogger())
//	log.SetLogger(logger)
//	defer cleanup()
//
// 设计对齐 go-wind/log 三层架构的契约层（接口 + 全局表 + nop 默认），
// 依赖反转：全部框架代码只 import 本包，具体后端（slog/zap/...）经适配器注入。
package log
