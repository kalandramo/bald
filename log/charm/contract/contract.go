// Package contract 把契约后端声明项（bootstrapv1.Logger_Backend）的 charm 段
// 映射为 charm 后端的构造选项，供 bootstrap.LogRegistry 显式注册：
//
//	reg := bootstrap.NewLogRegistry()
//	reg.MustRegister(contract.Type, contract.Provider)
//
// 本包是唯一 import 契约（bconf）的地方；根包（..）保持零契约依赖。
package contract

import (
	"context"
	"fmt"
	"os"

	charmlog "github.com/charmbracelet/log"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
	charm "github.com/kalandramo/bald/log/charm"
)

// Type 是契约后端项 type 的注册名（小写约定）。
const Type = "charm"

// Provider 按契约 charm 段构造 Charmbracelet 终端日志后端。
// level/format/output_path 与 slog 段同形状；output_path 打开文件时
// 返回的 cleanup 负责关闭。
func Provider(ctx context.Context, b *bootstrapv1.Logger_Backend) (log.Logger, func(), error) {
	c := b.GetCharm()
	if c == nil {
		return nil, nil, fmt.Errorf("log/charm/contract: charm segment is nil")
	}

	l := charmlog.New(os.Stderr)

	// Level.
	switch c.GetLevel() {
	case "debug":
		l.SetLevel(charmlog.DebugLevel)
	case "warn":
		l.SetLevel(charmlog.WarnLevel)
	case "error":
		l.SetLevel(charmlog.ErrorLevel)
	default:
		l.SetLevel(charmlog.InfoLevel)
	}

	// Format.
	switch c.GetFormat() {
	case "json":
		l.SetFormatter(charmlog.JSONFormatter)
	default:
		l.SetFormatter(charmlog.TextFormatter)
	}

	// Output.
	switch c.GetOutputPath() {
	case "stdout":
		l.SetOutput(os.Stdout)
	case "", "stderr":
		// 默认 stderr。
	default:
		f, err := os.OpenFile(c.GetOutputPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, nil, fmt.Errorf("log/charm/contract: open %s: %w", c.GetOutputPath(), err)
		}
		l.SetOutput(f)
		return charm.NewLoggerWith(l), func() { _ = f.Close() }, nil
	}

	return charm.NewLoggerWith(l), func() {}, nil
}
