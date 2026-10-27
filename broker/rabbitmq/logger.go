package rabbitmq

import (
	"fmt"

	"github.com/kalandramo/bald/log"
)

const logKey = "[rabbitmq]"

func logError(args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func logDebugf(format string, args ...any) {
	log.Debug(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func logWarnf(format string, args ...any) {
	log.Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func logErrorf(format string, args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
