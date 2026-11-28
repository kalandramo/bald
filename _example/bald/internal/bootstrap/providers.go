package bootstrap

import (
	"log/slog"

	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/pkg/appkit"
)

// newLoggerProvider 覆盖 std 的默认 logger Provider：Attr 层脱敏 + 服务标识。
//
// 不复制框架的分派逻辑：StandardLoggerProvider 的 deco（bslog.Option）在阶段 A
// 引导与各后端 slog 构造点统一生效，段路径末者胜使它在 std.Standard() 之后
// 传入即完成覆盖——装饰日志只需这一个构造器（见 pkg/appkit/standard.go 注记）。
func newLoggerProvider() appkit.Provider {
	return appkit.StandardLoggerProvider(
		bslog.WithFilter(bslog.FilterKey("password")),
		bslog.WithFilter(bslog.FilterKey("token")),
		bslog.WithAttrs(slog.String("service.name", "bald-demo")),
	)
}
