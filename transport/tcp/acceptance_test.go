package tcp

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"
)

// TestAcceptance_StopReleasesConnectionGoroutines 用户级验收：
//
//	动作：起一个 TCP server → 建 5 条**空闲**客户端连接（连上但不发数据，
//	      模拟"远端挂着不断开"）→ 调 Stop()。
//	可观察结果：Stop 返回后，该 server 持有的会话归零，且进程 goroutine 数
//	      回落到启动前基线附近（每会话 2 个 pump goroutine 已退出）。
//
// 这是 Fix2 的**后果级**验证——前面的单测只断言"会话表被清空"，
// 本测试断言真实可观察的进程状态（goroutine 不累积）。
func TestAcceptance_StopReleasesConnectionGoroutines(t *testing.T) {
	const conns = 5
	const tolerance = 3 // 测试框架/运行时自身可能有些许 goroutine 波动

	// 基线：让运行时稳定下来再取。
	time.Sleep(50 * time.Millisecond)
	runtime.GC()
	baseline := runtime.NumGoroutine()

	srv := NewServer(WithAddress("127.0.0.1:0"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	addr := waitEndpoint(t, srv)

	// 建 N 条空闲连接（不写数据 → 服务端 readPump 阻塞在 conn.Read）。
	clients := make([]net.Conn, 0, conns)
	for i := 0; i < conns; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatalf("dial #%d: %v", i, err)
		}
		clients = append(clients, c)
	}
	defer func() {
		for _, c := range clients {
			_ = c.Close()
		}
	}()

	// 等服务端 accept 全部登记（每连接启动 2 个 pump goroutine）。
	deadline := time.Now().Add(3 * time.Second)
	for srv.SessionCount() < conns {
		if time.Now().After(deadline) {
			t.Fatalf("only %d/%d sessions accepted", srv.SessionCount(), conns)
		}
		time.Sleep(5 * time.Millisecond)
	}
	peak := runtime.NumGoroutine()
	if peak <= baseline {
		t.Fatalf("precondition: goroutine peak (%d) should exceed baseline (%d) after %d conns", peak, baseline, conns)
	}

	// ---- 被验动作：Stop ----
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// ---- 可观察结果 1：会话归零 ----
	if got := srv.SessionCount(); got != 0 {
		t.Fatalf("after Stop: sessions = %d, want 0", got)
	}

	// ---- 可观察结果 2：pump goroutine 已退出（回落近基线）----
	var after int
	deadline = time.Now().Add(3 * time.Second)
	for {
		runtime.GC()
		after = runtime.NumGoroutine()
		if after <= baseline+tolerance {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak after Stop: baseline=%d, peak=%d, after=%d (want <= %d)",
				baseline, peak, after, baseline+tolerance)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("验收通过：baseline=%d peak=%d after Stop=%d（%d 条空闲连接的 pump goroutine 已释放）",
		baseline, peak, after, conns)
}
