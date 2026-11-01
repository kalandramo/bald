// Package cron provides a cron-based timer server that implements the
// [transport.Server] interface.
//
// It wraps [robfig/cron/v3] with support for second-level expressions
// (second minute hour day month weekday) and cron descriptors (e.g. "@every 5s",
// "@daily"). The server lifecycle is managed via the standard Start/Stop
// pattern, making it compatible with [wind.App].
//
// Usage:
//
//	import cronServer "github.com/kalandramo/bald/transport/cron"
//
//	srv := cronServer.NewServer()
//
//	// Register jobs BEFORE Start (recommended) or after.
//	srv.NewTimerJob("*/10 * * * * *", func() {
//	    log.Println("runs every 10 seconds")
//	})
//
//	if err := srv.Start(ctx); err != nil { ... }
package cron

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/kalandramo/bald/transport"
)

// KindCron 是 Cron 传输类型标识。
const KindCron = "cron"

// 确保 Server 实现了 wind transport.Server 接口。
var _ transport.Server = (*Server)(nil)

// ---------------------------------------------------------------------------
// Server
// ---------------------------------------------------------------------------

// Server 是基于 robfig/cron 的定时任务服务器，实现 transport.Server 接口。
// 它管理 cron 调度器的完整生命周期，支持动态添加/移除定时任务。
type Server struct {
	mu sync.Mutex

	started  atomic.Bool
	stopping atomic.Bool

	cronScheduler *cron.Cron

	entryIDs sync.Map // cron.EntryID -> spec string
	cronMu   sync.Mutex

	gracefullyShutdown bool
	shutdownTimeout    time.Duration

	// stopDone 在首次停机完成后关闭，供并发调用方等待停机结束。
	stopDone chan struct{}
}

// defaultShutdownTimeout 是 Start 因 ctx 取消触发的停机窗口默认值。
// 与 appkit 默认 stopTimeout 对齐，避免服务器停机拖过引擎的停机预算。
const defaultShutdownTimeout = 10 * time.Second

// NewServer 创建一个 Cron 服务器实例。
// 默认使用支持秒级表达式和描述符的解析器（可用 [WithSeconds] 关闭秒级）。
//
// 构造顺序：先求值全部 Option（得到最终配置），再**一次性**构造 scheduler ——
// 避免「每个 Option 各自重建 scheduler」导致的 Recover 链丢失与互相覆盖。
func NewServer(opts ...Option) *Server {
	o := options{
		seconds:            true, // 默认保留秒级：与既有行为/文档示例一致
		gracefullyShutdown: true,
	}
	for _, opt := range opts {
		opt(&o)
	}

	schedulerOpts := []cron.Option{
		cron.WithParser(buildParser(o.seconds)),
		cron.WithChain(
			cron.Recover(cron.PrintfLogger(log.New(log.Writer(), "[cron] ", log.LstdFlags))),
		),
	}
	if o.location != nil {
		schedulerOpts = append(schedulerOpts, cron.WithLocation(o.location))
	}
	if o.logger != nil {
		schedulerOpts = append(schedulerOpts, cron.WithLogger(o.logger))
	}

	srv := &Server{
		cronScheduler:      cron.New(schedulerOpts...),
		entryIDs:           sync.Map{},
		gracefullyShutdown: o.gracefullyShutdown,
		shutdownTimeout:    o.shutdownTimeout,
		stopDone:           make(chan struct{}),
	}
	if srv.shutdownTimeout <= 0 {
		srv.shutdownTimeout = defaultShutdownTimeout
	}

	return srv
}

// ---------------------------------------------------------------------------
// transport.Server 实现
// ---------------------------------------------------------------------------

// Start 启动 Cron 调度器，阻塞直到 ctx 被取消。
// ctx 取消后使用 shutdownTimeout 作为优雅停机窗口，不会无限等待运行中的任务。
func (s *Server) Start(ctx context.Context) error {
	s.mu.Lock()
	if s.started.Load() {
		s.mu.Unlock()
		return nil
	}

	log.Println("[cron] scheduler starting...")
	s.cronScheduler.Start()
	s.started.Store(true)
	s.mu.Unlock()
	log.Printf("[cron] scheduler started, %d job(s) registered", s.GetJobCount())

	// 阻塞等待 ctx 取消
	<-ctx.Done()

	s.mu.Lock()
	s.started.Store(false)
	s.mu.Unlock()

	// ctx 已取消，不能直接复用；保留其值并换上有界的停机窗口。
	stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.shutdownTimeout)
	defer cancel()
	return s.stopInternal(stopCtx)
}

