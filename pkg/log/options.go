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
	// Rotate 文件轮转配置；仅当某 OutputPath 为文件路径且 Enabled 时生效。
	Rotate *RotateOptions `json:"rotate,omitempty" yaml:"rotate,omitempty"`
}

// RotateOptions 控制日志文件轮转（基于 lumberjack）。
// 仅当 OutputPaths 含文件路径且 Enabled=true 时，对应文件按大小/时间切割、清理并可选 gzip 压缩。
type RotateOptions struct {
	// Enabled 是否启用文件轮转；关闭时文件路径按 os.OpenFile 直写（不切割）。
	Enabled bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// MaxSize 单个日志文件最大体积（MB），超过即触发切割，默认 100。
	MaxSize int `json:"max-size,omitempty" yaml:"max-size,omitempty"`
	// MaxBackups 保留的历史文件份数（不含当前），默认 7。
	MaxBackups int `json:"max-backups,omitempty" yaml:"max-backups,omitempty"`
	// MaxAge 历史文件最长保留天数，默认 30。
	MaxAge int `json:"max-age,omitempty" yaml:"max-age,omitempty"`
	// Compress 是否对历史文件 gzip 压缩，默认 true。
	Compress bool `json:"compress,omitempty" yaml:"compress,omitempty"`
}

// NewOptions 返回带默认值的 Options。
func NewOptions() *Options {
	return &Options{
		Level:       "info",
		Format:      "console",
		OutputPaths: []string{"stdout"},
		Rotate: &RotateOptions{
			Enabled:    false,
			MaxSize:    100,
			MaxBackups: 7,
			MaxAge:     30,
			Compress:   true,
		},
	}
}

// AddFlags 将日志配置注册为命令行 flag，对齐 onexstack/pkg/log 的 --log.* 形态。
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Level, "log.level", o.Level, "log level: debug|info|warn|error")
	fs.StringVar(&o.Format, "log.format", o.Format, "log format: console|json")
	fs.StringSliceVar(&o.OutputPaths, "log.output-paths", o.OutputPaths, "log output paths, e.g. stdout,/var/log/bald.log")
	if o.Rotate == nil {
		o.Rotate = &RotateOptions{}
	}
	fs.BoolVar(&o.Rotate.Enabled, "log.rotate.enabled", o.Rotate.Enabled, "enable log file rotation (lumberjack)")
	fs.IntVar(&o.Rotate.MaxSize, "log.rotate.max-size", o.Rotate.MaxSize, "max size per log file in MB")
	fs.IntVar(&o.Rotate.MaxBackups, "log.rotate.max-backups", o.Rotate.MaxBackups, "max number of backup files")
	fs.IntVar(&o.Rotate.MaxAge, "log.rotate.max-age", o.Rotate.MaxAge, "max age of backup files in days")
	fs.BoolVar(&o.Rotate.Compress, "log.rotate.compress", o.Rotate.Compress, "compress rotated files with gzip")
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
	if o.Rotate != nil && o.Rotate.Enabled && o.Rotate.MaxSize <= 0 {
		return fmt.Errorf("log.rotate.max-size must be > 0 when rotation is enabled")
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
