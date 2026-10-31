package asynq

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// RemovePeriodicTask 的参数语义
//
// `NewPeriodicTask(...) (string, error)` 返回 entryID，而
// `RemovePeriodicTask(taskId string)` 收 taskId（任务类型名）并内部反查——
// 方法名未体现差异，容易把 entryID 误传进来（得到 `periodic task not found`）。
//
// 对称入口 `RemovePeriodicTaskByEntryID` 直接接受 NewPeriodicTask 的返回值；
// 两者契约已写进方法文档。
// ---------------------------------------------------------------------------

// newBookkeepingServer 构造一个 scheduler 为 nil 的 Server——用于确定性地测
// taskId↔entryID 反查表的簿记逻辑，无需真实 Redis。
//
// 依据 `unregisterPeriodicTask`（server.go）：`s.scheduler == nil` 时直接返回 nil
// ——即「无 scheduler 时簿记逻辑仍完整执行」。
func newBookkeepingServer() *Server {
	return NewServer(WithSchedulerEnabled(false))
}

// TestRemovePeriodicTask_ByTaskID 既有语义保留：按 taskId（类型名）移除，
// 内部反查 entryID。
func TestRemovePeriodicTask_ByTaskID(t *testing.T) {
	srv := newBookkeepingServer()

	// 直接注入注册表条目（模拟 NewPeriodicTask 成功后的状态）。
	srv.addPeriodicTaskEntryID("task-a", "entry-123")
	srv.addPeriodicTaskEntryID("task-b", "entry-456")

	if srv.QueryPeriodicTaskEntryID("task-a") == "" || srv.QueryPeriodicTaskEntryID("task-b") == "" {
		t.Fatalf("precondition: want 2 entries registered")
	}

	// 按 taskId 移除。
	if err := srv.RemovePeriodicTask("task-a"); err != nil {
		t.Fatalf("RemovePeriodicTask(taskId): %v", err)
	}
	if got := srv.QueryPeriodicTaskEntryID("task-a"); got != "" {
		t.Fatalf("task-a 仍在注册表: %q", got)
	}
	if got := srv.QueryPeriodicTaskEntryID("task-b"); got != "entry-456" {
		t.Fatalf("task-b 不应受影响, got %q", got)
	}
}

// TestRemovePeriodicTask_MissingTaskIDErrors 不存在的 taskId 报错（含 taskId 文本）。
func TestRemovePeriodicTask_MissingTaskIDErrors(t *testing.T) {
	srv := newBookkeepingServer()
	err := srv.RemovePeriodicTask("no-such-task")
	if err == nil {
		t.Fatal("不存在的 taskId 应报错")
	}
	if !strings.Contains(err.Error(), "no-such-task") {
		t.Errorf("错误信息应含 taskId，got: %v", err)
	}
}

// TestRemovePeriodicTaskByEntryID_SymmetricWithNewPeriodicTask 对称方法：
// 直接接受 NewPeriodicTask 的返回值（entryID），无需调用方知道 taskId。
func TestRemovePeriodicTaskByEntryID_SymmetricWithNewPeriodicTask(t *testing.T) {
	srv := newBookkeepingServer()

	// NewPeriodicTask 返回 entryID；此处直接注入同名映射模拟其内部状态。
	srv.addPeriodicTaskEntryID("order:close", "entry-order-close")

	if err := srv.RemovePeriodicTaskByEntryID("entry-order-close"); err != nil {
		t.Fatalf("RemovePeriodicTaskByEntryID: %v", err)
	}
	// 反查映射应被同步清理（不留孤儿条目）。
	if got := srv.QueryPeriodicTaskEntryID("order:close"); got != "" {
		t.Fatalf("按 entryID 移除后反查映射应清空, got %q", got)
	}
}

// TestRemovePeriodicTaskByEntryID_UnknownEntryIDIsNoOp 未知 entryID 不报错
// （移除本就不存在的东西是幂等成功——与 Store.Delete 的幂等语义一致）。
func TestRemovePeriodicTaskByEntryID_UnknownEntryIDIsNoOp(t *testing.T) {
	srv := newBookkeepingServer()
	if err := srv.RemovePeriodicTaskByEntryID("entry-does-not-exist"); err != nil {
		t.Fatalf("未知 entryID 应幂等返回 nil, got: %v", err)
	}
}
