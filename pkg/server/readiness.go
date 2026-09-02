package server

import (
	"context"
	"errors"
)

// Ready 聚合多个就绪依赖为一个 ReadinessFunc（服务端设计 §7.4 技术债落地）：
// 全部依赖就绪返回 nil；任一失败返回聚合错误（errors.Join，保留全部失败原因）。
//
// 典型用法：多依赖（DB + 缓存 + 下游）的「全部就绪」收敛成一个探针回调，
// 直接喂给 NewHTTPServer / NewGRPCServer / NewGatewayServer：
//
//	ready := server.Ready(
//		func(ctx context.Context) error { return db.PingContext(ctx) },
//		func(ctx context.Context) error { return cache.Ping(ctx).Err() },
//	)
//	httpSrv := server.NewHTTPServer(cfg, router, ready)
func Ready(fns ...ReadinessFunc) ReadinessFunc {
	return func(ctx context.Context) error {
		var errs []error
		for _, fn := range fns {
			if fn == nil {
				continue
			}
			if err := fn(ctx); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) == 0 {
			return nil
		}
		return errors.Join(errs...)
	}
}
