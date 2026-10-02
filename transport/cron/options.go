package cron

import (
	"time"

	"github.com/robfig/cron/v3"
)

// Option 配置 Cron 服务器。所有 Option 在 NewServer 内**统一求值后**一次性
// 构造 scheduler——修复了此前「每个 Option 各自重建 scheduler」的缺陷：
//   - 重建会丢弃 NewServer 装配的 Recover 保护链；
//   - 多个重建型 Option 互相覆盖（顺序敏感，结果取决于最后一个）；
//   - WithSeconds 因此无法生效（parser 被后序 Option 硬编码的 6 字段冲掉）。
type Option func(*options)

// options 是 NewServer 求值 Option 后得到的完整配置。
type options struct {
	seconds            bool           // 是否接受 6 字段（含秒）表达式；默认 true（保持既有行为）
	location           *time.Location // nil = cron 默认（本地时区）
	logger             cron.Logger    // nil = cron 默认（标准输出）
	gracefullyShutdown bool
}

// WithGracefullyShutdown 设置是否启用优雅关闭模式。
// 启用时，Stop 会等待正在运行的任务执行完成后再返回。
func WithGracefullyShutdown(enable bool) Option {
	return func(o *options) { o.gracefullyShutdown = enable }
}

// WithLocation 设置 cron 调度器的时区。
func WithLocation(loc *time.Location) Option {
	return func(o *options) { o.location = loc }
}

// WithSeconds 设置是否接受秒级（6 字段）cron 表达式。
//
// 默认**启用**（保持既有行为与 README 示例：`*/10 * * * * *`）。传 false 关闭后，
// 只接受标准 5 字段表达式（分 时 日 月 周）与描述符（`@every 5s` 等）——
// 对应契约字段 `server.cron.seconds`。
//
// 修复前此选项是空实现（parser 在 NewServer 内硬编码含秒），配了完全无效。
func WithSeconds(enable bool) Option {
	return func(o *options) { o.seconds = enable }
}

// WithLogger 设置 cron 调度器的日志记录器。
func WithLogger(logger cron.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// buildParser 按 seconds 开关构造 cron 表达式解析器。
//
// 6 字段：[秒 分 时 日 月 周]；5 字段：[分 时 日 月 周]。两者都带 Descriptor
// （`@every`/`@daily` 等描述符与字段数无关，始终可用）。
func buildParser(seconds bool) cron.Parser {
	fields := cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor
	if seconds {
		fields |= cron.Second
	}
	return cron.NewParser(fields)
}
