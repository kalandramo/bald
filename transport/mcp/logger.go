package mcp

import (
	"fmt"

	"github.com/kalandramo/bald/log"
)

const (
	logKey = "[" + KindMCP + "]"
)

///
/// logger
///

func logInfo(args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func logWarn(args ...any) {
	log.Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

func logError(args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

///
/// logger
///

func logInfof(format string, args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

func logWarnf(format string, args ...any) {
	log.Warn(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
