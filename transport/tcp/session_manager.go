package tcp

import (
	"sync"
)

// SessionObserver defines the interface for observing session events such as addition and removal of sessions.
type SessionObserver interface {
	// OnSessionAdded is called when a new session is added to the SessionManager. It receives the newly added session as an argument.
	OnSessionAdded(session *Session)

	// OnSessionRemoved is called when a session is removed from the SessionManager. It receives the removed session as an argument.
	OnSessionRemoved(session *Session)
}

type SessionManager struct {
	sessions sync.Map

	// observerMu 保护 observer：RegisterObserver 可能在运行期被调用，
	// 与并发的 addSession/removeSession 读取构成数据竞争（-race 可检出）。
	observerMu sync.RWMutex
	observer   SessionObserver
}

func NewSessionManager(observer SessionObserver) *SessionManager {
	return &SessionManager{
		sessions: sync.Map{},
		observer: observer,
	}
}

func (sm *SessionManager) RegisterObserver(observer SessionObserver) {
	sm.observerMu.Lock()
	defer sm.observerMu.Unlock()
	sm.observer = observer
}

// getObserver 在锁保护下读取 observer。
func (sm *SessionManager) getObserver() SessionObserver {
	sm.observerMu.RLock()
	defer sm.observerMu.RUnlock()
	return sm.observer
}

func (sm *SessionManager) Clean() {
	sm.sessions.Clear()
}

// count returns the number of active sessions.
func (sm *SessionManager) count() int {
	count := 0
	sm.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// addSession adds a new session to the manager and notifies the observer.
// 幂等：同一 session 重复 add 只触发一次 OnSessionAdded（用 LoadOrStore 判定）。
func (sm *SessionManager) addSession(session *Session) {
	if session == nil {
		return
	}

	if _, loaded := sm.sessions.LoadOrStore(session.SessionID(), session); loaded {
		return // 已存在，不重复通知
	}

	if observer := sm.getObserver(); observer != nil {
		observer.OnSessionAdded(session)
	}
}

// removeSession removes a session from the manager and notifies the observer.
// 幂等：仅当确实删除成功时才通知（用 LoadAndDelete 判定）。
func (sm *SessionManager) removeSession(session *Session) {
	if session == nil {
		return
	}

	if _, loaded := sm.sessions.LoadAndDelete(session.SessionID()); !loaded {
		return // 本就不存在，不通知
	}

	if observer := sm.getObserver(); observer != nil {
		observer.OnSessionRemoved(session)
	}
}

// getSession retrieves a session by its session ID.
func (sm *SessionManager) getSession(sessionId SessionID) *Session {
	if val, ok := sm.sessions.Load(sessionId); ok {
		return val.(*Session)
	}
	return nil
}

// rangeSessions 遍历所有会话并对其应用 fn。
//
// fn 返回 true 表示继续遍历、返回 false 表示停止——与 websocket.RangeSessions
// 同一约定。此前实现忽略返回值（注释却声称 true 会停止），两处语义相反；
// 现已统一，调用方（BroadcastRawData）须传 return true 以遍历全部会话。
//
// 先快照再遍历：避免在 fn 内改动 sessions 时影响 sync.Map.Range 的遍历。
func (sm *SessionManager) rangeSessions(fn func(SessionID, *Session) bool) {
	var sessions []*Session
	sm.sessions.Range(func(_, val any) bool {
		sessions = append(sessions, val.(*Session))
		return true
	})

	for _, session := range sessions {
		if !fn(session.SessionID(), session) {
			break
		}
	}
}
