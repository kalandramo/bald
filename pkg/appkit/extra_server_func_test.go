package appkit

// extra_server_func_test.go —— WithExtraServerFunc：运行期构造额外服务器。
//
// 背景（能力）：Servers 收已构造实例（调用点即需存在）；若该服务器
// 需要运行期资源（如就绪的认证器），应用只能传「请求期解析」代理。本 Option 收
// 工厂函数，延后到 Run 期 beforeStart 链末尾调用。
//
// 本文件锁三条不变量：
//  1. 工厂在 **Run 期**调用（构造期零调用）——能力成立的前提；
//  2. 无 http/grpc 主面（无 server Provider 产物）时**同样生效**——独立于主协议面；
//  3. 工厂返回 nil 时不 panic、不污染启动序列（降级语义）。

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	bconf "github.com/kalandramo/bald/bconf"
	log "github.com/kalandramo/bald/log"
	"github.com/kalandramo/bald/transport"
)

// TestAssemble_ExtraServerFuncRunTime 断言工厂在 Run 期调用、构造期零调用，
// 且顺序为 beforeStart（建依赖）→ extraServerFn（用依赖）→ 服务器启动。
func TestAssemble_ExtraServerFuncRunTime(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap() // 预填 server 段豁免（无能力 ⇒ 不绑端口，L3 不报错）

	var mu sync.Mutex
	var order []string
	mark := func(s string) {
		mu.Lock()
		order = append(order, s)
		mu.Unlock()
	}
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(order, ",")
	}

	var started atomic.Bool
	srv := &probeServer{name: "extra", started: &started, mark: mark}

	a := assembleForTest(t, cfg,
		WithExtraServerFunc(func(context.Context) (transport.Server, error) {
			mark("extraServerFn")
			return srv, nil
		}),
		BeforeStart(func(context.Context) error { mark("beforeStart"); return nil }),
	)

	if got := snapshot(); strings.Contains(got, "extraServerFn") {
		t.Fatalf("extra server factory ran during construction (order=%q); want deferred to Run", got)
	}

	runBriefly(t, a, 50*time.Millisecond)

	if got := snapshot(); got != "beforeStart,extraServerFn,start" {
		t.Fatalf("wiring order = %q, want \"beforeStart,extraServerFn,start\"", got)
	}
	if !started.Load() {
		t.Fatal("Run 期构造的额外服务器未被启动")
	}
}

// TestAssemble_ExtraServerFuncNilSkipped 断言工厂返回 nil 时：不 panic、
// 不把 nil 塞进启动序列（避免 nil 解引用）。
func TestAssemble_ExtraServerFuncNilSkipped(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap() // 预填 server 段豁免（无能力 ⇒ 不绑端口，L3 不报错）

	a := assembleForTest(t, cfg,
		WithExtraServerFunc(func(context.Context) (transport.Server, error) { return nil, nil }),
	)

	// 正常启停（若 nil 被塞入序列，Start 会 panic）。
	runBriefly(t, a, 30*time.Millisecond)
	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil（nil 额外服务器应被跳过）", a.Err())
	}
}

// TestAssemble_ExtraServerFuncWithoutMainProtocol 断言**无 http/grpc 主面**时
// （零能力声明，server Provider 不构造）额外服务器仍被构造并启动——本 Option
// 独立于主协议面。
func TestAssemble_ExtraServerFuncWithoutMainProtocol(t *testing.T) {
	old := log.GetLogger()
	t.Cleanup(func() { log.SetLogger(old) })

	cfg := bconf.NewBootstrap() // 预填 server 段豁免（无能力 ⇒ 不绑端口，L3 不报错）

	var started atomic.Bool
	srv := &probeServer{name: "extra-only", started: &started, mark: func(string) {}}

	// 注意：**不**传 WithHTTP / WithGRPC —— 只有额外服务器。
	a := assembleForTest(t, cfg,
		WithExtraServerFunc(func(context.Context) (transport.Server, error) { return srv, nil }),
	)

	runBriefly(t, a, 50*time.Millisecond)

	if !started.Load() {
		t.Fatal("无 http/grpc 主面时额外服务器未被启动；WithExtraServerFunc 应独立于主协议面")
	}
	if a.Err() != nil {
		t.Fatalf("a.Err() = %v, want nil", a.Err())
	}
}

// probeServer 是最小 transport.Server 实现，记录 Start/Stop 到共享 order。
type probeServer struct {
	name    string
	started *atomic.Bool
	mark    func(string)
	stop    chan struct{}
}

func (p *probeServer) Start(ctx context.Context) error {
	p.started.Store(true)
	p.mark("start")
	p.stop = make(chan struct{})
	<-ctx.Done()
	close(p.stop)
	return nil
}

func (p *probeServer) Stop(context.Context) error { return nil }

func (p *probeServer) Endpoint() string { return "probe://" + p.name }
