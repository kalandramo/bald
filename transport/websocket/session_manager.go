package websocket

import "sync"

// SessionObserver 观察会话的添加和移除事件。
type SessionObserver interface {
	// OnSessionAdded 在新会话被添加时调用。
	OnSessionAdded(session *Session)
	// OnSessionRemoved 在会话被移除时调用。
	OnSessionRemoved(session *Session)
}

// SessionManager 管理所有活跃的 WebSocket 会话。
type SessionManager struct {
	sessions sync.Map
	observer SessionObserver
}

// NewSessionManager 创建一个 SessionManager 实例。
func NewSessionManager(observer SessionObserver) *SessionManager {
	return &SessionManager{
		sessions: sync.Map{},
		observer: observer,
	}
}

// RegisterObserver 注册会话观察者。
func (sm *SessionManager) RegisterObserver(observer SessionObserver) {
	sm.observer = observer
}

// Clean 关闭并清空所有会话。
func (sm *SessionManager) Clean() {
	sm.sessions.Range(func(_, val any) bool {
		if session, ok := val.(*Session); ok {
			session.Close()
		}
		return true
	})
	sm.sessions.Clear()
}

// Count 返回活跃会话数量。
func (sm *SessionManager) Count() int {
	count := 0
	sm.sessions.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// GetSession 按 ID 获取会话。
func (sm *SessionManager) GetSession(sessionId SessionID) *Session {
	if val, ok := sm.sessions.Load(sessionId); ok {
		return val.(*Session)
	}
	return nil
}

// RangeSessions 遍历所有会话。
func (sm *SessionManager) RangeSessions(fn func(SessionID, *Session) bool) {
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

// AddSession 添加新会话并通知观察者。
func (sm *SessionManager) AddSession(session *Session) {
	if session == nil {
		return
	}

	sm.sessions.Store(session.SessionID(), session)

	if sm.observer != nil {
		sm.observer.OnSessionAdded(session)
	}
}

// RemoveSession 移除会话并通知观察者。
func (sm *SessionManager) RemoveSession(session *Session) {
	if session == nil {
		return
	}

	sm.sessions.Delete(session.SessionID())

	if sm.observer != nil {
		sm.observer.OnSessionRemoved(session)
	}
}
