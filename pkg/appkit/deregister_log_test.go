package appkit

// 反注册失败必须可见（§4.1）：注册中心里留下条目 = 流量继续打到已停实例，
// 恰是反注册想避免的事。失败不阻断停机（停机期间没有可返回错误的调用方语义），
// 但要落日志；这两条都在这里锁住。
//
// 捕获走真实装配路径：契约 logger 段置 nil，StdLoggerProvider 回退引导 bslog，
// handler 指向内存 buffer——不 hack 全局 logger，避免与装配期的 logger 换入换出竞跑。

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	"github.com/kalandramo/bald/log/bslog"
	"github.com/kalandramo/bald/registry"
)

// failingDeregistrar：Deregister 恒报错（注册中心不可达/租约失败的形态）。
type failingDeregistrar struct {
	err         error
	registered  atomic.Int32
	deregisters atomic.Int32
}

func (f *failingDeregistrar) Register(context.Context, *registry.ServiceInstance) error {
	f.registered.Add(1)
	return nil
}

func (f *failingDeregistrar) Deregister(context.Context, *registry.ServiceInstance) error {
	f.deregisters.Add(1)
	return f.err
}

// deregFailCfg 构造带 registry 段、logger 段缺失（走 buffer）的契约。
func deregFailCfg() (*bootstrapv1.BootstrapConfig, *bytes.Buffer) {
	cfg := bconfNewBootstrapWithRegistry("fake")
	cfg.Logger = nil
	dynamicAddr(cfg)

	buf := &bytes.Buffer{}
	return cfg, buf
}

func bufferLogger(buf *bytes.Buffer) Provider {
	return StandardLoggerProvider(bslog.WithHandler(
		slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
}

// 正常停机路径：反注册失败 → 记日志，Run 仍返回 nil（调用方无需为注册中心抖动重启进程）。
func TestRun_DeregisterFailureIsLogged(t *testing.T) {
	cfg, buf := deregFailCfg()
	wantErr := errors.New("registry unreachable")
	stub := &failingDeregistrar{err: wantErr}

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		stubRegistrarProvider(stub, nil),
		bufferLogger(buf),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	select {
	case <-a.startedCh:
	case err := <-done:
		cancel()
		t.Fatalf("Run returned early: %v", err)
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run did not start within 5s")
	}
	if n := stub.registered.Load(); n != 1 {
		cancel()
		t.Fatalf("Register calls = %d, want 1", n)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil（反注册失败不改变停机语义）", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return")
	}

	if n := stub.deregisters.Load(); n != 1 {
		t.Fatalf("Deregister calls = %d, want 1", n)
	}
	logged := buf.String()
	if !strings.Contains(logged, "deregister failed") {
		t.Fatalf("日志缺反注册失败记录：%s", logged)
	}
	if !strings.Contains(logged, wantErr.Error()) {
		t.Errorf("日志未带上底层错误：%s", logged)
	}
}

// afterStart 失败路径同样要记：这条路径上实例已注册，吞掉错误就是留一个幽灵节点。
func TestRun_DeregisterFailureOnAfterStartError(t *testing.T) {
	cfg, buf := deregFailCfg()
	hookErr := errors.New("afterStart boom")
	stub := &failingDeregistrar{err: errors.New("registry unreachable")}

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		AfterStart(func(context.Context) error { return hookErr }),
		stubRegistrarProvider(stub, nil),
		bufferLogger(buf),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	err := a.Run(ctx)
	if !errors.Is(err, hookErr) {
		t.Fatalf("Run = %v, want afterStart 错误原样返回", err)
	}
	if n := stub.deregisters.Load(); n != 1 {
		t.Fatalf("Deregister calls = %d, want 1", n)
	}
	if logged := buf.String(); !strings.Contains(logged, "deregister failed") {
		t.Fatalf("日志缺反注册失败记录：%s", logged)
	}
}
