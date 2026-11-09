package nacos

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nacos-group/nacos-sdk-go/v2/clients/config_client"
	"github.com/nacos-group/nacos-sdk-go/v2/vo"
)

func TestNew_Success(t *testing.T) {
	c, err := New(
		WithServerAddrs("nacos:8848"),
		WithNamespace("public"),
		WithGroup("g"),
		WithDataID("app.yaml"),
		WithUsername("u"),
		WithPassword("p"),
	)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	if c.client != nil {
		t.Error("自建模式应惰性建连")
	}
	if c.opts.namespace != "public" || c.opts.username != "u" || c.opts.password != "p" {
		t.Errorf("opts = %+v", c.opts)
	}
}

func stubConfigClient(t *testing.T, client config_client.IConfigClient, err error) *vo.NacosClientParam {
	t.Helper()
	prev := newConfigClient
	t.Cleanup(func() { newConfigClient = prev })
	var got vo.NacosClientParam
	newConfigClient = func(p vo.NacosClientParam) (config_client.IConfigClient, error) {
		got = p
		return client, err
	}
	return &got
}

func TestInit_SelfBuild(t *testing.T) {
	stub := newStubClient()
	got := stubConfigClient(t, stub, nil)

	c, err := New(
		WithServerAddrs("127.0.0.1:8849", "nacos", "127.0.0.1:bad"),
		WithDataID("app.yaml"),
		WithNamespace("public"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.init(); err != nil {
		t.Fatalf("init() = %v", err)
	}
	if c.client != stub {
		t.Error("init 未保存注入的 client")
	}

	want := []struct {
		host string
		port uint64
	}{
		{"127.0.0.1", 8849},    // host:port
		{"nacos", DefaultPort}, // 无端口取默认
		{"127.0.0.1", 0},       // 端口不可解析时归零
	}
	if len(got.ServerConfigs) != len(want) {
		t.Fatalf("ServerConfigs = %+v", got.ServerConfigs)
	}
	for i, w := range want {
		sc := got.ServerConfigs[i]
		if sc.IpAddr != w.host || sc.Port != w.port {
			t.Errorf("ServerConfigs[%d] = %+v，期望 %s:%d", i, sc, w.host, w.port)
		}
	}
	if got.ClientConfig == nil || got.ClientConfig.NamespaceId != "" {
		t.Errorf("ClientConfig = %+v，期望 public 折成空 NamespaceId", got.ClientConfig)
	}
}

func TestInit_ClientError(t *testing.T) {
	boom := errors.New("build client failed")
	stubConfigClient(t, nil, boom)

	c, err := New(WithServerAddrs("127.0.0.1:1"), WithDataID("app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.init(); !errors.Is(err, boom) {
		t.Fatalf("init() = %v，期望包装建连错误", err)
	}

	ctx := context.Background()
	if _, err := c.Load(ctx, ""); !errors.Is(err, boom) {
		t.Errorf("Load() = %v，期望透传 init 错误", err)
	}
	if _, err := c.WatchValue(ctx, ""); !errors.Is(err, boom) {
		t.Errorf("WatchValue() = %v，期望透传 init 错误", err)
	}
}

func TestWatchValue_ListenError(t *testing.T) {
	boom := errors.New("listen failed")
	src, err := NewWithClient(&watchStub{listenErr: boom}, WithDataID("app.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := src.WatchValue(ctx, ""); !errors.Is(err, boom) {
		t.Fatalf("WatchValue() = %v，期望透传 ListenConfig 错误", err)
	}
}

// watchStub 让 CancelListenConfig 阻塞，使测试能在输出通道仍打开时确定性地
// 验证「消费端不读 + ctx 取消」时 OnChange 的丢弃分支。
type watchStub struct {
	config_client.IConfigClient
	param     vo.ConfigParam
	listenErr error
	entered   chan struct{}
	release   chan struct{}
}

func (s *watchStub) ListenConfig(p vo.ConfigParam) error {
	s.param = p
	return s.listenErr
}

func (s *watchStub) CancelListenConfig(vo.ConfigParam) error {
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-s.release
	return nil
}

func TestWatchValue_DropsWhenConsumerGone(t *testing.T) {
	stub := &watchStub{
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	src, err := NewWithClient(stub, WithDataID("app.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, err := src.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	stub.param.OnChange("", stub.param.Group, "app.yaml", "first") // 填满容量 1 的缓冲
	cancel()
	<-stub.entered // 反注册已进入且输出通道仍打开

	// 缓冲满且 ctx 已取消：这一次推送走 ctx.Done 丢弃分支，不阻塞也不 panic。
	stub.param.OnChange("", stub.param.Group, "app.yaml", "second")
	close(stub.release)

	var got []string
	for data := range ch {
		got = append(got, string(data))
	}
	if len(got) != 1 || got[0] != "first" {
		t.Errorf("派发 = %v，期望仅 first", got)
	}
}
