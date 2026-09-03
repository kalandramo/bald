package bconfig

import (
	"context"
	"testing"
)

// --- 编译期接口断言 ---

type mockReader struct{}

func (mockReader) Load(context.Context, string) ([]byte, error) { return nil, nil }

type mockCloser struct{}

func (mockCloser) Close() error { return nil }

type mockReadCloser struct{}

func (mockReadCloser) Load(context.Context, string) ([]byte, error) { return nil, nil }
func (mockReadCloser) Close() error                                 { return nil }

type mockWatcher struct{}

func (mockWatcher) Watch(context.Context, string) (<-chan struct{}, error) {
	return nil, nil
}

type mockReadWatcher struct{}

func (mockReadWatcher) Load(context.Context, string) ([]byte, error) { return nil, nil }
func (mockReadWatcher) Watch(context.Context, string) (<-chan struct{}, error) {
	return nil, nil
}

type mockValueWatcher struct{}

func (mockValueWatcher) WatchValue(context.Context, string) (<-chan []byte, error) {
	return nil, nil
}

type mockDecoder struct{}

func (mockDecoder) Decode([]byte, any) error { return nil }

var (
	_ Reader       = mockReader{}
	_ Closer       = mockCloser{}
	_ ReadCloser   = mockReadCloser{}
	_ Watcher      = mockWatcher{}
	_ ReadWatcher  = mockReadWatcher{}
	_ ValueWatcher = mockValueWatcher{}
	_ Decoder      = mockDecoder{}
)

// --- 可组合性断言 ---

func TestInterfaces_Composable(t *testing.T) {
	// 如果一个配置提供者同时实现 ReadCloser 和 Watcher，
	// 通过接口嵌入就可以自动满足 ReadWatcher；但反过来并不做强制要求。
	var rc ReadCloser = mockReadCloser{}
	_ = rc

	var w Watcher = mockWatcher{}
	_ = w

	// ValueWatcher 是独立接口：配置提供者可以仅实现该接口，而不去实现 Watcher。
	var vw ValueWatcher = mockValueWatcher{}
	_ = vw

	// Decoder 独立于 Reader、Watcher 接口。
	var d Decoder = mockDecoder{}
	_ = d
}
