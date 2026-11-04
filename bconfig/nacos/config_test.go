package nacos

import (
	"context"
	"errors"
	"strings"
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

// quickLoadRetry 把重试间隔压到毫秒级，让「重试了几次」这类判据不必真的等 250ms。
// 动的是包内变量，不为测试新增公开旋钮。
func quickLoadRetry(t *testing.T) {
	t.Helper()
	restore := loadRetryInterval
	loadRetryInterval = time.Millisecond
	t.Cleanup(func() { loadRetryInterval = restore })
}

// allErrors 造 n 次都返回同一个错误的序列（模拟持续判限/持续不可达）。
func allErrors(err error, n int) []error {
	out := make([]error, n)
	for i := range out {
		out[i] = err
	}
	return out
}

// TestLoad_RetriesUntilTheBucketRefills 瞬态判限必须被重试吃掉。
//
// 判限来自 nacos-sdk-go 自己的进程内令牌桶（config_client/limiter.go:42-56，按
// dataId@@group@@namespace 分键、5 次/秒）：同一个 dataID 在一个进程里连续起多个
// 应用时，多出来的那次查询**不发包**就被拒，隔一个回填周期就该成功。所以「首错
// 后成 ⇒ Load 成功」是产品语义，不是运气。calls==3 钉住它真的重试了而不是碰巧。
func TestLoad_RetriesUntilTheBucketRefills(t *testing.T) {
	quickLoadRetry(t)
	limited := errors.New("read config from both server and cache fail, err=config file not exist")
	stub := &stubClient{
		content: "log:\n  level: debug",
		getErrs: []error{limited, limited},
	}
	src, err := NewWithClient(stub, WithDataID("app.yaml"))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	data, err := src.Load(context.Background(), "")
	if err != nil {
		t.Fatalf("Load: %v（两次判限后应回填成功）", err)
	}
	if string(data) != "log:\n  level: debug" {
		t.Errorf("Load() = %q", data)
	}
	if stub.calls != 3 {
		t.Errorf("GetConfig 调了 %d 次，want 3（两次判限 + 回填后一次成功）", stub.calls)
	}
}

// TestLoad_NoRetryWhenFirstQuerySucceeds 成功即止：不把上界烧完，也不给健康的
// 配置源加无谓的第二跳。
func TestLoad_NoRetryWhenFirstQuerySucceeds(t *testing.T) {
	quickLoadRetry(t)
	stub := &stubClient{content: "a: 1"}
	src, err := NewWithClient(stub, WithDataID("app.yaml"))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}
	if _, err := src.Load(context.Background(), ""); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stub.calls != 1 {
		t.Errorf("GetConfig 调了 %d 次，want 1", stub.calls)
	}
}

// TestLoad_GivesUpAtTheBoundAndNamesTheCandidateCause 上界与错误文案。
//
// 每次查询都错 ⇒ 恰好 loadRetryAttempts 次、不再无限重试。文案必须同时带住两头：
//   - 原错误（%w 包装，errors.Is 判得出，消费方还能按 SDK 的串匹配）；
//   - 「可能是本地桶」这句候选真因 —— SDK 在该 dataID 没有本地快照时，会把拒绝
//     原因换成缓存文件的错误（config_client.go:200-215），照原话报错等于把真因丢了。
func TestLoad_GivesUpAtTheBoundAndNamesTheCandidateCause(t *testing.T) {
	quickLoadRetry(t)
	sdkErr := errors.New("read config from both server and cache fail, err=config file not exist")
	stub := &stubClient{getErrs: allErrors(sdkErr, loadRetryAttempts)}
	src, err := NewWithClient(stub, WithDataID("app.yaml"))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	_, err = src.Load(context.Background(), "")
	if err == nil {
		t.Fatal("持续判限时 Load 不该成功")
	}
	if stub.calls != loadRetryAttempts {
		t.Errorf("GetConfig 调了 %d 次，want 上界 %d", stub.calls, loadRetryAttempts)
	}
	if !errors.Is(err, sdkErr) {
		t.Errorf("没有用 %%w 带上原错误: %v", err)
	}
	if !strings.Contains(err.Error(), "5 config queries per second") {
		t.Errorf("错误里没有候选真因（本地桶）这句: %v", err)
	}
	if !strings.Contains(err.Error(), "app.yaml") {
		t.Errorf("错误里没带 dataID: %v", err)
	}
}

