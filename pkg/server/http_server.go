package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	confv1 "github.com/kalandramo/bald/pkg/conf/gen/go/bald/config/v1"
	"github.com/kalandramo/bald/pkg/conf"
)

// ReadinessFunc 是就绪探针回调：返回 nil 表示依赖就绪（可接受流量），
// 返回非 nil 错误表示未就绪（应被 K8s 从 Service 端点摘掉）。
// 与 gRPC 侧的 health.SetServingStatus 共用同一语义，保证两端对称。
type ReadinessFunc func(ctx context.Context) error

// HTTPServer 封装标准库 net/http，实现 Server 契约。
// 基于 proto 的 confv1.Http：Tls.Enabled=false 时为纯 HTTP，Enabled=true 时为 HTTPS
// （TLS 采用 Smart Mode，见 conf.ResolveTLS / conf.MustTLSConfig）。自动挂载
// /healthz（存活探针，进程在即 200）与 /readyz（就绪探针，由 readiness 回调决定）。
//
// 并发安全：ln 由 mu 保护。Start 在 AppKit 的 errgroup goroutine 中执行，
// 而 Endpoint() 由 appkit 主 goroutine 轮询（waitForEndpoints），二者并发，
// 无保护时构成数据竞争（go test -race 可复现）。
type HTTPServer struct {
	*http.Server
	cfg *confv1.Http

	readiness ReadinessFunc // 可为 nil（nil 时 /readyz 等同 /healthz）

	mu sync.RWMutex
	ln net.Listener // 实际监听器，用于解析 Endpoint（支持 :0 动态端口）
}

// NewHTTPServer 基于 http.Handler 构造一个 HTTPServer。
// cfg 为 proto 的 Http 配置：cfg.Tls.Enabled=true 时启用 HTTPS，否则纯 HTTP。
// readiness 为可选的就绪探针回调：传 nil 时 /readyz 恒返回 200（仅作存活）。
// 框架会把 /healthz、/readyz 注册到内部 mux，业务 handler 挂载在其余路径；
// 若业务 handler 自身也注册了同名路径，则业务优先（框架 probe 不覆盖）。
func NewHTTPServer(cfg *confv1.Http, handler http.Handler, readiness ReadinessFunc) *HTTPServer {
	mux := http.NewServeMux()
	// 业务路由优先：先挂业务 handler，框架 probe 仅当路径未被占用时兜底。
	if handler != nil {
		mux.Handle("/", handler)
	}
	srv := &HTTPServer{
		Server:    &http.Server{Addr: cfg.GetAddr(), Handler: mux},
		cfg:       cfg,
		readiness: readiness,
	}
	// 框架探针注册为精确路径，优先于业务挂载的 "/" 根路径匹配；
	// 若业务也用 HandleFunc("/healthz", ...) 注册了同名精确路径，标准库 mux
	// 会 panic（重复注册），此时以业务实现为准——框架不覆盖业务自定义探针。
	mux.HandleFunc("/healthz", srv.handleHealthz)
	mux.HandleFunc("/readyz", srv.handleReadyz)
	return srv
}

// Options 返回该 server 直消费的 proto 配置（实现 server.Server 契约的 Options()）。
func (s *HTTPServer) Options() any { return s.cfg }

// attachHandler 把业务 handler 挂载到根路径 "/"，覆盖构造时传入的占位 handler。
// 业务优先：仅当 "/" 尚未被占用时生效（同包内延迟挂载 gateway 用）。
// 框架探针（/healthz、/readyz 精确路径）不受影响。
func (s *HTTPServer) attachHandler(h http.Handler) {
	if mux, ok := s.Server.Handler.(*http.ServeMux); ok {
		mux.Handle("/", h)
		return
	}
	s.Server.Handler = h
}

// handleHealthz：存活探针，进程在即返回 200，不做任何依赖检查。
func (s *HTTPServer) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz：就绪探针，readiness 为 nil 或返回 nil 时 200，否则 503。
func (s *HTTPServer) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if s.readiness == nil {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
		return
	}
	if err := s.readiness(r.Context()); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable) // 503
		_, _ = w.Write([]byte(err.Error()))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// Start 启动 HTTP(S) 服务器（阻塞）。
func (s *HTTPServer) Start(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.cfg.GetAddr())
	if err != nil {
		return fmt.Errorf("http listen %s: %w", s.cfg.GetAddr(), err)
	}

	s.mu.Lock()
	s.ln = lis
	s.mu.Unlock()

	if s.cfg.GetTls().GetEnabled() {
		tlsConfig, terr := conf.ResolveTLS(s.cfg.GetTls())
		if terr != nil {
			// 监听器已创建，构建 TLS 配置失败属于启动失败路径，
			// 必须关闭监听器，否则造成文件描述符泄漏。
			_ = lis.Close()
			return fmt.Errorf("build http tls config: %w", terr)
		}
		s.TLSConfig = tlsConfig
		err = s.ServeTLS(lis, "", "")
	} else {
		err = s.Serve(lis)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop 优雅停止 HTTP 服务器。
func (s *HTTPServer) Stop(ctx context.Context) error {
	return s.Shutdown(ctx)
}

// Endpoint 返回实际监听地址（支持 ":0" 动态端口）。
// 通配符 / 仅端口绑定（如 ":8080"）会被解析为本机可达 IP，确保注册到服务发现的
// endpoint 对其他节点可直连，而非 "0.0.0.0:8080" 这类不可达通配符。
func (s *HTTPServer) Endpoint() string {
	scheme := conf.Scheme(s.cfg.GetTls())

	// 先取出快照再解锁：Extract 内部会枚举网卡（net.Interfaces），
	// 是相对耗时的系统调用，不能在持锁期间执行。
	s.mu.RLock()
	ln := s.ln
	s.mu.RUnlock()

	if ln != nil {
		hostPort, err := Extract(s.cfg.GetAddr(), ln)
		if err == nil {
			return scheme + "://" + hostPort
		}
		return scheme + "://" + ln.Addr().String()
	}
	// 未监听（Start 尚未执行）时返回空字符串，供 appkit.waitForEndpoints 正确等待，
	// 避免把未就绪的地址注册到服务发现。
	return ""
}
