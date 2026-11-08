package bslog

import "log/slog"

// 本文件提供 OpenTelemetry Logs 的可选桥接入口。
//
// 设计原则：log 契约核心不引入 otel 依赖。接入 OTel 只需把其 slog.Handler
// 经 WithHandler 注入即可；WithOTelHandler 是 WithHandler 的语义别名，
// 便于阅读时明确意图。
//
// 桥接实现首选仓内自有桥（record 转换完整、仓内实测，含错误晋升/递归深度
// 上限/级别闸门；改编自上游 contrib 桥 v0.21.0）：
//
//	import (
//		"github.com/kalandramo/bald/log/otelslog"
//		"go.opentelemetry.io/otel/log"
//		sdklog "go.opentelemetry.io/otel/sdk/log"
//	)
//
//	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(...))
//	h := otelslog.NewHandler("bald", otelslog.WithLoggerProvider(provider))
//	logger := bslog.New(opts, bslog.WithOTelHandler(h))
//
// 也可注入任一 slog.Handler（如官方 contrib/bridges/otelslog），契约不限定
// 实现来源。级别过滤默认委托给 LoggerProvider 的 SDK 侧；显式收窄用
// otelslog.WithLevel。
func WithOTelHandler(h slog.Handler) Option {
	return WithHandler(h)
}
