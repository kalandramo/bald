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

	// observerMu 保护 observer：RegisterObserver 可能在运行期被调用，
	// 与并发的 AddSession/RemoveSession 读取构成数据竞争（-race 可检出）。
	observerMu sync.RWMutex
	observer   SessionObserver
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
// 幂等：同一 session 重复添加只触发一次 OnSessionAdded。
func (sm *SessionManager) AddSession(session *Session) {
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

// RemoveSession 移除会话并通知观察者。
// 幂等：仅当确实删除成功时才通知。
func (sm *SessionManager) RemoveSession(session *Session) {
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
