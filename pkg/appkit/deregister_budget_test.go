package appkit

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/kalandramo/bald/registry"
)

// blockingDeregistrar：Deregister 阻塞到 ctx 被取消（注册中心不可达时 etcd
// Delete 等连接就绪的形态），并记录调用方 ctx 是否带 deadline。
type blockingDeregistrar struct {
	sawDeadline atomic.Bool
	canceled    atomic.Bool
	deregisters atomic.Int32
}

func (f *blockingDeregistrar) Register(context.Context, *registry.ServiceInstance) error {
	return nil
}

func (f *blockingDeregistrar) Deregister(ctx context.Context, _ *registry.ServiceInstance) error {
	f.deregisters.Add(1)
	if _, ok := ctx.Deadline(); ok {
		f.sawDeadline.Store(true)
	}
	<-ctx.Done()
	f.canceled.Store(true)
	return ctx.Err()
}

// TestRun_DeregisterBoundedByStopTimeout 复现 AR-2：deregister 此前用
// context.Background()——注册中心不可达时永久阻塞，进程走不到 stopAll，
// stop_timeout 形同虚设，只能被编排系统 SIGKILL。修复后 deregister 带
// 停机阶段预算（stopTimeout/2），到点放弃、停机流程继续。
func TestRun_DeregisterBoundedByStopTimeout(t *testing.T) {
	cfg, _ := deregFailCfg()
	cfg.Logger = nil
	dynamicAddr(cfg)
	// stop_timeout 取自契约 app 段（AR-12：Option 声明的值被预装载契约覆盖——
	// 这条测试同时也是 AR-12 的实证）。400ms → 预算 200ms。
	cfg.App.StopTimeout = durationpb.New(400 * time.Millisecond)
	stub := &blockingDeregistrar{}

	a := assembleForTest(t, cfg,
		WithHTTP(new(http.ServeMux)),
		stubRegistrarProvider(stub, nil),
		bufferLogger(&bytes.Buffer{}),
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

	start := time.Now()
	cancel()
	select {
	case err := <-done:
		elapsed := time.Since(start)
		if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Run err = %v（取消类错误不视为致命）", err)
		}
		if elapsed > 3*time.Second {
			t.Fatalf("deregister 无界：%v 才返回（AR-2，预算应为 stopTimeout/2=200ms）", elapsed)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("cancel 后 Run 15s 未返回——deregister 挂死（AR-2）")
	}

	if n := stub.deregisters.Load(); n != 1 {
		t.Fatalf("Deregister calls = %d, want 1", n)
	}
	if !stub.sawDeadline.Load() {
		t.Fatal("deregister 的 ctx 应携带 deadline（stopTimeout/2 预算）")
	}
	if !stub.canceled.Load() {
		t.Fatal("预算到期 deregister 的 ctx 应被取消")
	}
}
