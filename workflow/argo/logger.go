package argo

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/log"
)

const (
	logKey = "[Argo]"
)

func LogDebug(args ...any) {
	log.Debug(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func LogInfo(args ...any) {
	log.Info(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func LogWarn(args ...any) {
	log.Warn(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func LogError(args ...any) {
	log.Error(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func LogFatal(args ...any) {
	log.Error(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func LogDebugf(format string, args ...any) {
	log.Debug(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

func LogInfof(format string, args ...any) {
	log.Info(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

func LogWarnf(format string, args ...any) {
	log.Warn(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

func LogErrorf(format string, args ...any) {
	log.Error(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

func LogFatalf(format string, args ...any) {
	log.Error(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