// TestLoad_CancellationAbortsTheRetryWait ctx 取消后不再烧重试上界。
//
// 这条也顺带把 Load 的 ctx 从「摆设」变成真听：改动前 ctx 完全没被用到。
// 这里**故意不压小间隔**——要判的是「等待被取消打断」，间隔压到 1ms 会让等待自己
// 先过去，判据就空转了。ctx 已取消 ⇒ select 只有一路就绪，没有竞速。
func TestLoad_CancellationAbortsTheRetryWait(t *testing.T) {
	stub := &stubClient{getErrs: allErrors(errors.New("ConfigQueryRequest is limited"), loadRetryAttempts)}
	src, err := NewWithClient(stub, WithDataID("app.yaml"))
	if err != nil {
		t.Fatalf("NewWithClient: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	_, err = src.Load(ctx, "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load 的错误 = %v，want context.Canceled", err)
	}
	if stub.calls != 1 {
		t.Errorf("GetConfig 调了 %d 次，want 1（取消后不该继续重试）", stub.calls)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("取消后仍等了 %s，want 立刻退出", d)
	}
}

// TestWatchValue_WithStub 桩注入：ListenConfig 注册 + ctx 取消反注册。
func TestWatchValue_WithStub(t *testing.T) {
	// 必须经 newStubClient 初始化 listen/cancelled 通道：
	// 零值桩的 nil channel 会让 CancelListenConfig 的 select 发送永不就绪，
	// 反注册信号发不出来（会导致本测试超时）。
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
	// getErrs 逐次返回：非 nil 即本次 GetConfig 失败，越界后一律成功。
	// calls 记 GetConfig 的总调用次数（重试上界的判据就读它）。
	getErrs []error
	calls   int
}

func newStubClient() *stubClient {
	return &stubClient{listen: make(chan struct{}, 1), cancelled: make(chan struct{}, 1)}
}

func (s *stubClient) GetConfig(param vo.ConfigParam) (string, error) {
	s.lastParam = param
	i := s.calls
	s.calls++
	if i < len(s.getErrs) && s.getErrs[i] != nil {
		return "", s.getErrs[i]
	}
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

// TestNamespaceID_DefaultNameFoldsToEmpty 钉住 nacos 默认命名空间的两种写法：
// 显示名 "public" 必须折成空串（SDK 的 tenant 参数里默认空间是空），自建
// namespace 的 ID 原样透传。真机实测（v2.3.2 standalone）：NamespaceId="public"
// 时 GetConfig 返回「空内容 + 无错误」，配置源静默贡献零个键。
func TestNamespaceID_DefaultNameFoldsToEmpty(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"public", ""},
		{"", ""},
		{"7e7197f2-e44d-4c05-a8f6-9e0f3d2b1a4c", "7e7197f2-e44d-4c05-a8f6-9e0f3d2b1a4c"},
	} {
		if got := namespaceID(tc.in); got != tc.want {
			t.Errorf("namespaceID(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestClientConfig_NamespaceIsFolded 判折叠真的接进了建连参数（只测纯函数
// 判不出「init 用了别的字段」这条形状）。
func TestClientConfig_NamespaceIsFolded(t *testing.T) {
	cc := clientConfig(options{namespace: "public", username: "u", password: "p"})
	if cc.NamespaceId != "" {
		t.Errorf("ClientConfig.NamespaceId = %q, want \"\"（public 折成默认空间）", cc.NamespaceId)
	}
	if cc.Username != "u" || cc.Password != "p" || cc.TimeoutMs != 5000 {
		t.Errorf("ClientConfig = %+v, want 凭证透传 + 5s 超时", cc)
	}
	if got := clientConfig(options{namespace: "ns-1"}).NamespaceId; got != "ns-1" {
		t.Errorf("自建 namespace 被改写: %q", got)
	}
}
