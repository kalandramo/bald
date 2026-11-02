## 1. 采用的系统与架构

Bald 框架的日志体系采用「三层契约驱动」设计，核心仓库位于 `log/` 子模块（独立的 Go module），遵循与 `transport`、`broker` 一致的「顶层契约 + 子包实现」模式。

- **契约层** (`log/`)：仅声明 `Logger` 接口、`Level` 枚举、全局句柄（`SetLogger` / `GetLogger`）、上下文属性流（`ContextWithAttrs` / `ContextAttrs`）以及通用装饰器（`MultiLogger`、`filterLogger`）。该层零第三方依赖，所有框架代码只 import 本包。
- **默认后端** (`log/nop.go`)：`nopLogger` 是静默零成本实现，未注入任何后端时作为默认值；`Enabled(LevelDebug)` 恒返回 false，避免调用方构造无意义参数。
- **slog 后端** (`log/bslog/`)：基于标准库 `log/slog` 实现的开箱即用后端，支持 console/json 格式、stdout/stderr/文件输出、lumberjack 轮转切割、自定义 `slog.Handler` 注入（OTel 等）、过滤链装饰器（`WithFilter`）和固定属性（`WithAttrs`）。
- **远端适配器** (`log/{aliyun,loki,sentry,tencent,charm}`)：将各云厂商 SDK 适配为 `log.Logger` 契约，并通过各自子包的 `contract` 把 `bootstrapv1.Logger_Backend` 配置映射为实例。这些适配器由 `pkg/appkit` 的 logger Provider 装配注入。
- **装配层** (`bootstrap/log.go`)：`LogOptions` 函数把 `bootstrapv1.Logger_Backend` 转换为 `bslog.Options`，处理 level/format/output_paths/rotate 的字段映射与缺省回退。

## 2. 关键文件

- `log/doc.go` — 契约层文档，明确三层架构与依赖反转原则。
- `log/global.go` — 全局 Logger 注册表，`sync.RWMutex` 保护并发读写，nil 入参回退 nop。
- `log/nop.go` — 零成本静默实现，`Enabled` 恒 false。
- `log/multi.go` — 广播式组合多个 Logger，用于本地+远端同时输出。
- `log/filter.go` — 配置驱动的敏感字段脱敏装饰器，覆盖位置参数 kv 对、`With` 派生、ctx 属性流。
- `log/context.go` — 通过 context 携带结构化属性流（`slog.Attr` 切片）。
- `log/bslog/bslog.go` — slog 后端实现，包含 handler 构造、writer 打开、lumberjack 轮转、multiWriter 并发写。
- `log/bslog/filter.go` — slog handler 层的 `FilterKey` 过滤器，补全对 `slog.Attr` 形式的脱敏（与契约层 filter 互补）。
- `log/bslog/options.go` — `Options`、`RotateOptions` 结构体定义。
- `bootstrap/log.go` — 契约到 bslog.Options 的转换函数。
- 各远端适配器的 `options.go` / `xxx.go`：如 `log/aliyun/aliyun.go`、`log/loki/loki.go`、`log/sentry/sentry.go`、`log/tencent/tencent.go`、`log/charm/charm.go`。

## 3. 架构约定与设计决策

- **依赖反转**：契约层不依赖任何具体日志库；具体后端（slog/zap 等）经适配器注入，由进程入口在装配期显式调用 `log.SetLogger` 完成绑定。
- **默认行为**：未注入后端时使用 `nopLogger`，保证 import `log` 无任何副作用与输出。
- **级别策略**：统一使用 `slog` 的 Debug/Info/Warn/Error 四级，`bslog.parseLevel` 解析字符串 level，非法值回退 Info，避免崩溃。
- **输出目标**：支持 stdout/stderr 及任意文件路径；多 output_path 时通过 `multiWriter` 并发写入，任一失败不影响其余；首个文件打开失败回退 stdout，保证可观测性不丢。
- **日志轮转**：启用 rotate 时用 lumberjack，按 MaxSize(MaxSizeMB)/MaxBackups/MaxAge(天)/Compress 配置切割，cleanup 闭包负责 Close 触发缓冲落盘。
- **脱敏双通道**：
  - 契约层 `NewFilterLogger(keys...)` 是全后端通用的配置驱动脱敏，覆盖 kv 对与 ctx 属性流，但**不覆盖** `slog.Attr` 形式。
  - `bslog.WithFilter(FilterKey(...))` 是 slog handler 层的代码驱动过滤器，能改写 Record Attr，补全上述缺口。
  - 两者是互补关系而非替代关系。
- **多输出源**：`MultiLogger` 广播到全部子 Logger，`Enabled` 任一启用即放行（保守策略），单个失败不影响其余。
- **上下文属性流**：通过 `ContextWithAttrs(ctx, slog.Attr...)` 与 `ContextAttrs(ctx)` 在请求上下文中传递结构化字段，被 filterLogger 在调用前扫描并掩码。

## 4. 约定与约束

- **契约边界**：`log` 包仅声明接口与装饰器，不引入三方依赖（见 `log/go.mod` 仅依赖 stdlib）；具体后端下沉到子包（`bslog`、`aliyun`、`loki` 等）。
- **全局注入安全**：`SetLogger`/`GetLogger` 用 `sync.RWMutex` 保护，测试 `TestConcurrentSetGet` 验证并发安全；传入 nil 自动回退 nop。
- **默认后端不可变**：`nopLogger.With` 返回自身（不可变语义），`Enabled` 恒 false。
- **配置缺省回退**：`bootstrap.LogOptions` 中 Slog 段缺失时回退 `bslog.NewOptions()`（stdout + info）；rotate 零值字段回退 bslog 默认（100MB/7份/30天/gzip）。
- **脱敏范围约束**：契约层 `filterLogger` 明确不覆盖 `slog.Attr` 形式（见注释中的「实测确认，勿凭注释推断」），需同时挂 `bslog.WithFilter` 才能覆盖该形式。
- **Writer 容错**：文件打开失败回退 stdout，确保日志管线不因单一路径错误而中断。
- **资源释放**：`NewWithCleanup` 返回 cleanup 闭包收集 lumberjack/os.File 句柄，热更新重建后端时必须调用以触发缓冲落盘；`New` 丢弃 cleanup，适合一次性启动场景。
- **Provider 装配契约**：每个远端适配器子包提供 `contract` 子目录，把 `bootstrapv1.Logger_Backend` 映射为该后端的 Options 实例，由 appkit 段驱动装配注入。