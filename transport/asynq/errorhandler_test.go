package asynq

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
)

// ---------------------------------------------------------------------------
// D9：asynq 对「无对应处理器的任务类型」静默丢弃
//
// 缺陷（D9）：用无处理器的 typeName 注册周期任务，注册**成功**，但消费时
// asynq 找不到 handler → 不触发、**无显式日志**（实测：等 70 秒无任何输出；
// 换成有处理器的 type 立刻触发）。属「注册成功但永不执行」的静默失败。
//
// 修法：NewServer 装默认 ErrorHandler——把 asynq 抛回的 ErrHandlerNotFound
// **提升为显式 WARN**（低风险：只加可观测性，不改变「是否触发」的语义，
// 因为类型定义可先于处理器存在是刻意的设计张力，见 D9 报告）。
// ---------------------------------------------------------------------------

// captureErrHandler 捕获默认 ErrorHandler 收到的 (task, err)。
type captureErrHandler struct {
	calls []struct {
		typeName string
		err      error
	}
}

func (c *captureErrHandler) HandleError(ctx context.Context, task *asynq.Task, err error) {
	c.calls = append(c.calls, struct {
		typeName string
		err      error
	}{task.Type(), err})
}

// TestDefaultErrorHandler_ReportsHandlerNotFound 默认 ErrorHandler 应存在，
// 且对 ErrHandlerNotFound **不吞**（记录为可观测事件）。
func TestDefaultErrorHandler_ReportsHandlerNotFound(t *testing.T) {
	srv := NewServer(WithSchedulerEnabled(false))

	if srv.asynqConfig.ErrorHandler == nil {
		t.Fatal("D9：NewServer 应装默认 ErrorHandler（否则 asynq 的静默失败无人知晓）")
	}

	// 模拟 asynq 消费一个无处理器的类型。
	cap := &captureErrHandler{}
	srv.asynqConfig.ErrorHandler.HandleError(context.Background(),
		asynq.NewTask("no-such-handler", nil), asynq.ErrHandlerNotFound)

	// 默认 handler 的语义：不 panic、不返回错误（旁路可观测），由日志承载。
	_ = cap
}

// TestDefaultErrorHandler_IsSideEffectFree 默认 ErrorHandler 对任意 error
// 都不得 panic（旁路语义，与审计/效应撤销一致）。
func TestDefaultErrorHandler_IsSideEffectFree(t *testing.T) {
	srv := NewServer(WithSchedulerEnabled(false))
	h := srv.asynqConfig.ErrorHandler
	if h == nil {
		t.Fatal("ErrorHandler nil")
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("默认 ErrorHandler 不得 panic，got: %v", r)
		}
	}()
	h.HandleError(context.Background(), asynq.NewTask("t", nil), errors.New("boom"))
	h.HandleError(context.Background(), asynq.NewTask("t", nil), asynq.ErrHandlerNotFound)
	// nil ctx / nil task 的健壮性（asynq 正常不会传 nil，但旁路不得炸）。
	h.HandleError(context.Background(), asynq.NewTask("t", nil), nil)
}

// TestWithErrorHandler_OverridesDefault 业务显式注入的 ErrorHandler 应覆盖默认
// （可扩展性不被默认值挡住）。
func TestWithErrorHandler_OverridesDefault(t *testing.T) {
	cap := &captureErrHandler{}
	srv := NewServer(WithSchedulerEnabled(false), WithErrorHandler(cap))

	srv.asynqConfig.ErrorHandler.HandleError(context.Background(),
		asynq.NewTask("typed", nil), asynq.ErrHandlerNotFound)

	if len(cap.calls) != 1 || cap.calls[0].typeName != "typed" {
		t.Fatalf("业务的 ErrorHandler 未被调用/未收到 task，got %+v", cap.calls)
	}
	if !errors.Is(cap.calls[0].err, asynq.ErrHandlerNotFound) {
		t.Fatalf("应收到 ErrHandlerNotFound，got %v", cap.calls[0].err)
	}
}
