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

// runHeartBeat 驱动心跳直到 spy 被询问满 minAsks 次，然后取消 ctx 并断言
// heartBeat 随取消退出。AR-8 之后 heartBeat 在断链场景**不再退出**（无限
// 退避重注册——实例被摘除后必须重建）——退出语义改为「随 ctx/stop 收口」，
// 重试行为用 spy 的询问次数与次序断言。
func runHeartBeat(t *testing.T, r *Registry, leaseID clientv3.LeaseID, spy *retreatSpy, minAsks int) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.heartBeat(ctx, leaseID, "/microservices/retreat/1", "{}")
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Count(spy.seen(), " ") >= minAsks-1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cancel 后 heartBeat 未随 ctx 收口退出")
	}
}

// TestHeartBeatRetreatsBetweenFailedAttempts 钉两条不变量：
//
//  1. 轮内退避：maxRetry=3 的失败周期里策略被问 [0 1]（尝试间两次等待，
//     末次不再等）——2026-11-05 缺陷的回归锁。
//  2. AR-8 无限重注册：重试耗尽后不再静默退出，而是记日志 + 退避后重开
//     一个完整周期（spy 被继续询问，模式 [0 1 2] 重复）——此前耗尽即
//     return，实例被永久摘除而进程不自知。
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

	// 等到 ≥7 次询问 = 至少两个完整周期（周期形态 [0 1 2]：尝试间 [0 1] +
	// 耗尽后的轮间退避 [2]），证明重试耗尽后循环仍在继续。
	runHeartBeat(t, r, clientv3.LeaseID(0x1), spy, 7)

	got := spy.seen()
	if !strings.HasPrefix(got, "[0 1 2 0 1 2") {
		t.Fatalf("退避被问到 %s, want [0 1 2] 重复（轮内 [0 1] + 轮间耗尽退避 [2]）", got)
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

	// AR-8：耗尽后无限重注册——spy 被持续询问（模式 [0 1] 重复），cancel 后退出。
	runHeartBeat(t, r, clientv3.LeaseID(0x1), spy, 4)

	got := spy.seen()
	if !strings.HasPrefix(got, "[0 1 0 1") {
		t.Fatalf("退避被问到 %s, want [0 1] 重复（maxRetry=2 的周期形态）", got)
	}
}
