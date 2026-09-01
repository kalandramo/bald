# slog 日志能力接入路线（评估 + 实现规划）

本文评估把 `gookit/slog` 项目（`d:\code\konglingfei\slog`）的功能在 bald 的
`pkg/log/slog.go`（标准库 `log/slog` 适配层）中落地的可行性，并给出分阶段实现路线。

---

## 1. 评估结论速览

| gookit/slog 能力 | 是否在 slog.go 落地 | 方式 | 优先级 |
|---|---|---|---|
| 文件轮转（按大小 + 清理 + gzip） | ✅ 已落地 | 用 bald 已引用的 **lumberjack** 替换 `openWriter` 的文件分支 | 高 |
| 文件轮转（按**时间**切割，RotateTime 按天/小时） | ⏸️ 未迁移 | lumberjack 不支持按时间切割；确有需求时引入 `github.com/gookit/rotatefile`（可独立 io.Writer 接 slog）或系统级 logrotate | 按需 |
| 彩色 / 模板化控制台 | ✅ 可选落地 | 引入 `tint` 替换 console 的 `slog.NewTextHandler` | 中 |
| Processor（字段注入，如 hostname） | ❌ 不迁移 | bald 已有等价物 `ContextWithAttrs` + `WithAttrs` | — |
| Filter / 脱敏 | ❌ 不迁移 | bald 已有等价物 `FilterKey` | — |
| 多 Handler 同时输出 | ❌ 不迁移 | bald 已有等价物 `openWriter` 的 `multiWriter` | — |
| trace…panic 8 级体系 | ❌ 不迁移 | bald 用标准库 slog 4 级，语义已对齐生态 | — |
| 整套自研 Handler/Formatter/Record | ❌ 不引入 | 与 bald「标准库 slog 适配层」定位冲突 | — |

**一句话结论**：不要搬运 gookit/slog 的自研日志栈，而是取其**能力意图**，
用 bald 已有的标准库 `log/slog` 适配层 + 已有的 lumberjack 依赖来实现等价能力。

---

## 2. 现状对比

### 2.1 gookit/slog（`d:\code\konglingfei\slog`）

- **自研独立日志库**：完全不依赖标准库 `log/slog`，自带 `Logger`/`Handler`/`Formatter`/`Record`/`Processor` 体系。
- 杀手锏：`rotatefile` 子包（文件轮转 + 清理 + gzip）、彩色模板化 `TextFormatter`、多 Handler 同时输出、Processor 字段注入。
- go.mod 依赖 `gookit/color`、`gookit/goutil`、`gookit/rotatefile`、`valyala/bytebufferpool` 等一整套自研生态。
- 其 `rotatefile` 子包 README 自己声明：可作为独立 `io.Writer` 用在标准库 `log/slog` 上。

### 2.2 bald `pkg/log`（`d:\code\konglingfei\bald\pkg\log`）

- 核心 `log.go` 定义极简 `Logger` 接口（4 级 + `Enabled` + `With`），**不依赖任何具体日志库**，默认后端 nop。
- `slog.go` 是**标准库 `log/slog` 适配层**：把 `log/slog` 包装成 bald 的 `Logger`；`openWriter` 按 `OutputPaths` 选择 stdout/file/多目标。
- 已有能力覆盖 gookit 特色：
  - `context.go` 的 `ContextWithAttrs` → 等价 gookit **Processor**（字段注入）
  - `filter.go` 的 `FilterKey` → 等价 gookit **Filter**（脱敏）
  - `slog.go` 的 `multiWriter` + `openWriter` 多路径 → 等价 gookit **多 Handler**
- `slog.go:32` 注释已点名 **Lumberjack** 为预期轮转后端；`_example/go.mod`、`tests/integration/go.mod` 已间接引入 `gopkg.in/natefinch/lumberjack.v2`。

**冲突点**：gookit/slog 是「替代」标准库 slog 的自研栈，bald slog.go 是「适配」标准库 slog 的薄层。二者是替代 vs 适配关系，不能合并为一。

---

## 3. 推荐方案

以 **标准库 `log/slog` 适配层为主**，按需引入两个小依赖补齐标准库短板：

- **文件轮转**：引入 **lumberjack**（bald 已间接依赖，直接提升为 main 依赖），替换 `openWriter` 中 `os.OpenFile` 的文件分支。这是标准库 slog 唯一缺失的生产刚需，也是 gookit/slog 最有价值的能力。
- **彩色控制台**（可选）：引入 `github.com/lmittmann/tint`（轻量，仅依赖标准库），替换 console 格式的 `slog.NewTextHandler`，获得彩色 + 时间格式美化。
- **其余能力不迁移**：Processor / Filter / 多 Handler 已被 `ContextWithAttrs` / `FilterKey` / `multiWriter` 覆盖。

> 不引入 `gookit/slog` / `gookit/rotatefile`：会与已有 lumberjack 重复依赖，且把一整套自研日志栈塞进 bald，违背「标准库 slog 适配层」的既定设计。

---

## 4. 分阶段实现路线

