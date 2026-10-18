package appkit

// domain_test.go 钉住业务域引擎消费的四个不变量（装配层评估 §4.3）：
//  1. Wire 时点窗口：阶段 B 资源面就绪之后（App.Resource 可取产物）、业务
//     BeforeStart 之后、迟后阶段（server 构建）之前——两约束夹出的窗口；
//  2. 多域次序 = 声明序（Wire 与 Routes 均按 MustServe 变参序）；
//  3. 域 Wire/Routes 失败 fail-fast（错误带域名，经 Run 失败路径返回）；
//  4. Services 并入 server 构建的注册回调（业务注册在前、域在后），Routes/
//     Services 的装配期校验 fail-fast（无 HTTP 面 / 无 server.grpc 段）。

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	bconf "github.com/kalandramo/bald/bconf"
	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// recorder 是时序断言的顺序记录器（wiring_order_test 同款，加互斥锁）。
type recorder struct {
	mu sync.Mutex
	os []string
}

func (r *recorder) mark(s string) {
	r.mu.Lock()
	r.os = append(r.os, s)
	r.mu.Unlock()
}

func (r *recorder) snapshot() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.os, ",")
}

// stubScriptProvider 消费 script 段（表外顶层段 → 阶段 B 构建；产物为非 map
// 值 → 落资源面 "script"，供域 Wire 经 App.Resource 取用）。script 段被本
// Provider 触发即视为已消费，L3 不报错。
type stubScriptProvider struct {
	rec *recorder
}

func (stubScriptProvider) Segments() []string { return []string{"script"} }

func (p stubScriptProvider) Apply(d *Decl) { d.Providers = append(d.Providers, p) }

func (p stubScriptProvider) Build(_ context.Context, _ BuildScope) (any, Cleanup, error) {
	if p.rec != nil {
		p.rec.mark("stageB")
	}
	return "stub-script", nil, nil
}

// TestDomain_WireWindow 钉住时点窗口：构造期不执行；Run 期顺序恰为
// stageB（阶段 B Provider）→ wire（域 Wire，且能取到阶段 B 产物）→
// register（server 构建的注册回调——域的接线已完成）。
func TestDomain_WireWindow(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)
	cfg.Script = &bootstrapv1.Script{Engine: bootstrapv1.Script_LUA}

	rec := &recorder{}
	var wired bool
	dom := Domain{
		Name: "demo",
		Wire: func(_ context.Context, a *App) error {
			// 约束①：资源面已就绪——阶段 B 产物经 App.Resource 可取。
			v, ok := a.Resource("script")
			if !ok || v != "stub-script" {
				return errors.New("resource face not ready at Wire time")
			}
			rec.mark("wire")
			wired = true
			return nil
		},
	}

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		StandardServerProvider(),
		stubScriptProvider{rec: rec},
		WithGRPC(func(*grpc.Server) {
			rec.mark("register")
			// 约束②：server 构建时域已完成接线（其路由/服务注册已生效）。
			if !wired {
				t.Error("server built before domain Wire")
			}
		}),
		dom,
	)

	// 构造期：域接线不得执行（迟于 Run 的 beforeStart 链）。
	if wired {
		t.Fatal("domain Wire ran during construction")
	}
	runBriefly(t, a, 50*time.Millisecond)

	if got := rec.snapshot(); got != "stageB,wire,register" {
		t.Fatalf("wiring order = %q, want stageB,wire,register", got)
	}
}

// TestDomain_DeclarationOrder 钉住多域次序 = 声明序：逐域 Wire → Routes，
// 后一域在前一域完整接线之后（跨域端口依赖按声明序满足）。
func TestDomain_DeclarationOrder(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	rec := &recorder{}
	d1 := Domain{
		Name:   "d1",
		Wire:   func(context.Context, *App) error { rec.mark("wire:d1"); return nil },
		Routes: func(http.Handler) error { rec.mark("routes:d1"); return nil },
	}
	d2 := Domain{
		Name:   "d2",
		Wire:   func(context.Context, *App) error { rec.mark("wire:d2"); return nil },
		Routes: func(http.Handler) error { rec.mark("routes:d2"); return nil },
	}

	// 声明序 d2 在前：次序必须跟着声明走，而非 Domain 值的字典序。
	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), d2, d1)
	runBriefly(t, a, 30*time.Millisecond)

	if got := rec.snapshot(); got != "wire:d2,routes:d2,wire:d1,routes:d1" {
		t.Fatalf("wiring order = %q, want declaration order", got)
	}
}

