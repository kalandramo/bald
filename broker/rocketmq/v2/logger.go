package rocketmq

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/kalandramo/bald/log"
)

const (
	loggerKey = "[rocketmq] "
)

// logger 把 SDK 的日志调用转进 bald 的日志全局。
//
// level 是原子量：本包只往 SDK 全局注入一次（rocketmq.go 的 publishLogger），但级别
// 每个 broker 的 Init 都要覆盖一次，而读到它的是客户端自己的日志线程——rlog 那侧不带锁。
type logger struct {
	level atomic.Int32
}

func newLogger(level log.Level) *logger {
	l := &logger{}
	l.setLevel(level)
	return l
}

func (l *logger) setLevel(level log.Level) {
	l.level.Store(int32(level))
}

func (l *logger) currentLevel() log.Level {
	return log.Level(l.level.Load())
}

func toKeyVals(fields map[string]any) (keyVals []any) {
	for k, v := range fields {
		keyVals = append(keyVals, k)
		keyVals = append(keyVals, v)
	}
	return
}

func (l *logger) logMsg(level log.Level, msg string, fields map[string]any) {
	if l.currentLevel() > level {
		return
	}
	keyVals := toKeyVals(fields)
	lg := log.GetLogger()
	switch level {
	case log.LevelDebug:
		lg.Debug(nil, loggerKey+msg, keyVals...)
	case log.LevelInfo:
		lg.Info(nil, loggerKey+msg, keyVals...)
	case log.LevelWarn:
		lg.Warn(nil, loggerKey+msg, keyVals...)
	case log.LevelError:
		lg.Error(nil, loggerKey+msg, keyVals...)
	default:
		lg.Error(nil, loggerKey+msg, keyVals...)
	}
}

func (l *logger) logMsgf(level log.Level, format string, a ...any) {
	if l.currentLevel() > level {
		return
	}
	msg := fmt.Sprintf(format, a...)
	lg := log.GetLogger()
	switch level {
	case log.LevelDebug:
		lg.Debug(nil, loggerKey+msg)
	case log.LevelInfo:
		lg.Info(nil, loggerKey+msg)
	case log.LevelWarn:
		lg.Warn(nil, loggerKey+msg)
	case log.LevelError:
		lg.Error(nil, loggerKey+msg)
	default:
		lg.Error(nil, loggerKey+msg)
	}
}

func (l *logger) Debug(msg string, fields map[string]any) {
	l.logMsg(log.LevelDebug, msg, fields)
}

func (l *logger) Debugf(format string, a ...any) {
	l.logMsgf(log.LevelDebug, format, a...)
}

func (l *logger) Info(msg string, fields map[string]any) {
	l.logMsg(log.LevelInfo, msg, fields)
}

func (l *logger) Infof(format string, a ...any) {
	l.logMsgf(log.LevelInfo, format, a...)
}

func (l *logger) Warning(msg string, fields map[string]any) {
	l.logMsg(log.LevelWarn, msg, fields)
}

func (l *logger) Warningf(format string, a ...any) {
	l.logMsgf(log.LevelWarn, format, a...)
}

func (l *logger) Error(msg string, fields map[string]any) {
	l.logMsg(log.LevelError, msg, fields)
}

func (l *logger) Errorf(format string, a ...any) {
	l.logMsgf(log.LevelError, format, a...)
}

func (l *logger) Fatal(msg string, fields map[string]any) {
	l.logMsg(log.LevelError, msg, fields)
}

func (l *logger) Fatalf(format string, a ...any) {
	l.logMsgf(log.LevelError, format, a...)
}

// Level 实现 rlog.Logger：SDK 的 rlog.SetLogLevel 会调到这里，可能在任意线程上，
// 因此写级别走 setLevel（原子）。
func (l *logger) Level(lvl string) {
	switch strings.ToLower(lvl) {
	case "panic", "fatal", "error":
		l.setLevel(log.LevelError)
	case "warn", "warning":
		l.setLevel(log.LevelWarn)
	case "info":
		l.setLevel(log.LevelInfo)
	case "debug", "trace":
		l.setLevel(log.LevelDebug)
	}
}

func (l *logger) OutputPath(_ string) (err error) {
	return nil
}
