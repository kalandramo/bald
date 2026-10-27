package argo

import (
	"context"
	"fmt"

	"github.com/kalandramo/bald/log"
)

const (
	logKey = "[Argo]"
)

func logInfo(args ...any) {
	log.Info(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func logInfof(format string, args ...any) {
	log.Info(context.Background(), fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
