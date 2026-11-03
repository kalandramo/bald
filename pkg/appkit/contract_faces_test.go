package appkit

// contract_faces_test.go 钉住契约钩子的装配语义：钩子收到的是装载后的真实段、
// 两条声明口不得同时生效、钩子建的资源停机释放、启动失败也回滚、--plan 只认
// 声明不执行用户代码。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

func httpMW(rate float64, burst int32) *bootstrapv1.Server_Http_Middleware {
	return &bootstrapv1.Server_Http_Middleware{
		RateLimit: &bootstrapv1.Server_Http_Middleware_RateLimit{Rate: rate, Burst: burst},
	}
}

func grpcMW(ms int64) *bootstrapv1.Server_Grpc_Middleware {
	return &bootstrapv1.Server_Grpc_Middleware{
		Timeout: &bootstrapv1.Server_Grpc_Middleware_Timeout{DefaultTimeoutMs: ms},
	}
}

// restoreLogger 复原 assemble 阶段 A 替换的全局 logger（测试之间的隔离）。
func restoreLogger(t *testing.T) {
	t.Helper()
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })
}

// runThenStop 真实启动到 startedCh，在启动态执行 fn（可为 nil），再取消 ctx 等
// Run 返回——Effect 回放（钩子资源的释放点）就发生在这一段。
func runThenStop(t *testing.T, a *App, fn func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	select {
	case <-a.startedCh:
	case err := <-done:
		cancel()
		t.Fatalf("Run returned before start: %v", err)
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("Run did not reach startedCh")
	}
	if fn != nil {
		fn()
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// stdFaces 是测试用的最小框架 Provider 集：assemble 不自动注入内置默认，真实
// 应用在生成物 Options() 里给 std.Standard()。
func stdFaces() Option { return Providers(StandardHTTPProvider(), StandardGRPCProvider()) }

// TestContractFace_HTTPSeesLoadedSegment 是「契约段有真消费者」的判据：钩子拿到
// 的就是装载后的 server.http.middleware，不是零值、也不是调用方自拼的副本。
func TestContractFace_HTTPSeesLoadedSegment(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	want := httpMW(10, 3)
	cfg.Server.Http.Middleware = want

	var got *bootstrapv1.Server_Http_Middleware
	a, err := assemble(newDecl(stdFaces(), WithHTTPContract(
		func(mw *bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			got = mw
			return http.NotFoundHandler(), nil, nil
		})), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got != want {
		t.Fatalf("hook received %+v, want the contract segment %+v", got, want)
	}
	if a.httpHandler == nil {
		t.Fatal("resolved handler did not reach the app")
	}
	runThenStop(t, a, nil)
	if len(a.servers) != 1 {
		t.Fatalf("HTTP face did not start a server: %d", len(a.servers))
	}
}

// TestContractFace_GRPCSeesLoadedSegment 对偶：gRPC 钩子收到 server.grpc.middleware，
// 产出的选项并入 GRPCOptionFuncs 队列（链序 = 登记顺序，见 Decl 注释）。
func TestContractFace_GRPCSeesLoadedSegment(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	want := grpcMW(40)
	cfg.Server.Grpc.Middleware = want

	var got *bootstrapv1.Server_Grpc_Middleware
	marker := grpc.EmptyServerOption{}
	spec := newDecl(
		stdFaces(),
		WithGRPC(func(*grpc.Server) {}),
		WithGRPCOptionsContract(func(mw *bootstrapv1.Server_Grpc_Middleware) ([]grpc.ServerOption, func() error, error) {
			got = mw
			return []grpc.ServerOption{marker}, nil, nil
		}),
	)
	a, err := assemble(spec, cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if got != want {
		t.Fatalf("hook received %+v, want %+v", got, want)
	}
	if len(spec.GRPCOptionFuncs) != 1 {
		t.Fatalf("contract hook did not join the option-factory queue: %d entries", len(spec.GRPCOptionFuncs))
	}
	if opts := spec.GRPCOptionFuncs[0](); len(opts) != 1 || opts[0] != marker {
		t.Fatalf("hook output did not survive into the queue: %+v", opts)
	}
	runThenStop(t, a, nil)
}

// TestContractFace_BothHTTPDeclsFail 钉住「两条口二选一」：不是末者胜——静默
// 覆盖等于让另一条声明里的链配置不生效且不吭声。
func TestContractFace_BothHTTPDeclsFail(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	_, err := assemble(newDecl(
		WithHTTP(new(http.ServeMux)),
		WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return http.NotFoundHandler(), nil, nil
		})), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "declared twice") {
		t.Fatalf("want declared-twice error, got %v", err)
	}
}

// TestContractFace_HTTPContractNilClearsFace：业务/变体用 WithHTTP(nil) 撤面时，
// 契约钩子一并撤——否则已声明的钩子会把面装回来。
func TestContractFace_HTTPContractNilClearsFace(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	called := 0
	spec := newDecl(
		WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			called++
			return http.NotFoundHandler(), nil, nil
		}),
		WithHTTP(nil),
	)
	a, err := assemble(spec, cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if called != 0 {
		t.Fatalf("cleared face still ran the hook %d time(s)", called)
	}
	if a.httpHandler != nil {
		t.Fatal("WithHTTP(nil) must leave the app without an HTTP face")
	}
	// 不 Run：撤面后 server 段确实没有消费者，L3 覆盖检查会在启动时报错——
	// 那是预期的 fail-fast，本用例只验 Decl 层的「nil 清掉两条口」。
}

// TestContractFace_HandlerNilFails：钩子返回 nil handler 是接线错误，不能退化成
// 「面存在但什么都不响应」。
func TestContractFace_HandlerNilFails(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	_, err := assemble(newDecl(WithHTTPContract(
		func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return nil, nil, nil
		})), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "handler is nil") {
		t.Fatalf("want nil-handler error, got %v", err)
	}
}

