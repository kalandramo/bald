// recovery.go 提供 gRPC 侧的 panic 兜底拦截器（D5）。
//
// gin 链首道是 ginmw.Recovery()，gRPC 链此前没有对应层——handler/内层拦截器
// panic 会直接打穿进程（连接中断、无日志）。本拦截器捕获 panic、记日志后返回
// *berrors.Internal，由链最外层的 ErrorInterceptor 收口为 gRPC status。
package grpc

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	berrors "github.com/kalandramo/bald/pkg/berrors"
	"github.com/kalandramo/bald/pkg/log"
)

// RecoveryInterceptor 恢复 handler/内层拦截器的 panic 并返回 Internal 错误。
//
// 链序约定：放在 ErrorInterceptor 之后（第二外层）——panic 转成的 *berrors.Error
// 由最外层 Error 收口为完整 gRPC status；RequestID 等内层 panics 均被覆盖。
func RecoveryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				method := ""
				if info != nil {
					method = info.FullMethod
				}
				log.GetLogger().Error(ctx, "grpc handler panicked",
					"panic", r, "method", method, "stack", fmt.Sprintf("%+v", r))
				err = berrors.Internal("PANIC").WithMessage("internal panic: %v", r)
				resp = nil
			}
		}()
		return handler(ctx, req)
	}
}
