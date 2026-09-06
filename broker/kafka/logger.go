package kafka

import (
	"fmt"

	kafkaGo "github.com/segmentio/kafka-go"
	"github.com/kalandramo/bald/log"
)

const logKey = "[kafka]"

func LogDebug(args ...any) {
	log.GetLogger().Debug(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogInfo(args ...any) {
	log.GetLogger().Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogWarn(args ...any) {
	log.GetLogger().Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogError(args ...any) {
	log.GetLogger().Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogFatal(args ...any) {
	log.GetLogger().Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}
func LogDebugf(format string, args ...any) {
	log.GetLogger().Debug(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogInfof(format string, args ...any) {
	log.GetLogger().Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogWarnf(format string, args ...any) {
	log.GetLogger().Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogErrorf(format string, args ...any) {
	log.GetLogger().Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
func LogFatalf(format string, args ...any) {
	log.GetLogger().Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

// Logger adapts to kafka-go.Logger interface
type Logger struct{}

func (l Logger) Printf(msg string, args ...any) {
	log.GetLogger().Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(msg, args...)))
}

// ErrorLogger adapts to kafka-go.Logger interface for errors
type ErrorLogger struct{}

func (l ErrorLogger) Printf(msg string, args ...any) {
	log.GetLogger().Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(msg, args...)))
}

var _ kafkaGo.Logger = Logger{}
var _ kafkaGo.Logger = ErrorLogger{}
