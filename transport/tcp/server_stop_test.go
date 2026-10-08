package tcp

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fix2：Server.Stop 必须关闭已建立会话
//
// 缺陷：Stop（server.go）此前只关 listener，不遍历关闭已建会话——每会话持有的
// 2 个 pump goroutine + net.Conn 在「远端保持连接但空闲」时不会被释放
// （readPump 阻塞在 conn.Read，对端不断开就一直挂着）。
// 触发面：运行期热插拔/重建 server（WithExtraServerFunc）；整机退出时由进程终止兜底。
//
// Session.Close 已 closeOnce 幂等，故遍历关闭是安全的。
// ---------------------------------------------------------------------------

// TestServer_StopClosesEstablishedSessions Stop 后不得残留会话。
func TestServer_StopClosesEstablishedSessions(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	// Start 阻塞到 ctx 取消——必须放 goroutine（对齐 TestServerStartStop 写法）。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()

	// 等待监听就绪（Endpoint 解析出真实端口）。
	addr := waitEndpoint(t, srv)

	// 建立一条空闲会话（客户端连上但不发数据）。
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()

	// 等待服务端 accept 完成并登记会话。
	deadline := time.Now().Add(2 * time.Second)
	for srv.SessionCount() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("server did not accept the connection in time")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Stop 必须清空会话（否则空闲连接的 pump goroutine 泄漏）。
	if got := srv.SessionCount(); got != 0 {
		t.Fatalf("sessions after Stop = %d, want 0（空闲会话的 pump goroutine 会泄漏）", got)
	}
	cancel() // 结束 Start goroutine
}

// TestServer_StopIdempotent Close 幂等：重复 Stop 不 panic、不报错。
func TestServer_StopIdempotent(t *testing.T) {
	srv := NewServer(WithAddress("127.0.0.1:0"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Start(ctx) }()
	_ = waitEndpoint(t, srv)

	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("first Stop: %v", err)
	}
	if err := srv.Stop(context.Background()); err != nil {
		t.Fatalf("second Stop must be no-op, got: %v", err)
	}
	cancel()
}

// waitEndpoint 轮询等待 server 完成监听并返回**可拨号**地址（剥离 scheme——
// Endpoint 返回形如 tcp://127.0.0.1:port，net.Dial 需要裸 host:port）。
func waitEndpoint(t *testing.T, srv *Server) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if ep := srv.Endpoint(); ep != "" && !strings.HasSuffix(ep, ":0") {
			return strings.TrimPrefix(ep, "tcp://")
		}
		if time.Now().After(deadline) {
			t.Fatalf("server not ready, Endpoint=%q", srv.Endpoint())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestSessionManager_CloseAllClosesSessions 直接钉住原语：CloseAll 应逐个 Close
// 并清空管理器。
func TestSessionManager_CloseAllClosesSessions(t *testing.T) {
	sm := NewSessionManager(nil)
	conns := make([]*fakeConn, 3)
	for i, s := range []*Session{NewSession(newFakeConn(), nil), NewSession(newFakeConn(), nil), NewSession(newFakeConn(), nil)} {
		conns[i] = s.conn.(*fakeConn)
		sm.addSession(s)
	}
	if got := sm.count(); got != 3 {
		t.Fatalf("precondition: sessions = %d, want 3", got)
	}

	sm.CloseAll()

	if got := sm.count(); got != 0 {
		t.Fatalf("sessions after CloseAll = %d, want 0", got)
	}
	for i, c := range conns {
		select {
		case <-c.closed:
			// ok：底层连接已被关闭
		default:
			t.Fatalf("session %d conn not closed by CloseAll", i)
		}
	}
}
