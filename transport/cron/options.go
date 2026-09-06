package cron

import (
	"time"

	"github.com/robfig/cron/v3"
)

// Option 是 Cron 服务器的配置选项。
type Option func(*Server)

// WithGracefullyShutdown 设置是否启用优雅关闭模式。
// 启用时，Stop 会等待正在运行的任务执行完成后再返回。
func WithGracefullyShutdown(enable bool) Option {
	return func(s *Server) {
		s.gracefullyShutdown = enable
	}
}

// WithLocation 设置 cron 调度器的时区。
func WithLocation(loc *time.Location) Option {
	return func(s *Server) {
		s.cronScheduler = cron.New(
			cron.WithLocation(loc),
			cron.WithParser(cron.NewParser(
				cron.Second|cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor,
			)),
		)
	}
}

// WithSeconds 设置是否使用秒级 cron 表达式（默认已启用）。
func WithSeconds(enable bool) Option {
	return func(s *Server) {
		// 默认已启用秒级，此选项保留用于未来扩展
	}
}

// WithLogger 设置 cron 调度器的日志记录器。
func WithLogger(logger cron.Logger) Option {
	return func(s *Server) {
		s.cronScheduler = cron.New(
			cron.WithLogger(logger),
			cron.WithParser(cron.NewParser(
				cron.Second|cron.Minute|cron.Hour|cron.Dom|cron.Month|cron.Dow|cron.Descriptor,
			)),
		)
	}
}
