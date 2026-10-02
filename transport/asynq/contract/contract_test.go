package contract

// contract_test.go —— asynq 契约装配的字段映射与三态语义。
//
// 锁定的不变量（Wave 3）：
//  1. 段缺失 → (nil, nil, nil)，BuildServers 跳过；
//  2. optional bool 为 nil 时不覆盖实现默认（反向陷阱防护）；
//  3. 显式 true/false 正确覆盖；
//  4. queues 有序列表 → 递减权重（保优先级，不退化为轮询）；
//  5. handler 回调在 server 构造后被调用（时序：早于 Start）。
//
// 用真实 asynq.Server（不 mock）——构造不联网，可安全实例化。

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/asynq"
)

func boolPtr(b bool) *bool { return &b }

// TestProvider_SectionMissingSkips 段缺失 → nil server（跳过语义）。
func TestProvider_SectionMissingSkips(t *testing.T) {
	p := Provider()
	srv, cleanup, err := p(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (asynq section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_SectionPresentConstructs 段存在 → 构造出 *asynq.Server。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	p := Provider()
	cfg := &bootstrapv1.Server{
		Asynq: &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"},
	}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv == nil {
		t.Fatal("srv = nil, want *asynq.Server")
	}
	if _, ok := srv.(*asynq.Server); !ok {
		t.Fatalf("srv type = %T, want *asynq.Server", srv)
	}
}

// TestQueueWeights_DescendingByOrder 有序队列列表 → 递减权重（首项最高）。
func TestQueueWeights_DescendingByOrder(t *testing.T) {
	w := queueWeights([]string{"critical", "default", "low"})
	if w["critical"] != 3 || w["default"] != 2 || w["low"] != 1 {
		t.Fatalf("queueWeights = %v, want critical:3 default:2 low:1 (descending by contract order)", w)
	}
}

// TestBuildOpts_NilBoolsNotApplied 三态：optional bool 为 nil 时**不**产生 Option。
//
// 这是反向陷阱的直接防护：asynq 的调度器等开关实现默认启用，若 provider 恒传
// false 会关掉它们。buildOpts 对 nil 字段不追加 Option——体现为切片长度。
//
// 为什么断言切片长度而非运行时读回：asynq.Server 的开关字段未导出且无 getter
// （LSP 实测 SchedulerEnabled undefined），运行时不可观测。Option 是黑盒函数值，
// 唯一可测的可观测面就是「是否被追加」——长度差正是该语义的等价观测。
func TestBuildOpts_NilBoolsNotApplied(t *testing.T) {
	// 仅 RedisAddress → 恰好 1 个 Option（nil 的 optional bool 均不追加）。
	sec := &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"}
	if n := len(buildOpts(sec)); n != 1 {
		t.Fatalf("len(buildOpts) = %d, want 1 (only redis_address; nil optionals must not append)", n)
	}
	// 构造本身必须成功（nil 三态不得导致 panic 或错误 Option）。
	if srv := asynq.NewServer(buildOpts(sec)...); srv == nil {
		t.Fatal("NewServer returned nil with nil optional bools")
	}
}

// TestBuildOpts_ExplicitFalseAppends 三态：显式 false 追加对应 Option
// （长度比 nil 时多 1），证明 nil 与 false 被区分。
func TestBuildOpts_ExplicitFalseAppends(t *testing.T) {
	nilCase := len(buildOpts(&bootstrapv1.Server_Asynq{RedisAddress: "x"}))

	sec := &bootstrapv1.Server_Asynq{
		RedisAddress:     "x",
		SchedulerEnabled: boolPtr(false),
	}
	if n := len(buildOpts(sec)); n != nilCase+1 {
		t.Fatalf("len(buildOpts) = %d, want %d (explicit false must append one Option)", n, nilCase+1)
	}
}

// TestWithHandlers_CalledAfterConstruct handler 回调在构造后被调用。
func TestWithHandlers_CalledAfterConstruct(t *testing.T) {
	called := false
	p := Provider(WithHandlers(func(_ context.Context, s *asynq.Server) error {
		called = true
		if s == nil {
			t.Fatal("handler got nil server")
		}
		return nil
	}))
	cfg := &bootstrapv1.Server{
		Asynq: &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"},
	}
	if _, _, err := p(context.Background(), cfg); err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if !called {
		t.Fatal("handler callback was not invoked")
	}
}

// TestProvider_HandlerErrorShortCircuits handler 返回 error → 装配失败。
func TestProvider_HandlerErrorShortCircuits(t *testing.T) {
	p := Provider(WithHandlers(func(context.Context, *asynq.Server) error {
		return context.DeadlineExceeded
	}))
	cfg := &bootstrapv1.Server{
		Asynq: &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"},
	}
	if _, _, err := p(context.Background(), cfg); err == nil {
		t.Fatal("Provider() = nil error, want handler error propagated")
	}
}

// TestProvider_AddressResolverFallback 段 redis_address 为空 → 用解析器回退地址
// （保持既有部署兼容：配了 cache.redis 即启用 asynq）。
func TestProvider_AddressResolverFallback(t *testing.T) {
	called := false
	p := Provider(WithAddressResolver(func() string {
		called = true
		return "127.0.0.1:6379"
	}))
	cfg := &bootstrapv1.Server{Asynq: &bootstrapv1.Server_Asynq{}} // 地址空
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if srv == nil {
		t.Fatal("srv = nil, want constructed server via resolver fallback")
	}
	if !called {
		t.Fatal("address resolver was not invoked when section address is empty")
	}
}

// TestProvider_AddressResolverNotCalledWhenSectionSet 段显式配地址 → 不调解析器
// （显式配置优先于回退）。
func TestProvider_AddressResolverNotCalledWhenSectionSet(t *testing.T) {
	called := false
	p := Provider(WithAddressResolver(func() string {
		called = true
		return "should-not-be-used:6379"
	}))
	cfg := &bootstrapv1.Server{
		Asynq: &bootstrapv1.Server_Asynq{RedisAddress: "127.0.0.1:6379"},
	}
	if _, _, err := p(context.Background(), cfg); err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if called {
		t.Fatal("address resolver was invoked despite explicit section address (explicit must win)")
	}
}
