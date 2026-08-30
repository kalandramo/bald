package audit

import "sync"

var (
	mu     sync.RWMutex
	global Auditor = nopAuditor{}
)

// SetAuditor 注入全局审计后端。传入 nil 回退到静默 nop。
func SetAuditor(a Auditor) {
	mu.Lock()
	defer mu.Unlock()
	if a == nil {
		global = nopAuditor{}
		return
	}
	global = a
}

// GetAuditor 返回当前全局审计后端。
func GetAuditor() Auditor {
	mu.RLock()
	defer mu.RUnlock()
	return global
}
