package appkit

import (
	"context"
	"net/http"
	"time"

	"google.golang.org/grpc"

	"github.com/kalandramo/bald/health"
)

// 框架默认探针路径（可用 WithProbePaths 覆盖）。
const (
	DefaultHealthPath = "/healthz" // 存活探针（进程在即 200）
	DefaultReadyPath  = "/readyz"  // 就绪探针（依赖检查，Down → 503）
)

// HealthOption 配置 appkit 的健康检查默认装配。
type HealthOption func(*bootstrapSpec)

// WithProbePaths 覆盖 HTTP 探针路径（默认 /healthz /readyz）；空值忽略。
func WithProbePaths(live, ready string) HealthOption {
	return func(s *bootstrapSpec) {
		if live != "" {
			s.probeLivePath = live
		}
		if ready != "" {
			s.probeReadyPath = ready
		}
	}
}

// WithHealthPollInterval 覆盖 gRPC 侧就绪状态同步间隔（默认 2s）。
func WithHealthPollInterval(d time.Duration) HealthOption {
	return func(s *bootstrapSpec) {
		if d > 0 {
			s.healthInterval = d
		}
	}
}

// WithHealth 装配健康检查：HTTP/gateway 双探针 + gRPC 标准健康服务状态联动。
//
// 探针路由由装配层在业务 handler 外层包一层 mux 得到（协议实现不注册任何
// 框架路由）；gRPC 侧由 bootstrap 后台轮询 h 并把结果 SetServingStatus。
// **不声明则不挂任何探针**——业务也可以完全自行组装：把 health.NewHandler(h)
// 挂进自己的 gin/mux 即可。见《Bald 健康检查装配设计》。
func WithHealth(h *health.Health, opts ...HealthOption) BootstrapOption {
	return func(s *bootstrapSpec) {
		s.health = h
		s.probeLivePath = DefaultHealthPath
		s.probeReadyPath = DefaultReadyPath
		for _, o := range opts {
			o(s)
		}
	}
}

// WithProbes 在业务 handler 外层包一层探针 mux（精确路径给探针，其余交给业务），
// 导出给手工装配场景复用：CLI 生成的 app、不用 FromBootstrap 的装配代码。
// live/ready 传空串时用默认路径（/healthz、/readyz）。
func WithProbes(business http.Handler, h *health.Health, live, ready string) http.Handler {
	if live == "" {
		live = DefaultHealthPath
	}
	if ready == "" {
		ready = DefaultReadyPath
	}
	return withProbes(business, h, live, ready)
}

// withProbes 在业务 handler 外层包一层探针 mux：精确路径给探针（存活恒 200、
// 就绪按依赖 503），其余路径交给业务。
func withProbes(business http.Handler, h *health.Health, live, ready string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle(live, health.NewLivenessHandler())
	mux.Handle(ready, health.NewHandler(h))
	if business != nil {
		mux.Handle("/", business)
	}
	return mux
}

// withGatewayProbes 包装 gateway 转码注册回调：转码 handler 由业务在 Start 期
// 才产出，探针在装配层包在返回值外层（与纯 HTTP 面复用同一函数）。
func withGatewayProbes(
	register func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error),
	h *health.Health,
	live, ready string,
) func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
	return func(ctx context.Context, conn *grpc.ClientConn) (http.Handler, error) {
		business, err := register(ctx, conn)
		if err != nil {
			return nil, err
		}
		return withProbes(business, h, live, ready), nil
	}
}
