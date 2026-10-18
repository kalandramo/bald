// Package metricshttp 持有 metrics 暴露端的独立监听面（Prometheus 抓取端口）。
//
// B7（批 3，D1 §4.1）：真起监听面是**能力构造**而非 App 编排——原
// appkit.StartMetricsServer 自建 mux/http.Server 并 go ListenAndServe，随
// 职责边界落点下沉到本包。后端 Provider（如 contrib/observability-otlp 的
// contract）在自己的 Build 内调用 Start，把返回的 *Server 作为 appkit
// MetricsExposure 交回引擎；引擎把停机合成进 canonical 槽位——先停暴露端
// （Stop）、再执行 Provider 的 flush，尾批指标不丢失。
//
// 旁路语义与既有约定一致：监听失败异步记日志不 panic——指标面挂了不该拖垮
// 业务。
package metricshttp

import (
	"context"
	"net/http"

	log "github.com/kalandramo/bald/log"
)

// 指标暴露端缺省值（根治：与 gRPC :9090 错峰）。
const (
	// DefaultAddr 是暴露端监听地址缺省值。
	DefaultAddr = ":9091"
	// DefaultPath 是抓取路径缺省值。
	DefaultPath = "/metrics"
)

// Server 是 metrics 暴露端的独立监听面（实现 appkit.MetricsExposure）。
type Server struct {
	srv *http.Server
}

// Start 在独立端口起指标暴露端（Prometheus 抓取），返回 *Server 供停机 Stop
// （与业务 server 生命周期解耦的独立监听面）。addr / path 空串走缺省
// （:9091 / /metrics）。监听失败异步记日志不 panic（旁路语义）。
func Start(addr, path string, h http.Handler) *Server {
	if addr == "" {
		addr = DefaultAddr
	}
	if path == "" {
		path = DefaultPath
	}
	mux := http.NewServeMux()
	mux.Handle(path, h)
	srv := &http.Server{Addr: addr, Handler: mux}
	s := &Server{srv: srv}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error(context.Background(), "metrics server stopped", "addr", addr, "error", err.Error())
		}
	}()
	return s
}

// Stop 停止暴露端：关闭监听、排空在途请求（http.Server.Shutdown 语义）。
// 不 flush 指标——flush 归 MetricsProvider 的 shutdown，停机序「先停暴露端
// 再 flush」由 appkit 引擎合成（见 storeProduct 的 metrics 分支）。
func (s *Server) Stop(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
