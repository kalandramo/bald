// Package config 提供 bald 的配置加载层。
//
// 设计来源与核心思想见 docs/config-center-design.md。
// 这里定义远程配置源抽象 RemoteSource，并内置一个把 Kratos 的
// config.Source 适配成 RemoteSource 的桥接器（FromKratosSource），
// 从而直接复用 kratos contrib 中已实现的 etcd/consul/nacos/apollo 等后端，
// 避免重复造轮子（与 registry.FromKratos 的思路一致）。
package config

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	kratosconfig "github.com/go-kratos/kratos/v3/config"
	"github.com/spf13/viper"
)

// RemoteSource 表示一个远程配置源。
//
// 每个后端（etcd/consul/nacos/apollo...）实现该接口，自行声明字节格式，
// 从而绕开 viper 标准 remote 的「强制 JSON / watch 不可靠 / 无鉴权」缺陷。
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

// injectRemote 将远程字节手动注入 viper。
// 关键技巧：先 SetConfigType 声明格式，再 ReadConfig 解析，
// 因此 etcd/consul 里存 yaml 也能正确解析（绕过 viper 强制 JSON 的限制）。
func injectRemote(v *viper.Viper, data []byte, format string) error {
	v.SetConfigType(format)
	return v.ReadConfig(bytes.NewReader(data))
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
