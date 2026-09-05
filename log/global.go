package log

import "sync"

var (
	mu     sync.RWMutex
	global Logger = nopLogger{}
)

// SetLogger 注入全局日志后端。
// 传入 nil 会回退到静默的 nop 默认，且调用并发安全。
func SetLogger(l Logger) {
	mu.Lock()
	defer mu.Unlock()
	if l == nil {
		global = nopLogger{}
		return
	}
	global = l
}

// GetLogger 返回当前全局日志后端。
// 子模块与框架核心统一通过它获取共享实例，不依赖任何具体日志库。
func GetLogger() Logger {
	mu.RLock()
	defer mu.RUnlock()
	return global
}
