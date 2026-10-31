// Package log 是 bald 日志体系的契约层：仅声明 Logger 接口、级别枚举、
// 全局句柄（SetLogger/GetLogger）、上下文属性流（ContextWithAttrs/ContextAttrs）
// 与通用装饰器（MultiLogger），不携带任何具体日志后端——
// 后端实现见子包 bslog（对齐 transport：顶层契约 + 子包实现），
// 契约驱动的装配见 pkg/appkit（logger 段 Provider：内置 StandardLoggerProvider 或
// log/<backend>/contract 的后端 Provider）。
//
// 默认后端为 nop（静默零成本），由进程入口在装配期显式注入：
//
//	// logger 段 Provider 的 Build 产物即已调用 log.SetLogger 的实例
//	appkit.MustServe(ctx, std.Standard(), myLoggerProvider())
//
// 设计为三层架构的契约层（接口 + 全局表 + nop 默认），
// 依赖反转：全部框架代码只 import 本包，具体后端（slog/zap/...）经适配器注入。
package log
