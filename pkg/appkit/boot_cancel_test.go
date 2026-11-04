package appkit

// 启动窗口内「调用方取消」与「某个 server 启动失败」必须是两种可区分的结局。
//
// 背景：Run 的端点等待（waitForEndpoints）跑在 servers 启动之后、进入主等待
// 之前。真实 gRPC server 在 net.Listen 完成前 Endpoint() 返回空串，所以这个
// 窗口对任何带 gRPC 面的进程都存在。窗口内 gctx 被取消有两条来源：
//   - 调用方取消（SIGTERM 打到仍在启动的进程）——主等待一侧早已按「不是装配
//     错误」处理（见 Run 尾部对 context.Canceled 的过滤），启动侧却当致命错误；
//   - errgroup 因某个 server 的 Start 失败而取消——原先返回的是
//     「appkit: wait for endpoints canceled: context canceled」，把真实失败
//     （如 bind 冲突）整条抹掉。
// 两条都是外部消费者实际会撞上的：bald-admin 的装配 e2e 在共享 runner 上
// 红过一次，报错正是被取消的那条。
//
// 同一条判据（startupOutcome）覆盖 Run 的全部五处启动分支——beforeStart 钩子、
// 组件顺序启动、端点等待、服务注册、afterStart 钩子；差别只在 errgroup 建立
// 之后的三处要先取 servers 的真实启动错误。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// endpointPendingSrv 的 Start 立刻返回、Endpoint 恒为 ":0"——模拟监听器迟迟
// 未定型的 server（真实 gRPC server 在 net.Listen 前返回空串，语义相同：
// 端点等待不会通过）。
type endpointPendingSrv struct{}

func (endpointPendingSrv) Start(context.Context) error { return nil }
func (endpointPendingSrv) Stop(context.Context) error  { return nil }
func (endpointPendingSrv) Endpoint() string            { return "http://:0" }

// 调用方在端点等待窗口内取消 → 干净退出（nil），不是装配错误。
func TestRun_CancelDuringEndpointWaitExitsClean(t *testing.T) {
	app := NewApp(Name("boot-cancel"), StopTimeout(2*time.Second),
		Servers(endpointPendingSrv{}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("启动窗口内取消应干净退出，得 err=%v", err)
	}
}

// 端点等待窗口内某个 server 的 Start 失败 → 返回**那个错误**，
// 不能报「wait for endpoints canceled」。
func TestRun_StartFailureDuringEndpointWaitSurfacesRealError(t *testing.T) {
	crash := errors.New("boom: listen tcp: address already in use")
	bad := newMock("bad")
	bad.startErr = crash

	app := NewApp(Name("boot-mask"), StopTimeout(2*time.Second),
		Servers(endpointPendingSrv{}, bad))

	// 调用方 ctx 永不取消：gctx 只能被 errgroup（即 bad 的崩溃）取消。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := app.Run(ctx)
	if err == nil {
		t.Fatal("server 启动失败必须让 Run 返回错误")
	}
	if !errors.Is(err, crash) {
		t.Fatalf("真实启动失败被端点等待掩盖：得 %v，want %v", err, crash)
	}
	if strings.Contains(err.Error(), "wait for endpoints") {
		t.Fatalf("错误文案仍是等待掩盖文案：%v", err)
	}
}

// errgroup 建立之前的两处启动窗口（beforeStart 钩子、组件顺序启动）同一条判据：
// 调用方取消打在这两处时，同一信号的结局不能因为它比端点等待早到几毫秒就从
// 「正常停机」变成「装配错误」。

// cancelOnStartComp 的 Start 阻塞到 ctx 结束并原样返回 ctx.Err()——取消打断组件启动。
type cancelOnStartComp struct{ name string }

func (c cancelOnStartComp) Name() string { return c.name }
func (c cancelOnStartComp) Start(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}
func (cancelOnStartComp) Dispose(context.Context) error { return nil }

// TestRun_CancelDuringBeforeStartExitsClean beforeStart 窗口内取消 → 干净退出。
func TestRun_CancelDuringBeforeStartExitsClean(t *testing.T) {
	app := NewApp(Name("boot-before"), StopTimeout(2*time.Second),
		BeforeStart(func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("beforeStart 窗口内取消应干净退出，得 err=%v", err)
	}
}

// TestRun_CancelDuringComponentStartExitsClean 组件启动窗口内取消 → 干净退出。
func TestRun_CancelDuringComponentStartExitsClean(t *testing.T) {
	app := NewApp(Name("boot-comp"), StopTimeout(2*time.Second),
		componentsOpt(cancelOnStartComp{"test.cancel"}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); err != nil {
		t.Fatalf("组件启动窗口内取消应干净退出，得 err=%v", err)
	}
}

// TestRun_CancelledDuringBeforeStartWithRealErrorStillFails 判据的第二半：调用方
// 确实取消了，但这一步报的是与取消无关的真实失败 → 仍须返回该错误（fail-fast
// 是这个框架的既定口径，只看「调用方已取消」会把真失败一起放过）。
func TestRun_CancelledDuringBeforeStartWithRealErrorStillFails(t *testing.T) {
	boom := errors.New("beforeStart 的真实装配失败")
	app := NewApp(Name("boot-before-real"), StopTimeout(2*time.Second),
		BeforeStart(func(ctx context.Context) error {
			<-ctx.Done()
			return boom
		}))

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	if err := app.Run(ctx); !errors.Is(err, boom) {
		t.Fatalf("调用方取消不该吞掉真实装配失败：得 %v，want %v", err, boom)
	}
}