// TestContractFace_HookErrorFailsFast：钩子的配置错误（桥在显式段非法时报的错）
// 必须原样终止启动，不能降级成「没有链」。两面各验一次。
func TestContractFace_HookErrorFailsFast(t *testing.T) {
	restoreLogger(t)
	sentinel := errors.New("boom")

	t.Run("grpc", func(t *testing.T) {
		cfg := bconf.NewBootstrap()
		dynamicAddr(cfg)
		_, err := assemble(newDecl(
			WithGRPC(func(*grpc.Server) {}),
			WithGRPCOptionsContract(func(*bootstrapv1.Server_Grpc_Middleware) ([]grpc.ServerOption, func() error, error) {
				return nil, nil, sentinel
			})), cfg, nil)
		if !errors.Is(err, sentinel) {
			t.Fatalf("hook error did not surface: %v", err)
		}
		if !strings.Contains(err.Error(), "WithGRPCOptionsContract") {
			t.Fatalf("error lost the declaring Option name: %v", err)
		}
	})

	t.Run("http", func(t *testing.T) {
		cfg := bconf.NewBootstrap()
		dynamicAddr(cfg)
		_, err := assemble(newDecl(WithHTTPContract(
			func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
				return nil, nil, sentinel
			})), cfg, nil)
		if !errors.Is(err, sentinel) {
			t.Fatalf("hook error did not surface: %v", err)
		}
		if !strings.Contains(err.Error(), "WithHTTPContract") {
			t.Fatalf("error lost the declaring Option name: %v", err)
		}
	})
}

// TestContractFace_CloseOnStop：钩子建的资源（桥新建的限流器）经框架 Effect 在
// 停机回放时释放，且只释放一次。
func TestContractFace_CloseOnStop(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	closes := 0
	a, err := assemble(newDecl(stdFaces(), WithHTTPContract(
		func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return http.NotFoundHandler(), func() error { closes++; return nil }, nil
		})), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if closes != 0 {
		t.Fatalf("close ran during assembly: %d", closes)
	}
	runThenStop(t, a, nil)
	if closes != 1 {
		t.Fatalf("close count = %d, want 1", closes)
	}
}

