package option

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/log"
)

const (
	logKey = "[redis]"
)

func LogDebug(args ...any) {
	log.Debug(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func LogError(args ...any) {
	log.Error(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
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
