package http3

import (
	"fmt"

	"github.com/kalandramo/bald/log"
)

const (
	logKey = "[" + KindHTTP3 + "]"
)

///
/// logger
///

func logInfo(args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprint(args...)))
}

///
/// logger
///

func logInfof(format string, args ...any) {
	log.Info(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}

func logErrorf(format string, args ...any) {
	log.Error(nil, fmt.Sprintf("%s %s", logKey, fmt.Sprintf(format, args...)))
}
