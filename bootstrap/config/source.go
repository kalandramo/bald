// Package config 提供 bald 的配置加载层。
//
// 设计来源与核心思想见 docs/config-center-design.md。
// 这里定义远程配置源抽象 RemoteSource，并内置一个把 Kratos 的
// config.Source 适配成 RemoteSource 的桥接器（FromKratosSource），
// 从而直接复用 kratos contrib 中已实现的 etcd/consul/nacos/apollo 等后端，
// 避免重复造轮子（与 registry.FromKratos 的思路一致）。
package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	kratosconfig "github.com/go-kratos/kratos/v3/config"
)

// RemoteSource 表示一个远程配置源。
//
// 每个后端（etcd/consul/nacos/apollo...）实现该接口，自行声明字节格式，
// 文档由 Store 内 decodeDocument 直接解析（yaml/json 均可），无需依赖
// 任何通用 remote 实现的「强制 JSON / watch 不可靠」等约束。
//
// 鉴权/TLS 等凭据由各后端在构造时自行持有（RemoteSource 接口本身不携带），
// 这样既能满足复杂环境的鉴权需求，又不污染抽象。
type RemoteSource interface {
	// Read 拉取一次远程配置，返回原始字节与格式（"json"/"yaml"/"toml"...）。
	Read(ctx context.Context) (data []byte, format string, err error)

	// Watch 监听远程变更，每次变更通过 onChange 推送最新字节与格式。
	// ctx 取消即停止监听（实现方应在内部退出监听 goroutine）。
	Watch(ctx context.Context, onChange func(data []byte, format string)) error
}

// newRemoteLayer 把 RemoteSource（kratos 桥便捷入口）适配为最低优先级的
// 命名层：实现 Reader（整文档 Load）+ bconfig.ValueWatcher（变更推送）+
// formatAware（动态格式声明），使远程桥与契约源层共用同一层机制。
type remoteLayer struct {
	mu     sync.Mutex
	src    RemoteSource
	format string // 最近一次 Read/Watch 返回的格式
}

func newRemoteLayer(src RemoteSource) *remoteLayer {
	return &remoteLayer{src: src}
}

// Load implements [bconfig.Reader]：拉取一次远程文档并记录其声明格式。
func (l *remoteLayer) Load(ctx context.Context, _ string) ([]byte, error) {
	data, format, err := l.src.Read(ctx)
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	l.format = format
	l.mu.Unlock()
	return data, nil
}

// WatchValue implements [bconfig.ValueWatcher]：把 RemoteSource.Watch 回调
// 转为字节通道；ctx 取消后转发 goroutine 自行退出（RemoteSource 契约）。
func (l *remoteLayer) WatchValue(ctx context.Context, _ string) (<-chan []byte, error) {
	out := make(chan []byte, 1)
	if err := l.src.Watch(ctx, func(data []byte, format string) {
		l.mu.Lock()
		l.format = format
		l.mu.Unlock()
		select {
		case out <- data:
		case <-ctx.Done():
		}
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// currentFormat implements [formatAware]：返回最近一次声明的文档格式。
func (l *remoteLayer) currentFormat() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.format
}

// FromKratosSource 把 Kratos 的 config.Source 适配为 bald 的 RemoteSource。
//
// 用法：
//
//	import etcdconfig "github.com/go-kratos/kratos/v3/contrib/config/etcd/v3"
//	src := config.FromKratosSource(etcdconfig.New(client, etcdconfig.WithPath("/config/demo/prod.yaml")))
//	appkit.RemoteConfig(src)
//
// 注意：用户需自行引入具体的 contrib 后端模块（如 .../contrib/config/etcd/v3），
// bald 仅依赖 kratos 核心库的 config 接口，不强制绑定任何后端 SDK。
func FromKratosSource(src kratosconfig.Source) RemoteSource {
	return &kratosSourceAdapter{src: src}
}

// kratosSourceAdapter 适配 kratos config.Source → RemoteSource。
type kratosSourceAdapter struct {
	src kratosconfig.Source
}

func (a *kratosSourceAdapter) Read(ctx context.Context) ([]byte, string, error) {
	kvs, err := a.src.Load()
	if err != nil {
		return nil, "", fmt.Errorf("config: kratos source Load: %w", err)
	}
	if len(kvs) == 0 {
		return nil, "", fmt.Errorf("config: kratos source returned no data")
	}
	format := kvs[0].Format
	if len(kvs) == 1 {
		return kvs[0].Value, format, nil
	}
	// 多 KV（如 file source 按目录返回多个文件）：合并为单一文档，避免静默丢配置。
	var sb strings.Builder
	for _, kv := range kvs {
		sb.WriteString(fmt.Sprintf("# key=%s\n", kv.Key))
		sb.Write(kv.Value)
		sb.WriteString("\n")
	}
	return []byte(sb.String()), format, nil
}

func (a *kratosSourceAdapter) Watch(ctx context.Context, onChange func([]byte, string)) error {
	w, err := a.src.Watch()
	if err != nil {
		return fmt.Errorf("config: kratos source Watch: %w", err)
	}
	go func() {
		defer w.Stop() // ctx 取消后退出循环即停止 watcher，避免独立 goroutine 泄漏
		for {
			kvs, err := w.Next()
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				// 监听出错（如连接临时中断）：退避后继续，对齐 Kratos 主循环行为。
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
					continue
				}
			}
			for _, kv := range kvs {
				onChange(kv.Value, kv.Format)
			}
		}
	}()
	return nil
}
