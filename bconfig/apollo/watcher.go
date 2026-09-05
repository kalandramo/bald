package apollo

import (
	"context"
	"strings"

	"github.com/apolloconfig/agollo/v4/storage"
)

// valueChangeListener 接收 Apollo 变更事件并推送新文档字节。
type valueChangeListener struct {
	out       chan<- []byte
	ctx       context.Context // 推送阻塞时的退出通道（与 WatchValue 的 ctx 同生命周期）
	namespace string
	conf      *Config // 回读全量文档用（缓存先于 listener 更新，此刻即最新值）
}

func (c *valueChangeListener) onChange(namespace string, changes map[string]*storage.ConfigChange) []byte {
	// 结构化命名空间（yaml/yml/json）：变更以 "content" 键承载原文档。
	if strings.Contains(namespace, ".") && !strings.HasSuffix(namespace, "."+properties) &&
		(format(namespace) == yaml || format(namespace) == yml || format(namespace) == json) {
		if value, ok := changes["content"]; ok {
			if s, ok := value.NewValue.(string); ok {
				return []byte(s)
			}
		}
	}

	// 其余（properties 等）：回读整份命名空间文档（与 Load 语义一致）。
	// 不能只推变更增量：消费方（bootstrap/config Store）把推送当作该层的
	// 新整文档替换层缓存，增量会让命名空间其余键从合并树消失。
	if c.conf == nil {
		return nil
	}
	data, ok := c.conf.fullDocument(namespace)
	if !ok {
		return nil
	}
	return data
}

func (c *valueChangeListener) OnChange(changeEvent *storage.ChangeEvent) {
	if changeEvent.Namespace != c.namespace {
		return
	}
	data := c.onChange(changeEvent.Namespace, changeEvent.Changes)
	if data == nil {
		return
	}
	select {
	case c.out <- data:
	case <-c.ctx.Done():
	}
}

func (c *valueChangeListener) OnNewestChange(_ *storage.FullChangeEvent) {}

// newWatchValueChannel 注册变更监听并返回推送通道；ctx 取消后反注册并关闭通道。
func newWatchValueChannel(ctx context.Context, a *Config, namespace string) (<-chan []byte, error) {
	out := make(chan []byte, 1)
	listener := &valueChangeListener{
		out:       out,
		ctx:       ctx,
		namespace: namespace,
		conf:      a,
	}
	a.client.AddChangeListener(listener)

	// ctx 取消后反注册监听器再关闭通道（修正 go-wind 版注册后立即反注册的 bug）。
	go func() {
		defer close(out)
		defer a.client.RemoveChangeListener(listener)
		<-ctx.Done()
	}()

	return out, nil
}
