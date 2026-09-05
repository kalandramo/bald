package nacos

import (
	"context"
	"testing"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

// TestNew_ParameterValidation 自建模式参数校验（不触网）。
func TestNew_ParameterValidation(t *testing.T) {
	if _, err := New(); err == nil {
		t.Error("New() without server addrs should fail")
	}
	if _, err := New(WithServerAddrs("127.0.0.1:8848")); err == nil {
		t.Error("New() without data id should fail")
	}
}

// TestNewWithClient_ParameterValidation 注入模式参数校验。
func TestNewWithClient_ParameterValidation(t *testing.T) {
	if _, err := NewWithClient(nil, WithDataID("cfg")); err == nil {
		t.Error("NewWithClient(nil) should fail")
	}
	if _, err := NewWithClient(&stubClient{}); err == nil {
		t.Error("NewWithClient without data id should fail")
	}
}

// TestResolveDataID key 覆盖默认 dataID。
func TestResolveDataID(t *testing.T) {
	c := &Config{opts: options{dataID: "default.yaml"}}
	if got := c.resolveDataID(""); got != "default.yaml" {
		t.Errorf("resolveDataID(\"\") = %q", got)
	}
	if got := c.resolveDataID("explicit.yaml"); got != "explicit.yaml" {
		t.Errorf("resolveDataID(explicit) = %q", got)
	}
}

// TestLoad_WithStub 桩注入：Load 拉取与默认 group/dataID 透传。
func TestLoad_WithStub(t *testing.T) {
	stub := &stubClient{content: "log:\n  level: debug"}
	src, err := NewWithClient(stub, WithGroup("g1"), WithDataID("app.yaml"))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(data) != "log:\n  level: debug" {
		t.Errorf("Load() = %q", data)
	}
	if stub.lastParam.DataId != "app.yaml" || stub.lastParam.Group != "g1" {
		t.Errorf("GetConfig param = %+v", stub.lastParam)
	}
}

// TestWatchValue_WithStub 桩注入：ListenConfig 注册 + ctx 取消反注册。
func TestWatchValue_WithStub(t *testing.T) {
	// 必须经 newStubClient 初始化 listen/cancelled 通道：
	// 零值桩的 nil channel 会让 CancelListenConfig 的 select 发送永不就绪，
	// 反注册信号发不出来（曾导致本测试超时误报）。
	stub := newStubClient()
	src, err := NewWithClient(stub, WithDataID("app.yaml"))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatalf("WatchValue: %v", err)
	}

	// ListenConfig 已同步注册，模拟服务端推送。
	stub.push([]byte("changed"))
	select {
	case data := <-ch:
		if string(data) != "changed" {
			t.Errorf("watch got %q", data)
		}
	case <-time.After(time.Second):
		t.Fatal("no push received")
	}

	cancel()
	select {
	case <-stub.cancelled:
	case <-time.After(time.Second):
		t.Fatal("CancelListenConfig not called after ctx cancel")
	}
}

// stubClient 接口嵌入桩：仅覆写 GetConfig/ListenConfig/CancelListenConfig。
type stubClient struct {
	config_client.IConfigClient
	content   string
	listen    chan struct{}
	cancelled chan struct{}
	lastParam vo.ConfigParam
}

func newStubClient() *stubClient {
	return &stubClient{listen: make(chan struct{}, 1), cancelled: make(chan struct{}, 1)}
}

func (s *stubClient) GetConfig(param vo.ConfigParam) (string, error) {
	s.lastParam = param
	return s.content, nil
}

func (s *stubClient) ListenConfig(param vo.ConfigParam) error {
	s.lastParam = param
	select {
	case s.listen <- struct{}{}:
	default:
	}
	return nil
}

func (s *stubClient) CancelListenConfig(_ vo.ConfigParam) error {
	select {
	case s.cancelled <- struct{}{}:
	default:
	}
	return nil
}

func (s *stubClient) push(data []byte) {
	if s.lastParam.OnChange != nil {
		s.lastParam.OnChange("ns", s.lastParam.Group, s.lastParam.DataId, string(data))
	}
}
