// Package appkit 的插件注册表（对照 go-lulu 的 builder 注册表范式）。
//
// 设计意图：核心只定义最小接口与契约（P5 零后端耦合），所有引擎/传输/中间件
// 具体实现作为独立子模块，经 init() 自注册进对应 Registry，业务侧
// `import _ "github.com/kalandramo/bald-store-gorm/register"` 即插即用，无需
// 在 main 里手写接线。
//
// 注册表是并发安全的通用泛型容器；框架预置三类实例：
//   - ServerRegistry：传输层插件（grpc / http / gateway 变体）
//   - MiddlewareRegistry：gin/grpc 中间件工厂（any，调用方按类型断言）
//   - ProviderRegistry：存储后端工厂（any，因 DBProvider[T] 泛型无法静态存放）
package appkit

import (
	"fmt"
	"sort"
	"sync"

	"github.com/kalandramo/bald/pkg/server"
)

// Registry 是并发安全的按名注册表。T 为被注册的实现类型。
type Registry[T any] struct {
	mu    sync.RWMutex
	items map[string]T
}

// NewRegistry 构造空注册表。
func NewRegistry[T any]() *Registry[T] {
	return &Registry[T]{items: make(map[string]T)}
}

// Register 注册一个实现；重名返回 error（不覆盖，避免静默踩踏）。
func (r *Registry[T]) Register(name string, impl T) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.items[name]; ok {
		return fmt.Errorf("appkit: %q already registered", name)
	}
	r.items[name] = impl
	return nil
}

// MustRegister 同 Register，重名直接 panic（适合 init() 自注册场景）。
func (r *Registry[T]) MustRegister(name string, impl T) {
	if err := r.Register(name, impl); err != nil {
		panic(err)
	}
}

// Get 按名取实现，未命中返回 (零值, false)。
func (r *Registry[T]) Get(name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.items[name]
	return v, ok
}

// List 返回所有已注册名字（升序，便于稳定输出/测试）。
func (r *Registry[T]) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.items))
	for k := range r.items {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// 三类预置注册点（插件协议对齐 go-lulu 的 RegisterXxxBuilder）：
var (
	// ServerRegistry 存放传输层插件（实现 server.Server 接口）。
	ServerRegistry = NewRegistry[server.Server]()

	// MiddlewareRegistry 存放 gin/grpc 中间件工厂；gin 与 grpc 中间件签名不同，
	// 故以 any 存放，调用方按实际类型断言后装配。
	MiddlewareRegistry = NewRegistry[any]()

	// ProviderRegistry 存放存储后端工厂。因 store.DBProvider[T] 是泛型、无法
	// 静态作为注册表值类型，故以 any 存放，业务获取后断言回具体 store.DBProvider[T]。
	ProviderRegistry = NewRegistry[any]()
)

// RegisterStoreProvider 是 ProviderRegistry 的便捷封装，供桥接子模块的
// register 包 init() 调用，例如：
//
//	func init() { appkit.RegisterStoreProvider("gorm", gormFactory) }
//
// factory 通常形如 func() (store.DBProvider[T], error)，调用方 Get 后断言。
func RegisterStoreProvider(name string, factory any) {
	ProviderRegistry.MustRegister(name, factory)
}
