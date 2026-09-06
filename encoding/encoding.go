// Package encoding 定义 bald 统一编解码抽象：Codec 契约与全局编解码器注册表。
//
// 为所有传输面（HTTP/gRPC/TCP/WebSocket/SSE…）提供可拔插的序列化能力：
// 业务与传输层通过名称取用编解码器，格式切换不改业务代码。
//
// 注册语义与框架原则一致——显式注册，不用 init() + blank import：
// 各格式包（encoding/json、encoding/proto …）导出 New() 构造器，
// 由应用在装配期显式 encoding.MustRegister(jsoncodec.New())。
// 未注册的格式在 GetCodec 查表时返回 nil，由调用方 fail-fast。
package encoding

import (
	"sort"
	"strings"
	"sync"
)

// Codec 定义报文体编解码契约，所有具体编解码器（json/proto/yaml…）必须满足。
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
	Name() string
}

var (
	mu     sync.RWMutex
	codecs = map[string]Codec{}
)

// MustRegister 按 c.Name() 注册编解码器（名称大小写无关，统一小写存储）。
// nil codec、空名称、重复名称均 panic——注册期编程错误 fail-fast。
func MustRegister(c Codec) {
	if c == nil {
		panic("encoding: codec cannot be nil")
	}
	name := strings.ToLower(c.Name())
	if name == "" {
		panic("encoding: codec name cannot be empty")
	}
	mu.Lock()
	defer mu.Unlock()
	if _, dup := codecs[name]; dup {
		panic("encoding: codec already registered: " + name)
	}
	codecs[name] = c
}

// GetCodec 按名称返回已注册编解码器；未注册返回 nil。名称大小写无关。
func GetCodec(name string) Codec {
	if name == "" {
		return nil
	}
	name = strings.ToLower(name)
	mu.RLock()
	defer mu.RUnlock()
	return codecs[name]
}

// Names 返回全部已注册编解码器名称（升序），用于诊断与文档。
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(codecs))
	for n := range codecs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
