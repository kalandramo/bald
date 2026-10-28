// key.go 是类型化资源句柄：Provider 导出自己的 Key，消费侧只依赖
// Key[T] 而不是段路径字符串——把「字符串 + any 断言」从公开范式降为
// 边界实现细节（resource.go 的 lookup）。
package appkit

// Key 是类型化的资源句柄。name 是稳定路径（写入 Plan/日志/序列化边界）；
// T 是节点产物类型。零值 Key 的 name 为空，取不到任何资源。
type Key[T any] struct{ name ResourceKey }

// NewKey 以段路径构造类型化句柄。name 语义与 Resource 的 segment 参数
// 一致：精确子段路径（"database.sql"）、顶层段（"database"）或表外段。
func NewKey[T any](name string) Key[T] { return Key[T]{name: ResourceKey(name)} }

// Name 返回非泛型的稳定身份（Node/Plan 等不可泛型化的结构用）。
func (k Key[T]) Name() ResourceKey { return k.name }

// Resource 取节点产物；未装配、段缺失或类型不符返回零值 + false。
// a 可为 nil（直测 Provider 等无 App 场景）。
func (k Key[T]) Resource(a *App) (T, bool) {
	var zero T
	if a == nil {
		return zero, false
	}
	v, ok := a.lookup(string(k.name))
	if !ok {
		return zero, false
	}
	t, ok := v.(T)
	if !ok {
		return zero, false
	}
	return t, true
}