// TestContractFace_CloseOnFailedStart：启动失败（beforeStart 报错）时也要回放
// 释放——否则每次重启失败泄漏一个桶。
func TestContractFace_CloseOnFailedStart(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	closes := 0
	a, err := assemble(newDecl(
		stdFaces(),
		WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return http.NotFoundHandler(), func() error { closes++; return nil }, nil
		}),
		BeforeStart(func(context.Context) error { return errors.New("beforeStart failed") }),
	), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if err := a.Run(context.Background()); err == nil {
		t.Fatal("want Run to fail")
	}
	if closes != 1 {
		t.Fatalf("close count after failed start = %d, want 1", closes)
	}
}

// TestContractFace_RollbackOnAssembleError：能力声明与契约段不一致（声明了 HTTP
// 面却没有 server.http 段）时报错，此时钩子已经执行并建了资源——错误路径释放它。
func TestContractFace_RollbackOnAssembleError(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	cfg.Server.Http = nil // 声明了 HTTP 面却没有该段：validateCapabilities 报错
	closes := 0
	_, err := assemble(newDecl(WithHTTPContract(
		func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return http.NotFoundHandler(), func() error { closes++; return nil }, nil
		})), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "server.http is nil") {
		t.Fatalf("want nil-segment error, got %v", err)
	}
	if closes != 1 {
		t.Fatalf("close count on assemble error = %d, want 1", closes)
	}
}

// TestContractFace_VariantPatchGuardFails：变体在钩子求值之后补丁 middleware 段
// 时必须报错——「改了配置却不改变链」正是本项修复要消灭的那类静默。
func TestContractFace_VariantPatchGuardFails(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Server.Http.Middleware = &bootstrapv1.Server_Http_Middleware{
		RequestId: &bootstrapv1.Server_Http_Middleware_RequestId{
			HeaderName: proto.String("x-request-id"),
		},
	}
	_, err := assemble(newDecl(
		WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return http.NotFoundHandler(), nil, nil
		}),
		WithVariantDecls(VariantDecl{Name: "gw", Effects: VariantEffect{
			Patches: []VariantPatch{{
				Path:   "server.http.middleware.request_id.header_name",
				Value:  "x-trace-id",
				Reason: "pin the guard",
			}},
		}}),
	), cfg, nil)
	if err == nil || !strings.Contains(err.Error(), "patched server.http.middleware") {
		t.Fatalf("want variant patch guard error, got %v", err)
	}
}

// TestContractFace_PatchGuardIsQuietWithoutPatches 对偶：没补丁就不该报错——
// 判据不能是「声明了钩子就失败」。
func TestContractFace_PatchGuardIsQuietWithoutPatches(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Server.Http.Middleware = httpMW(1, 1)
	if _, err := assemble(newDecl(
		WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			return http.NotFoundHandler(), nil, nil
		}),
		WithVariantDecls(VariantDecl{Name: "gw", Detail: "no patches"}),
	), cfg, nil); err != nil {
		t.Fatalf("unconditional variant without patches: %v", err)
	}
}

// TestContractFace_PlanDeclaresWithoutExecuting：--plan 路径认得出两面（imperative
// 投影与域路由节点都在），但一行用户代码都不执行——计划期无副作用是既有口径。
// 两面都要断言：钩子形态在计划期只有声明、没有求值产物，任何一面只看 handler
// （或只看已登记的选项队列）都会让 --plan 与真实启动给出不同计划。
func TestContractFace_PlanDeclaresWithoutExecuting(t *testing.T) {
	cfgPath := writePlanConfig(t, "contract-face",
		"server:\n  http:\n    addr: \"127.0.0.1:0\"\n    middleware:\n      rate_limit:\n        rate: 10\n        burst: 3\n  grpc:\n    addr: \"127.0.0.1:0\"\n")
	httpCalled, grpcCalled := 0, 0
	p, err := Compile(context.Background(), ConfigFile(cfgPath),
		WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			httpCalled++
			return http.NotFoundHandler(), nil, nil
		}),
		WithGRPCOptionsContract(func(*bootstrapv1.Server_Grpc_Middleware) ([]grpc.ServerOption, func() error, error) {
			grpcCalled++
			return nil, nil, nil
		}),
		Domain{Name: "svc", Routes: func(http.Handler) error { return nil }})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if httpCalled != 0 || grpcCalled != 0 {
		t.Fatalf("plan path executed the contract hooks (http=%d grpc=%d)", httpCalled, grpcCalled)
	}
	seen := map[string]bool{}
	for _, im := range p.Imperative {
		if im.Kind == "serverCapability" {
			seen[im.Name] = true
		}
	}
	var routes bool
	for _, n := range p.Nodes {
		if n.Key == "domain.svc.routes" {
			routes = true
		}
	}
	if !seen["http"] || !seen["grpcOptions"] || !routes {
		t.Fatalf("plan lost the contract-declared faces (http=%v grpcOptions=%v routes=%v, got %+v)",
			seen["http"], seen["grpcOptions"], routes, p.Imperative)
	}
}

