package log

import "log/slog"

// 本文件提供 OpenTelemetry Logs 的可选桥接入口。
//
// 设计原则：pkg/log 核心不引入 otel 依赖。接入 OTel 只需把其 slog.Handler
// 经 WithHandler 注入即可；WithOTelHandler 是 WithHandler 的语义别名，
// 便于阅读时明确意图。调用方需自行引入 otel 相关依赖。
//
// 示例（调用方模块）：
//
//	import (
//		"go.opentelemetry.io/contrib/bridges/otelslog"
//		"go.opentelemetry.io/otel/log"
//		"go.opentelemetry.io/otel/sdk/log"
//	)
//
//	provider := log-sdk.NewLoggerProvider(log-sdk.WithProcessor(...))
//	h := otelslog.NewHandler("bald", otelslog.WithLoggerProvider(provider))
//	logger := log.NewSlogLogger(opts, log.WithOTelHandler(h))
//
// 级别与属性映射复用 onexstack/pkg/otelslog 的 convert 逻辑（slog.Record → OTel log.Record）。
func WithOTelHandler(h slog.Handler) Option {
	return WithHandler(h)
}