// TestDomain_WireFailFast 钉住域 Wire 失败 fail-fast：Run 返回错误且带域名。
func TestDomain_WireFailFast(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	dom := Domain{
		Name: "boom",
		Wire: func(context.Context, *App) error { return errors.New("port build failed") },
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), dom)
	runExpectErr(t, a, "domain \"boom\"", "port build failed")
}

// TestDomain_RoutesFailFast 钉住域 Routes 失败 fail-fast（错误带域名）。
func TestDomain_RoutesFailFast(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	dom := Domain{
		Name:   "badroute",
		Routes: func(http.Handler) error { return errors.New("router mismatch") },
	}

	a := assembleForTest(t, cfg, WithHTTP(new(http.ServeMux)), dom)
	runExpectErr(t, a, "domain \"badroute\" routes", "router mismatch")
}

// TestDomain_AssembleFailFast 钉住装配期校验（先于 Run、先于阶段 A Logger）：
// Routes 无 HTTP 面、Services 无 server.grpc 段、Name 缺失/重复均构造期报错。
func TestDomain_AssembleFailFast(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	noHTTP := bconf.NewBootstrap()
	noHTTP.Server.Http = nil
	if _, err := assembleForTestErr(noHTTP,
		Domain{Name: "d", Routes: func(http.Handler) error { return nil }}); err == nil {
		t.Fatal("expected error: Routes declared but no HTTP face")
	} else if !strings.Contains(err.Error(), "no HTTP face") {
		t.Fatalf("error %q does not mention the missing HTTP face", err)
	}

	noGRPC := bconf.NewBootstrap()
	noGRPC.Server.Grpc = nil
	if _, err := assembleForTestErr(noGRPC,
		Domain{Name: "d", Services: func(*grpc.Server) {}}); err == nil {
		t.Fatal("expected error: Services declared but server.grpc is nil")
	} else if !strings.Contains(err.Error(), "server.grpc is nil") {
		t.Fatalf("error %q does not mention server.grpc", err)
	}

	cfg := bconf.NewBootstrap()
	if _, err := assembleForTestErr(cfg, Domain{Routes: func(http.Handler) error { return nil }}); err == nil {
		t.Fatal("expected error: domain without Name")
	}
	if _, err := assembleForTestErr(cfg,
		Domain{Name: "dup"}, Domain{Name: "dup"}); err == nil {
		t.Fatal("expected error: duplicate domain name")
	}
}

// TestDomain_ServicesComposed 钉住 Services 组合语义：server 构建的注册回调
// 先跑业务注册、再按声明序跑域注册（同一 *grpc.Server），且彼时 Wire 已完成
// （域端口就绪）。
func TestDomain_ServicesComposed(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	rec := &recorder{}
	var wired bool
	a := assembleForTest(t, cfg,
		StandardServerProvider(),
		WithGRPC(func(*grpc.Server) { rec.mark("register:business") }),
		Domain{
			Name: "svc",
			Wire: func(context.Context, *App) error { wired = true; return nil },
			Services: func(*grpc.Server) {
				rec.mark("register:domain")
				if !wired {
					t.Error("domain Services invoked before Wire")
				}
			},
		},
	)
	runBriefly(t, a, 50*time.Millisecond)

	if got := rec.snapshot(); got != "register:business,register:domain" {
		t.Fatalf("register order = %q, want register:business,register:domain", got)
	}
}

// TestDomain_ServicesAloneBuildsGRPC 钉住「业务未声明 WithGRPC、域自持
// Services」：组合出的注册回调使 std server Provider 正常构建 gRPC 面。
func TestDomain_ServicesAloneBuildsGRPC(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap()
	dynamicAddr(cfg)

	rec := &recorder{}
	a := assembleForTest(t, cfg,
		StandardServerProvider(),
		Domain{Name: "svc", Services: func(*grpc.Server) { rec.mark("register:domain") }},
	)
	runBriefly(t, a, 50*time.Millisecond)

	if got := rec.snapshot(); got != "register:domain" {
		t.Fatalf("register order = %q, want register:domain", got)
	}
}

// TestDomain_ApplyAppendsDecl 钉住 Option 形状：Domain 值经 Apply 追加进
// Decl.Domains（MustServe 变参序 = Apply 序 = 声明序的载体）。
func TestDomain_ApplyAppendsDecl(t *testing.T) {
	d := newDecl(Domain{Name: "a"}, Domain{Name: "b"})
	if len(d.Domains) != 2 {
		t.Fatalf("Decl.Domains = %d entries, want 2", len(d.Domains))
	}
	if d.Domains[0].Name != "a" || d.Domains[1].Name != "b" {
		t.Fatalf("Decl.Domains order = [%q %q], want [a b]", d.Domains[0].Name, d.Domains[1].Name)
	}
}
