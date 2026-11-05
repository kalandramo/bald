package etcd

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/kalandramo/bald/retry"
)

// retreatSpy 记录心跳循环向退避策略要过哪几次等待。
// 每次给一个固定的小间隔：判据断的是「要过几次、按什么次序」，
// 而不是墙钟——下界型断言在慢机器上不会假红。
type retreatSpy struct {
	mu     sync.Mutex
	gaps   []int
	perGap time.Duration
}

func (s *retreatSpy) Delay(attempt int) time.Duration {
	s.mu.Lock()
	s.gaps = append(s.gaps, attempt)
	s.mu.Unlock()
	return s.perGap
}

func (s *retreatSpy) seen() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	parts := make([]string, len(s.gaps))
	for i, g := range s.gaps {
		parts[i] = fmt.Sprint(g)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// refusedAddr 返回一个刚刚释放的本地地址：对其建连必然被拒。
func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}
	return addr
}

// runHeartBeat 驱动一次心跳并在 budget 内等它退出。
// 不退出即判红——这既是判据，也避免整包因为一个不退出的 goroutine 挂死。
func runHeartBeat(t *testing.T, r *Registry, leaseID clientv3.LeaseID, budget time.Duration) time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	t.Cleanup(cancel)

	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.heartBeat(ctx, leaseID, "/microservices/retreat/1", "{}")
	}()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(budget):
		t.Fatalf("heartBeat 未在 %s 内退出（重注册循环或等待没有随 ctx 收口）", budget)
		return budget
	}
}

// TestHeartBeatRetreatsBetweenFailedAttempts 钉「断链重注册真的按退避等待」这件事。
//
// 缺陷形态（2026-11-05 实测）：原先只有「重注册成功但 KeepAlive 建流失败」那一支
// 会等待，而这三支——单次尝试被 3s 上限掐掉、尝试自己返回错误、建流失败——里前两支
// 直接 continue。把曲线配好之后直调 heartBeat 跑满 maxRetry=3，策略被问到的次数是
// 0 次：契约的 registry.etcd.backoff 落进的是一个几乎走不到的分支。
//
// 这里的失败原因是 lease 尚未建立（registerWithKV 立刻返回 errNoLease），不是网络
// 错误——刻意选它：它走的是循环里同一条「失败 → 退避 → 下一次」的码，却不需要
// 真 etcd、也不需要 3s 上限，因此判据与调度无关。真后端那一半由
// [TestHeartBeatRetreatsWithUnreachableBackend] 补。
//
// 期望的次序是 [0 1] 而不是 [0 1 2]：末次尝试之后没有下一次，不该再等。
func TestHeartBeatRetreatsBetweenFailedAttempts(t *testing.T) {
	spy := &retreatSpy{perGap: 20 * time.Millisecond}

	r, err := New(
		WithEndpoints(refusedAddr(t)),
		WithDialTimeout(300*time.Millisecond),
		WithMaxRetry(3),
		WithRetreat(spy, retry.NoJitter),
	)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	elapsed := runHeartBeat(t, r, clientv3.LeaseID(0x1), 15*time.Second)

	if got := spy.seen(); got != "[0 1]" {
		t.Errorf("退避被问到 %s, want [0 1]（三次尝试之间两次等待，末次不再等）；整轮耗时 %s", got, elapsed.Round(time.Millisecond))
	}
	// 两次等待至少 40ms：只在「等待被跳过」时有牙齿，慢机器上恒成立。
	if elapsed < 2*spy.perGap {
		t.Errorf("整轮 %s < 两次等待之和 %s：等待没有真的发生在尝试之间", elapsed, 2*spy.perGap)
	}
}

// TestHeartBeatRetreatsWithUnreachableBackend 是同一条不变量在**真不可达后端**上的形状：
// 每次 Grant 要么被 gRPC 的连接重试拖到 3s 上限、要么自己返回错误，两种都算一次失败。
// 这条不指望区分是哪一支（不同网络栈下形态不同，强行区分会变成环境脚），它只钉
// 「失败之后确实向策略要过等待」——原先的缺陷正好在这一支上完全不走等待。
func TestHeartBeatRetreatsWithUnreachableBackend(t *testing.T) {
	spy := &retreatSpy{perGap: 50 * time.Millisecond}

	r, err := New(
		WithEndpoints(refusedAddr(t)),
		WithDialTimeout(300*time.Millisecond),
		WithMaxRetry(2),
		WithRetreat(spy, retry.NoJitter),
	)
	if err != nil {
		t.Fatalf("new registry: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	elapsed := runHeartBeat(t, r, clientv3.LeaseID(0x1), 30*time.Second)

	if got := spy.seen(); got != "[0]" {
		t.Errorf("不可达后端下退避被问到 %s, want [0]（两次尝试之间一次等待）；整轮耗时 %s", got, elapsed.Round(time.Millisecond))
	}
}