// TestContractFace_PlanVariantSkipsNilWrap：--plan 不执行钩子，契约形态在计划期
// 只有声明、没有 handler——命中变体的 WrapHTTP 必须跳过这一次（包 nil 会把
// panic 留给启动后的第一个请求），但路由清单照记；真实装配那一次必须包装到
// 求值后的 handler（否则「变体加了路由」只进计划，不进链）。
func TestContractFace_PlanVariantSkipsNilWrap(t *testing.T) {
	restoreLogger(t)
	newSpec := func(hookBody http.Handler) ([]Option, func() int, func() int) {
		var calls, nilCalls int
		wrap := func(next http.Handler) http.Handler {
			calls++
			if next == nil {
				nilCalls++
			}
			return next
		}
		opts := []Option{
			WithHTTPContract(func(*bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
				return hookBody, nil, nil
			}),
			WithVariantDecls(VariantDecl{Name: "gw", Effects: VariantEffect{
				Routes: []VariantRoute{{Method: "get", Path: "/gw/ping", RPC: "Ping"}},
			}}),
			WithVariantImpl(VariantImpl{Name: "gw", WrapHTTP: wrap}),
		}
		return opts, func() int { return calls }, func() int { return nilCalls }
	}

	cfgPath := writePlanConfig(t, "contract-face-wrap", "server:\n  http:\n    addr: \"127.0.0.1:0\"\n")
	opts, calls, nilCalls := newSpec(http.NewServeMux())
	p, err := Compile(context.Background(), append([]Option{ConfigFile(cfgPath)}, opts...)...)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if nilCalls() != 0 {
		t.Fatalf("plan path wrapped a nil handler (WrapHTTP calls=%d, nil-wraps=%d)", calls(), nilCalls())
	}
	var routed bool
	for _, r := range p.Routes {
		if r.Path == "/gw/ping" && r.Variant == "gw" {
			routed = true
		}
	}
	if !routed {
		t.Fatalf("variant route missing from plan: %+v", p.Routes)
	}

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	opts, calls, nilCalls = newSpec(http.NewServeMux())
	if _, err := assemble(newDecl(append([]Option{stdFaces()}, opts...)...), cfg, nil); err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if nilCalls() != 0 || calls() != 1 {
		t.Fatalf("real assembly WrapHTTP calls=%d nil=%d, want exactly one non-nil wrap", calls(), nilCalls())
	}
}

// TestContractFace_ResolvedHandlerServes：resolve 后的 handler 真在 HTTP 面上
// 服务，且看到的是装载后的段（防「声明了钩子、装了面、handler 却是空转」）。
func TestContractFace_ResolvedHandlerServes(t *testing.T) {
	restoreLogger(t)
	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Server.Http.Middleware = httpMW(1, 7)

	a, err := assemble(newDecl(stdFaces(), WithHTTPContract(
		func(mw *bootstrapv1.Server_Http_Middleware) (http.Handler, func() error, error) {
			burst := mw.GetRateLimit().GetBurst()
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if burst == 7 {
					w.WriteHeader(http.StatusTeapot)
					return
				}
				w.WriteHeader(http.StatusInternalServerError)
			}), nil, nil
		})), cfg, nil)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	runThenStop(t, a, func() {
		if a.servers[0].Endpoint() == "" {
			t.Error("HTTP face has no endpoint")
		}
		rec := httptest.NewRecorder()
		a.httpHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		if rec.Code != http.StatusTeapot {
			t.Fatalf("handler served %d, want 418 (must see the resolved burst=7 segment)", rec.Code)
		}
	})
}
