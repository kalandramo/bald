package rocketmq

// logger_test.go —— SDK 日志注入姿势的闸门（风险登记册 §7.24 的「bald 自己的竞争」那一半）。
//
// 本机 -race 在 broker/rocketmq 两个包的完整包级跑里量到 28 个竞争块（v2 5 个、contract
// 23 个），其中 24 个指向同一处 bald 侧的写法，成对出现：
//   - 写侧：每次 Init 都 `rlog.SetLogger(...)`，而注进去的是**那台 broker 自带的**
//     `*logger`（每个对象一个 plain 字段 `level`，Init 里直接赋值）；
//   - 读侧：任何一个**活着**的 SDK 客户端线程 —— 心跳、路由查询、rebalance 都经
//     rlog 的包级函数走那个全局（rlog/log.go:201-225 读 `rLog` 后直接调，全程不带锁）。
//
// 注意读侧不要求「有泄漏的线程」：本包只要已经起过一个客户端，它的线程就在读这个全局，
// 下一次 Init 的写与之相冲即可复现。这两处都不是上游库的锅，属于 bald 自己可以修的东西
// ——排除清单（.github/race-skip.txt）的规矩是「bald 自己的竞争要修，不进这里」。
//
// 修法见 rocketmq.go 的 sdkLogger/publishLogger：进程内共用一个转发对象，只在第一次
// Init 时注入一次，级别改成原子读写。这里判两件事——
//   1. 级别仍然生效，而且生效在**被注入的那一个对象**上（把注入改成一次、同时把级别
//      丢掉，是一种「为了绿而删能力」的修法，这一族判据专门拦它）；
//   2. 并发 Init 与并发日志不再相冲（这一条只有 `-race` 才判得动，见该用例注释）。
//
// 观测面：bald 自己的日志全局（log.SetLogger）——SDK 的 rlog.Info 经被注入的转发器
// 最终落到这里，所以「哪一级能过」是可直接数的行数，不需要假后端。

import (
	"context"
	"sync"
	"testing"

	"github.com/apache/rocketmq-client-go/v2/rlog"

	"github.com/kalandramo/bald/broker/rocketmq/option"
	"github.com/kalandramo/bald/log"
)

// capture 是装进 bald 日志全局的计数器，实现 log.Logger 契约。
type capture struct {
	mu    sync.Mutex
	lines map[string]int
}

func newCapture() *capture { return &capture{lines: make(map[string]int)} }

func (c *capture) record(msg string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines[msg]++
}

func (c *capture) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lines[msg]
}

func (c *capture) Debug(_ context.Context, msg string, _ ...any) { c.record(msg) }
func (c *capture) Info(_ context.Context, msg string, _ ...any)  { c.record(msg) }
func (c *capture) Warn(_ context.Context, msg string, _ ...any)  { c.record(msg) }
func (c *capture) Error(_ context.Context, msg string, _ ...any) { c.record(msg) }

func (c *capture) Enabled(log.Level) bool { return true }
func (c *capture) With(...any) log.Logger { return c }

// installCapture 把 capture 装进全局，并把级别恢复与全局恢复挂在 t.Cleanup 上。
func installCapture(t *testing.T) *capture {
	t.Helper()

	c := newCapture()
	prev := log.GetLogger()
	log.SetLogger(c)
	t.Cleanup(func() {
		log.SetLogger(prev)
		sdkLogger.setLevel(log.LevelInfo)
	})
	return c
}

