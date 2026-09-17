package cobramcp

import (
	"strings"

	baldlog "github.com/kalandramo/bald/log"
)

// parseLogLevel 把字符串日志级别解析为 bald/log 级别。
// 支持 debug / info / warn / error（大小写不敏感），未知取值回退 info。
func parseLogLevel(level string) baldlog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return baldlog.LevelDebug
	case "warn":
		return baldlog.LevelWarn
	case "error":
		return baldlog.LevelError
	default:
		return baldlog.LevelInfo
	}
}
