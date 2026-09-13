package rabbitmq

import (
	"fmt"

	"github.com/kalandramo/bald/log"
)

const logKey = "[rabbitmq]"

func LogDebug(args ...any) {
	log.Debug(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogInfo(args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogWarn(args ...any) {
	log.Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogError(args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogFatal(args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogDebugf(format string, args ...any) {
	log.Debug(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogInfof(format string, args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogWarnf(format string, args ...any) {
	log.Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogErrorf(format string, args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogFatalf(format string, args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
