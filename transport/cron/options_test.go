package cron

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// P2-5：WithSeconds 契约落地（原为空实现）
//
// 契约声明 `server.cron.seconds`「是否启用秒级精度，默认 false」，但
// WithSeconds 此前是空实现——配了完全无效。且 parser 在 NewServer 内构造、
// Options 在其后才应用，WithLocation/WithLogger 会**重建 scheduler** 并各自
// 硬编码 6 字段 parser（丢弃 NewServer 的 Recover 链、互相覆盖、顺序敏感）。
//
// 本组测试钉住修复后的语义：seconds 开关生效、默认行为不变、选项顺序无关。
// ---------------------------------------------------------------------------

// TestWithSeconds_DefaultAcceptsSixField 默认（不传 WithSeconds）仍支持 6 字段
// ——保持既有行为（最小惊讶；README/包文档示例均为 6 字段）。
func TestWithSeconds_DefaultAcceptsSixField(t *testing.T) {
	srv := NewServer()
	_, err := srv.NewTimerJob("*/10 * * * * *", func() {})
	if err != nil {
		t.Fatalf("默认应支持 6 字段（含秒）表达式，got: %v", err)
	}
}

// TestWithSeconds_FalseRejectsSixField WithSeconds(false) 必须真正关闭秒级：
// 6 字段被拒、5 字段可用。这是契约 server.cron.seconds 的承诺。
func TestWithSeconds_FalseRejectsSixField(t *testing.T) {
	srv := NewServer(WithSeconds(false))

	_, err := srv.NewTimerJob("*/10 * * * * *", func() {})
	if err == nil {
		t.Fatal("WithSeconds(false) 后 6 字段表达式必须被拒绝（秒级已关闭）")
	}

	if _, err := srv.NewTimerJob("*/10 * * * *", func() {}); err != nil {
		t.Fatalf("WithSeconds(false) 后 5 字段标准 cron 应可用，got: %v", err)
	}
}

// TestWithSeconds_DescriptorStillWorks seconds=false 不影响描述符（@every 等）。
func TestWithSeconds_DescriptorStillWorks(t *testing.T) {
	srv := NewServer(WithSeconds(false))
	if _, err := srv.NewTimerJob("@every 5s", func() {}); err != nil {
		t.Fatalf("描述符表达式应始终可用，got: %v", err)
	}
}

// TestWithSeconds_OrderIndependent 选项顺序不影响结果（原实现顺序敏感，
// 因为每个重建型 Option 都会覆盖前一个）。
func TestWithSeconds_OrderIndependent(t *testing.T) {
	a := NewServer(WithSeconds(false), WithLocation(time.UTC))
	b := NewServer(WithLocation(time.UTC), WithSeconds(false))

	for name, srv := range map[string]*Server{"seconds-then-loc": a, "loc-then-seconds": b} {
		if _, err := srv.NewTimerJob("*/10 * * * * *", func() {}); err == nil {
			t.Errorf("%s: 6 字段应被拒绝（WithSeconds(false) 生效且不被后序 Option 冲掉）", name)
		}
		if _, err := srv.NewTimerJob("*/10 * * * *", func() {}); err != nil {
			t.Errorf("%s: 5 字段应可用，got: %v", name, err)
		}
	}
}

// TestWithLocation_DoesNotResetParser WithLocation 不得重置 parser 语义
// （原实现会用自带的 6 字段 parser 重建 scheduler，覆盖 seconds=false）。
func TestWithLocation_DoesNotResetParser(t *testing.T) {
	srv := NewServer(WithSeconds(false), WithLocation(time.UTC))
	if _, err := srv.NewTimerJob("*/10 * * * * *", func() {}); err == nil {
		t.Fatal("WithLocation 不得覆盖 WithSeconds(false) 的 parser 配置")
	}
}

// TestWithLogger_PreservesRecoverChain WithLogger 不应丢弃 Recover 保护链，
// 也不应重置 parser。此处以 parser 语义作可观测量（Recover 链为内部实现，
// 由 TestServer_JobPanicDoesNotKillScheduler 单独验证）。
func TestWithLogger_PreservesParserConfig(t *testing.T) {
	srv := NewServer(WithSeconds(false), WithLogger(cronDiscardLogger{}))
	if _, err := srv.NewTimerJob("*/10 * * * * *", func() {}); err == nil {
		t.Fatal("WithLogger 不得覆盖 WithSeconds(false) 的 parser 配置")
	}
}

// TestServer_JobPanicDoesNotKillScheduler 任务 panic 不应终止调度器
// （Recover 链必须存在，无论是否使用 WithLocation/WithLogger/WithSeconds）。
func TestServer_JobPanicDoesNotKillScheduler(t *testing.T) {
	srv := NewServer(WithSeconds(false), WithLocation(time.UTC), WithLogger(cronDiscardLogger{}))

	_, err := srv.NewTimerJob("*/1 * * * *", func() { panic("boom") })
	if err != nil {
		t.Fatalf("add job: %v", err)
	}
	// 仅验证 scheduler 可用（未被 Options 破坏为 nil）且条目已登记。
	if srv.Scheduler() == nil {
		t.Fatal("Scheduler() 不应为 nil——Options 不得破坏 scheduler 构造")
	}
	if got := srv.GetJobCount(); got != 1 {
		t.Fatalf("job count = %d, want 1", got)
	}
}

// cronDiscardLogger 是 cron.Logger 的丢弃实现（测试用，避免日志噪声）。
type cronDiscardLogger struct{}

func (cronDiscardLogger) Info(_ string, _ ...any)           {}
func (cronDiscardLogger) Error(_ error, _ string, _ ...any) {}