// TestLoggerLevelReachesInjectedLogger 判第 1 件事：级别走的是**注入进 SDK 的那一个**对象。
//
// 三步各自拦一种改坏：
//   - Debug 级选项后 `rlog.Debug` 必须落地：拦「级别写到了 per-broker 对象、
//     被注入的那个永远停在默认 Info」（那是把竞争修好了但把能力删了）。
//   - 后一次 Init 覆盖成 Info 后 `rlog.Debug` 必须不再落地、`rlog.Info` 仍落地：
//     钉住「后一次 Init 生效」这个改动前就有的可观察语义。
//   - 不带级别选项的 Init **不**把级别重置回默认：这是本轮有意改变的另一半——
//     旧写法里每台 broker 都自带默认 Info 的新对象并注入，于是一台不带选项的
//     broker 会把全进程的 SDK 日志级别拽回去；共用一个转发器之后，级别只在
//     显式给出时被覆盖。
func TestLoggerLevelReachesInjectedLogger(t *testing.T) {
	c := installCapture(t)

	debugBroker := NewBroker(option.WithLoggerLevel(log.LevelDebug))
	if err := debugBroker.Init(); err != nil {
		t.Fatalf("Init(Debug): %v", err)
	}
	rlog.Debug("sdk-debug-with-option", nil)
	if n := c.count(loggerKey + "sdk-debug-with-option"); n != 1 {
		t.Fatalf("Debug 级选项下 rlog.Debug 落地 %d 行, want 1 —— 级别没走到被注入的转发器上", n)
	}

	infoBroker := NewBroker(option.WithLoggerLevel(log.LevelInfo))
	if err := infoBroker.Init(); err != nil {
		t.Fatalf("Init(Info): %v", err)
	}
	rlog.Debug("sdk-debug-after-raise", nil)
	rlog.Info("sdk-info-after-raise", nil)
	if n := c.count(loggerKey + "sdk-debug-after-raise"); n != 0 {
		t.Fatalf("后一次 Init(Info) 之后 rlog.Debug 仍落地 %d 行, want 0 —— 「后一次 Init 生效」丢了", n)
	}
	if n := c.count(loggerKey + "sdk-info-after-raise"); n != 1 {
		t.Fatalf("rlog.Info 落地 %d 行, want 1", n)
	}

	plainBroker := NewBroker()
	if err := plainBroker.Init(); err != nil {
		t.Fatalf("Init(无级别选项): %v", err)
	}
	rlog.Info("sdk-info-after-plain-init", nil)
	if n := c.count(loggerKey + "sdk-info-after-plain-init"); n != 1 {
		t.Fatalf("不带级别选项的 Init 之后 rlog.Info 落地 %d 行, want 1 —— 无选项的 Init 把级别拽回默认了", n)
	}
	rlog.Debug("sdk-debug-after-plain-init", nil)
	if n := c.count(loggerKey + "sdk-debug-after-plain-init"); n != 0 {
		t.Fatalf("不带级别选项的 Init 把级别从 Info 改回了 Debug（落地 %d 行, want 0）", n)
	}
}

// TestLoggerConcurrentInitAndSDKLogging 判第 2 件事：并发 Init 与并发读不再相冲。
//
// 这条腿**只在 -race 下判得动**（它断言的是「没有竞争」，不是某个可观察值）：
// 读侧就是 rlog.Info/rlog.Warning 走的那次全局读 + 那次 level 读，正是旧写法里
// 与 Init 的 `rlog.SetLogger` / `level =` 相冲的两个访问。因此它不需要真客户端，
// 也就不会把上游那个 `close bool` 的竞争牵进来——单独跑它是干净的：
//
//	go test -race -run TestLogger ./v2/
//
// 整个模块仍留在 .github/race-skip.txt 里（上游 `close bool` 落在 Subscribe +
// Disconnect 的必经路径上），所以这条腿在 CI 的 race 作业里不会被跑到；
// 它的用处是本地/手动跑 -race 时把 bald 自己那一半钉住。改回旧写法时这条腿判红
// （本轮实测：写侧回到 Init→SetLogger 后本用例报出 DATA RACE）。
func TestLoggerConcurrentInitAndSDKLogging(t *testing.T) {
	installCapture(t)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				rlog.Info("sdk-并发读", nil)
				if n%2 == 0 {
					rlog.Warning("sdk-并发读", nil)
				}
			}
		}()
	}

	levels := []log.Level{log.LevelDebug, log.LevelInfo, log.LevelWarn, log.LevelError}
	for i := 0; i < 200; i++ {
		b := NewBroker(option.WithLoggerLevel(levels[i%len(levels)]))
		if err := b.Init(); err != nil {
			t.Fatalf("Init #%d: %v", i, err)
		}
	}

	close(stop)
	wg.Wait()
}