### 阶段 1：文件轮转（lumberjack 接入 `openWriter`）—— 高优先级 ✅ 已实现（2026-09-01，含 proto 契约对齐）

> 能力边界：lumberjack 仅支持**按大小**（MaxSize）触发切割；MaxAge/MaxBackups 只控制
> 历史备份的保留时限与份数，**不是**按时间（每日/每小时）切割。gookit rotatefile 的
> `RotateTime` 能力未迁移，确有按天切割需求时再评估引入。

**改动点**：`pkg/log/slog.go` 的 `openWriter` + `pkg/log/options.go` 的 `Options`。

1. `Options` 增加轮转子结构（或扁平字段），纳入多源配置：

```go
// Options 新增字段
type RotateOptions struct {
    Enabled   bool   `json:"enabled,omitempty"   yaml:"enabled,omitempty"`
    MaxSize   int    `json:"max-size,omitempty"  yaml:"max-size,omitempty"`   // MB，默认 100
    MaxBackups int   `json:"max-backups,omitempty" yaml:"max-backups,omitempty"` // 保留份数，默认 7
    MaxAge    int    `json:"max-age,omitempty"   yaml:"max-age,omitempty"`     // 天，默认 30
    Compress  bool   `json:"compress,omitempty"  yaml:"compress,omitempty"`
}
```

2. `openWriter` 的文件分支从 `os.OpenFile` 改为 lumberjack：

```go
default: // 文件路径
    if o.Rotate != nil && o.Rotate.Enabled {
        return &lumberjack.Logger{
            Filename:   p,
            MaxSize:    o.Rotate.MaxSize,    // MB
            MaxBackups: o.Rotate.MaxBackups,
            MaxAge:     o.Rotate.MaxAge,     // days
            Compress:   o.Rotate.Compress,
        }
    }
    // 回退：无轮转直写（保持原行为）
    f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
    ...
```

3. `go.mod` 把 `lumberjack.v2` 从 indirect 提升为 direct 依赖；`AddFlags` 增加 `--log.rotate.*` 系列 flag。

**验收**：`OutputPaths: ["/var/log/bald/bald.log"]` + `Rotate.Enabled=true` 后，日志按大小切割、保留 N 份、过期清理、可 gzip。

### 阶段 2：彩色 / 美化控制台（tint）—— 中优先级

**改动点**：`slog.go` 的 `NewSlogLogger` 构造 handler 分支。

```go
import "github.com/lmittmann/tint"

// Format == "console" 时：
h = tint.NewHandler(w, &tint.Options{
    Level:     level,
    AddSource: false,
    TimeFormat: time.RFC3339,
})
// Format == "json" 时仍用 slog.NewJSONHandler
```

`tint` 仅依赖标准库，提供与 zap-console 类似的彩色输出，无需引入 gookit/color 全家桶。

### 阶段 3：（可选）高级能力对齐

- **分级 Handler（error→err.log / info→info.log）**：⚠️ 现有 `OutputPaths` 多目标是**复制分流**（每个目标收到全部日志，经 `multiWriter`），**不是**按级别分流。确需按级别落不同文件时，须构造多个不同 Level 的 handler 各挂独立 lumberjack writer（`WithHandler` 注入点已支持），或在框架层新增 `LevelWriters` 配置——按需再做。
- **hostname / 环境字段注入**：gookit 的 `AddHostname` Processor 等价于在进程入口 `log.SetLogger(log.NewSlogLogger(o, log.WithAttrs(slog.String("hostname", ...))))`，已有 `WithAttrs` 支持，不必新增机制。
- **Fatal/Panic 级别**：标准库 slog 无此级，业务可用 `Error` + `os.Exit` 替代；不在框架层补。

---

## 5. 依赖与风险

- **依赖**：新增 `tint`（阶段 2，可选）；`lumberjack` 由 indirect 转 direct（阶段 1，已在树中）。
- **不引入**：`gookit/slog`、`gookit/rotatefile`、`gookit/color` —— 与既有 lumberjack / 标准库 slog 重复且违背适配层定位。
- **兼容性**：lumberjack 与标准库 slog 是事实标准组合，零摩擦；tint API 稳定。
- **回退**：`Rotate.Enabled=false` 时 `openWriter` 退回原 `os.OpenFile` 行为，保证无配置时不破坏现有示例（`_example/bald` 默认 `OutputPaths: ["stdout"]`）。
- **测试**：`slog.go` 已有 `withWriter` 测试注入点，可复用验证轮转 writer 接入；建议补 `openWriter` 文件路径 + 轮转开关的单测。

---

## 6. 决策建议

- **立即做**：阶段 1（lumberjack 轮转），因为它填补标准库 slog 生产刚需，且与 bald 已有意图（slog.go 注释点名 Lumberjack、`_example` 已间接依赖）完全一致。
- **按需做**：阶段 2（tint 彩色控制台），仅当控制台可读性成为诉求时。
- **不做**：搬运 gookit/slog 自研栈；Processor/Filter/多 Handler 能力已被 bald 现有代码覆盖。