// Stop 优雅关闭 Cron 调度器。ctx 携带停机超时（必须未取消）。
func (s *Server) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.started.Store(false)
	s.mu.Unlock()

	return s.stopInternal(ctx)
}

func (s *Server) stopInternal(ctx context.Context) error {
	select {
	case <-s.stopDone:
		return nil
	default:
	}

	if !s.stopping.CompareAndSwap(false, true) {
		// 另一个调用方正在停机：等待其结束，同时尊重本次调用的截止时间。
		select {
		case <-s.stopDone:
		case <-ctx.Done():
		}
		return nil
	}
	defer func() {
		s.stopping.Store(false)
		close(s.stopDone)
	}()

	log.Println("[cron] scheduler stopping...")

	// Stop 会立即停止调度，并返回一个在运行中任务全部结束后取消的 ctx。
	stopped := s.cronScheduler.Stop()
	if !s.gracefullyShutdown {
		log.Println("[cron] not waiting for running jobs")
		return nil
	}

	select {
	case <-stopped.Done():
		log.Println("[cron] all jobs stopped gracefully")
	case <-ctx.Done():
		log.Println("[cron] shutdown timeout, running jobs abandoned")
	}

	return nil
}

// Endpoint 返回服务器的访问端点描述。
func (s *Server) Endpoint() string {
	return fmt.Sprintf("%s://scheduler", KindCron)
}

// ---------------------------------------------------------------------------
// 定时任务管理
// ---------------------------------------------------------------------------

// NewTimerJob 添加一个 cron 定时任务。
// spec 支持：
//   - 秒级表达式: "*/10 * * * * *"（每 10 秒）
//   - 描述符: "@every 5s", "@daily", "@hourly" 等
//
// 可以在 Start 前或 Start 后调用。Start 前注册的任务会在调度器启动时立即调度。
func (s *Server) NewTimerJob(spec string, cmd func()) (cron.EntryID, error) {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	entryID, err := s.cronScheduler.AddFunc(spec, cmd)
	if err != nil {
		return 0, fmt.Errorf("cron: add job failed (spec=%q): %w", spec, err)
	}

	s.entryIDs.Store(entryID, spec)
	log.Printf("[cron] job added: id=%d, spec=%s", entryID, spec)
	return entryID, nil
}

// RemoveTimerJob 根据任务 ID 移除单个定时任务。
func (s *Server) RemoveTimerJob(entryID cron.EntryID) {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	s.cronScheduler.Remove(entryID)
	if spec, ok := s.entryIDs.LoadAndDelete(entryID); ok {
		log.Printf("[cron] job removed: id=%d, spec=%s", entryID, spec)
	}
}

// RemoveAllJobs 移除所有定时任务。
func (s *Server) RemoveAllJobs() {
	s.cronMu.Lock()
	defer s.cronMu.Unlock()

	count := 0
	s.entryIDs.Range(func(key, _ any) bool {
		if entryID, ok := key.(cron.EntryID); ok {
			s.cronScheduler.Remove(entryID)
			count++
		}
		return true
	})

	s.entryIDs = sync.Map{}
	log.Printf("[cron] all jobs removed, total: %d", count)
}

// GetJobCount 获取当前注册的任务数量。
func (s *Server) GetJobCount() int {
	count := 0
	s.entryIDs.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

// GetEntries 返回当前调度器中的所有任务条目。
func (s *Server) GetEntries() []cron.Entry {
	return s.cronScheduler.Entries()
}

// Scheduler 返回底层的 cron.Cron 实例。
// 注意：直接操作底层调度器时需自行管理并发安全。
func (s *Server) Scheduler() *cron.Cron {
	return s.cronScheduler
}
