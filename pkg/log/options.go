package log

import (
	"fmt"
	"log/slog"

	"github.com/spf13/pflag"
)

// Options 定义日志后端的配置项，可经多源配置（flag > env > 文件 > 远程）加载。
type Options struct {
	// Level 日志级别：debug|info|warn|error，默认 info。
	Level string `json:"level,omitempty" yaml:"level,omitempty"`
	// Format 输出格式：console|json，默认 console。
	Format string `json:"format,omitempty" yaml:"format,omitempty"`
	// OutputPaths 输出目标，支持 "stdout"/"stderr" 或文件路径，默认 ["stdout"]。
	OutputPaths []string `json:"output-paths,omitempty" yaml:"output-paths,omitempty"`
}

// NewOptions 返回带默认值的 Options。
func NewOptions() *Options {
	return &Options{
		Level:       "info",
		Format:      "console",
		OutputPaths: []string{"stdout"},
	}
}

// AddFlags 将日志配置注册为命令行 flag，对齐 onexstack/pkg/log 的 --log.* 形态。
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Level, "log.level", o.Level, "log level: debug|info|warn|error")
	fs.StringVar(&o.Format, "log.format", o.Format, "log format: console|json")
	fs.StringSliceVar(&o.OutputPaths, "log.output-paths", o.OutputPaths, "log output paths, e.g. stdout,/var/log/bald.log")
}

// Validate 校验 Options 取值合法性。
func (o *Options) Validate() error {
	switch o.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log.level %q, want debug|info|warn|error", o.Level)
	}
	switch o.Format {
	case "console", "json":
	default:
		return fmt.Errorf("invalid log.format %q, want console|json", o.Format)
	}
	if len(o.OutputPaths) == 0 {
		return fmt.Errorf("log.output-paths must not be empty")
	}
	return nil
}

// parseLevel 将字符串级别映射为 slog.Level。
func parseLevel(s string) (level slog.Level, err error) {
	switch s {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unknown level %q", s)
	}
}
