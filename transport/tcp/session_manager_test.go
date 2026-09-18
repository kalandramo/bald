package tcp

import (
	"net"
	"sync"
	"testing"
	"time"
)

// fakeConn 是满足 net.Conn 的最小实现，仅用于构造 Session。
type fakeConn struct{ closed chan struct{} }

func newFakeConn() *fakeConn { return &fakeConn{closed: make(chan struct{})} }

func (c *fakeConn) Read([]byte) (int, error)         { <-c.closed; return 0, net.ErrClosed }
func (c *fakeConn) Write(b []byte) (int, error)      { return len(b), nil }
func (c *fakeConn) Close() error                     { close(c.closed); return nil }
func (c *fakeConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (c *fakeConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (c *fakeConn) SetDeadline(time.Time) error      { return nil }
func (c *fakeConn) SetReadDeadline(time.Time) error  { return nil }
func (c *fakeConn) SetWriteDeadline(time.Time) error { return nil }

type recordObserver struct {
	mu    sync.Mutex
	added int
}

func (o *recordObserver) OnSessionAdded(*Session) {
	o.mu.Lock()
	o.added++
	o.mu.Unlock()
}
func (o *recordObserver) OnSessionRemoved(*Session) {}

// 回归：rangeSessions 必须尊重 fn 的返回值——返回 true 继续、false 停止。
//
// 此前实现完全忽略返回值（注释却声称 "returns true, the iteration is stopped"），
// 与 websocket.RangeSessions（尊重返回值）语义相反；调用方 BroadcastRawData
// 传 return false 期望「继续」，一旦实现改为尊重返回值就会只广播第一个会话。
// 本测试钉住统一后的约定：return true = 继续。
func TestRangeSessions_RespectsReturnValue(t *testing.T) {
	sm := NewSessionManager(nil)
	for i := 0; i < 3; i++ {
		sm.addSession(NewSession(newFakeConn(), nil))
	}

	t.Run("return true continues through all sessions", func(t *testing.T) {
		var visited int
		sm.rangeSessions(func(SessionID, *Session) bool {
			visited++
			return true
		})
		if visited != 3 {
			t.Errorf("visited = %d, want 3（return true 应遍历全部）", visited)
		}
	})

	t.Run("return false stops iteration", func(t *testing.T) {
		var visited int
		sm.rangeSessions(func(SessionID, *Session) bool {
			visited++
			return false
		})
		if visited != 1 {
			t.Errorf("visited = %d, want 1（return false 应停止）", visited)
		}
	})
}

// 回归：observer 的注册与读取必须并发安全（此前无任何同步）。
// 用 -race 运行本测试可检出竞争。
func TestSessionManager_ObserverConcurrentSafety(t *testing.T) {
	sm := NewSessionManager(nil)

	var wg sync.WaitGroup
	// 并发注册 observer
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sm.RegisterObserver(&recordObserver{})
		}()
	}
	// 并发增删 session（读 observer）
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := NewSession(newFakeConn(), nil)
			sm.addSession(s)
			sm.removeSession(s)
		}()
	}
	wg.Wait()
}

// 回归：同一 session 重复 add 时 observer 只应收到一次通知（幂等）。
// 此前用裸 Store，重复 add 会重复触发 OnSessionAdded。
func TestSessionManager_AddIsIdempotent(t *testing.T) {
	obs := &recordObserver{}
	sm := NewSessionManager(obs)
	s := NewSession(newFakeConn(), nil)

	sm.addSession(s)
	sm.addSession(s)

	obs.mu.Lock()
	got := obs.added
	obs.mu.Unlock()
	if got != 1 {
		t.Errorf("OnSessionAdded called %d times, want 1（重复 add 应幂等）", got)
	}
}
