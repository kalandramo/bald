// Package bconfig 定义 Bald 框架的配置源抽象层
//
// 本包提供可组合的接口，供各类具体配置提供者（文件、环境变量、etcd、consul 等）实现：
//   - [Reader]       — 一次性键值读取。
//   - [Closer]       — 面向文件/连接类提供者的资源生命周期管理。
//   - [ReadCloser]   — 组合 Reader 与 Closer 接口。
//   - [Watcher]      — 信号模式的配置变更通知。
//   - [ReadWatcher]  — 组合 Reader 与 Watcher 接口。
//   - [ValueWatcher] — 携带配置值的推送模式变更通知。
//   - [Decoder]      — 原始字节到类型化数值的转换。
package bconfig

import "context"

// Reader 接口提供根据键一次性加载配置数据的能力。
// 该接口刻意不包含生命周期相关方法；持有资源（文件、网络连接）的配置提供者应当改为实现 [ReadCloser] 接口。
type Reader interface {
	// Load 获取指定键对应的原始配置字节数据。
	Load(ctx context.Context, key string) (data []byte, err error)
}

// Closer 用于释放配置提供者所持有的全部资源。该接口对标 [io.Closer]，是构成 [ReadCloser] 的基础组件。
type Closer interface {
	Close() error
}

// ReadCloser 组合了 [Reader] 和 [Closer] 接口，适用于持有资源（文件、网络连接等）、
// 需要显式释放资源的配置提供者。
type ReadCloser interface {
	Reader
	Closer
}

// Watcher 提供信号模式的配置变更通知能力：事件只携带「配置已变更」这一事实，不携带新值，
// 调用方需自行 [Reader.Load] 回读。
//
// 能力边界（重要）：本包的组合器 [FallbackReader] 只合并 [ValueWatcher]，不识别本接口——
// 仅实现 Watcher 的配置源，其变更不会触发组合器的通知（见 fallback.go 的 [FallbackReader.WatchValue]）。
// 因此：
//   - 能直接拿到新值的后端，请实现 [ValueWatcher]；
//   - 只能拿到信号的后端，请在 provider 内部收到信号后 Load 回读，再以 [ValueWatcher] 的形式暴露给框架。
type Watcher interface {
	// Watch 方法返回一个通道，每当键对应的配置值发生变化时，该通道会收到一个信号。
	// 当监听器停止工作或者上下文被取消时，通道会被关闭。
	Watch(ctx context.Context, key string) (<-chan struct{}, error)
}

// ReadWatcher 组合 [Reader] 和 [Watcher] 接口，用于同时支持一次性读取与信号模式更新的配置提供者。
//
// 注意：组合层不识别本组合，它仅作为类型便利存在；监听能力仍以 [ValueWatcher] 为准（见 [Watcher] 注释）。
type ReadWatcher interface {
	Reader
	Watcher
}

// ValueWatcher 提供推送模式的配置变更通知。
// [Watcher] 仅发送“值已变更”的信号（需要后续调用 [Reader.Load] 获取新值），与之不同，
// ValueWatcher 会直接通过通道传递新的配置值。
// 该接口为可选接口，且是组合层（[FallbackReader.WatchValue]）唯一识别的监听契约：
// 能高效传递变更后数值的提供者实现本接口即可；
// 只能给出变更信号的后端请实现 [Watcher]，并自行在 provider 内部 Load 回读后转推值。
type ValueWatcher interface {
	// WatchValue 返回一个通道，每当键对应的数据发生变更时，通道会收到新的原始字节值。
	// 当监听器停止或者上下文被取消时，该通道会被关闭。
	WatchValue(ctx context.Context, key string) (<-chan []byte, error)
}

// Decoder 将配置原始字节数据转换为类型化值。
// 该接口对反序列化流程做标准化，调用方无需直接调用 json.Unmarshal / yaml.Unmarshal；
// 只需注入 [Decoder]，由框架处理具体数据格式。
//
// 典型实现包括 JSONDecoder、YAMLDecoder、TOMLDecoder。
// [Decoder] 与 [Reader] 相互独立，由调用方按需组合使用：
//
//	data, _ := reader.Load(ctx, "config.json")
//	var cfg MyConfig
//	_ = jsonDecoder.Decode(data, &cfg)
type Decoder interface {
	// Decode 将输入数据反序列化到 out 所指向的变量中。
	Decode(data []byte, out any) error
}
