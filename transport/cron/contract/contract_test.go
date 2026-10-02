package contract

// contract_test.go —— cron 契约装配的字段映射与三态语义。
//
// 锁定的不变量（Wave 3）：
//  1. 段缺失 → (nil, nil, nil)；
//  2. optional bool 为 nil 时不覆盖实现默认（cron 的 seconds 默认启用）；
//  3. 非法 location → fail-fast（时区拼错会让任务在意外时间触发）；
//  4. 合法 location → 正确加载；
//  5. jobs 回调在 server 构造后被调用。

import (
	"context"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"

	"github.com/kalandramo/bald/transport/cron"
)

func boolPtr(b bool) *bool { return &b }

// TestProvider_SectionMissingSkips 段缺失 → nil server。
func TestProvider_SectionMissingSkips(t *testing.T) {
	p := Provider()
	srv, cleanup, err := p(context.Background(), &bootstrapv1.Server{})
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if srv != nil {
		t.Fatalf("srv = %v, want nil (cron section absent)", srv)
	}
	if cleanup != nil {
		t.Fatal("cleanup != nil on skip, want nil")
	}
}

// TestProvider_SectionPresentConstructs 段存在 → 构造出 *cron.Server。
func TestProvider_SectionPresentConstructs(t *testing.T) {
	p := Provider()
	cfg := &bootstrapv1.Server{Cron: &bootstrapv1.Server_Cron{}}
	srv, _, err := p(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Provider() error = %v, want nil", err)
	}
	if _, ok := srv.(*cron.Server); !ok {
		t.Fatalf("srv type = %T, want *cron.Server", srv)
	}
}

// TestBuildOpts_NilBoolsNotApplied 三态：optional bool 为 nil → 不追加 Option。
// 空段 cron: {} 必须用实现默认（seconds 启用），不得被零值 false 反转。
func TestBuildOpts_NilBoolsNotApplied(t *testing.T) {
	opts, err := buildOpts(&bootstrapv1.Server_Cron{})
	if err != nil {
		t.Fatalf("buildOpts() error = %v", err)
	}
	if len(opts) != 0 {
		t.Fatalf("len(buildOpts) = %d, want 0 (empty section must not append any Option)", len(opts))
	}
	if srv := cron.NewServer(opts...); srv == nil {
		t.Fatal("NewServer returned nil")
	}
}

// TestBuildOpts_ExplicitFalseAppends 显式 false 追加 Option（与 nil 区分）。
func TestBuildOpts_ExplicitFalseAppends(t *testing.T) {
	opts, err := buildOpts(&bootstrapv1.Server_Cron{Seconds: boolPtr(false)})
	if err != nil {
		t.Fatalf("buildOpts() error = %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("len(buildOpts) = %d, want 1 (explicit false must append)", len(opts))
	}
}

// TestBuildOpts_InvalidLocationFailsFast 非法时区名 → fail-fast。
func TestBuildOpts_InvalidLocationFailsFast(t *testing.T) {
	_, err := buildOpts(&bootstrapv1.Server_Cron{Location: "Not/AZone"})
	if err == nil {
		t.Fatal("buildOpts() = nil error, want fail-fast for invalid location")
	}
}

// TestBuildOpts_ValidLocationLoads 合法时区名 → 追加 1 个 Option。
func TestBuildOpts_ValidLocationLoads(t *testing.T) {
	opts, err := buildOpts(&bootstrapv1.Server_Cron{Location: "Asia/Shanghai"})
	if err != nil {
		t.Fatalf("buildOpts() error = %v", err)
	}
	if len(opts) != 1 {
		t.Fatalf("len(buildOpts) = %d, want 1", len(opts))
	}
}

// TestWithJobs_CalledAfterConstruct jobs 回调在构造后被调用。
func TestWithJobs_CalledAfterConstruct(t *testing.T) {
	called := false
	p := Provider(WithJobs(func(s *cron.Server) error {
		called = true
		if s == nil {
			t.Fatal("jobs callback got nil server")
		}
		// 注册一个真实周期任务（6 字段，默认秒级启用）。
		_, err := s.NewTimerJob("*/30 * * * * *", func() {})
		return err
	}))
	cfg := &bootstrapv1.Server{Cron: &bootstrapv1.Server_Cron{}}
	if _, _, err := p(context.Background(), cfg); err != nil {
		t.Fatalf("Provider() error = %v", err)
	}
	if !called {
		t.Fatal("jobs callback was not invoked")
	}
}

// TestProvider_JobErrorShortCircuits jobs 返回 error → 装配失败。
func TestProvider_JobErrorShortCircuits(t *testing.T) {
	p := Provider(WithJobs(func(*cron.Server) error {
		return context.DeadlineExceeded
	}))
	cfg := &bootstrapv1.Server{Cron: &bootstrapv1.Server_Cron{}}
	if _, _, err := p(context.Background(), cfg); err == nil {
		t.Fatal("Provider() = nil error, want job error propagated")
	}
}
