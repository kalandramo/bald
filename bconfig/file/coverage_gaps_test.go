package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// syntheticSource 手工构造一个只带通道的 fsnotify.Watcher：真实 watcher 的事件
// 时序不可控，而事件循环的过滤/派发分支必须按固定顺序命中。该 watcher 不能 Close
// （内部 backend 为 nil），测试只用取消 ctx 让循环退出。
func syntheticSource(path string) (*source, chan fsnotify.Event, chan error) {
	ev := make(chan fsnotify.Event)
	er := make(chan error)
	return &source{
		options: &options{ctx: context.Background(), path: path},
		watcher: &fsnotify.Watcher{Events: ev, Errors: er},
	}, ev, er
}

func TestWithContextOption(t *testing.T) {
	type key struct{}
	ctx := context.WithValue(context.Background(), key{}, "v")
	o := &options{}
	WithContext(ctx)(o)
	if o.ctx != ctx {
		t.Fatal("WithContext 未写入 options.ctx")
	}
}

func TestNew_WatchDirMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "cfg.yaml")
	if _, err := New(WithPath(path), WithWatch(true)); err == nil {
		t.Fatal("父目录不存在时 WithWatch(true) 应当报错")
	}
}

// 结构性零覆盖（登记备查）：
//   file.go:53-55 / 112-114 —— fsnotify.NewWatcher 只在 OS 拒绝创建 kqueue/inotify
//   句柄时失败（fd 耗尽等外部资源状态），包内没有注入点；这两处是构造失败的
//   fail-fast 封装，保留。
//   file.go:44-46 / 75-77 —— filepath.Abs 依赖 os.Getwd；darwin 上删除 cwd 后仍能
//   解析目录，本地无法复现，由 Linux CI 覆盖（TestNew_AbsError 在可复现平台不跳过）。

// TestNew_AbsError 依赖「当前目录被删除后 os.Getwd 失败」这一副作用，部分平台
// （PWD 仍可解析）无法复现；不可复现时按结构性跳过，而不是伪造错误。
func TestNew_AbsError(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Skipf("无法获取当前目录：%v", err)
	}

	dir := t.TempDir()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("切换目录失败：%v", err)
	}
	defer func() { _ = os.Chdir(old) }()

	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("删除工作目录失败：%v", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("目录删除后仍能解析 cwd，绝对路径失败分支不可复现")
	}

	if _, err := New(WithPath("cfg.yaml")); err == nil {
		t.Error("cwd 不可解析时 New 应当返回绝对路径错误")
	}

	s := &source{options: &options{path: "/tmp/default"}}
	if got := s.resolveKey("relative.yaml"); got != "relative.yaml" {
		t.Errorf("resolveKey 在 Abs 失败时应回退原 key，得到 %q", got)
	}
	if got := s.resolveKey(""); got != "/tmp/default" {
		t.Errorf("resolveKey(\"\") 应返回默认 path，得到 %q", got)
	}
}

func TestLoad_ReadError(t *testing.T) {
	dir := t.TempDir()
	s, err := New(WithPath(filepath.Join(dir, "cfg.yaml")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(context.Background(), filepath.Join(dir, "nope.yaml")); err == nil {
		t.Fatal("读取不存在的文件应当报错")
	}
}

func TestWatchValue_LazyWatcherInit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(WithPath(path)) // 不开启 WithWatch，走 WatchValue 惰性初始化
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatalf("惰性初始化 watcher 失败：%v", err)
	}
	cancel()
	for range ch {
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

func TestWatchValue_LazyWatcherAddError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "cfg.yaml")
	s, err := New(WithPath(missing))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.WatchValue(ctx, ""); err == nil {
		t.Fatal("父目录不存在时惰性 Add 应当报错")
	}
}

func TestWatchValue_SyntheticWatcherFilters(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	prev := readFile
	t.Cleanup(func() { readFile = prev })
	readFile = func(p string) ([]byte, error) {
		calls.Add(1)
		return os.ReadFile(p)
	}

	s, ev, _ := syntheticSource(path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// 非目标文件 → 忽略；操作为 Chmod → 忽略；随后一次合法 Write 才读盘。
	ev <- fsnotify.Event{Name: filepath.Join(dir, "other.yaml"), Op: fsnotify.Write}
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Chmod}
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}

	select {
	case data := <-ch:
		if string(data) != "good" {
			t.Errorf("派发内容 = %q，期望 good", data)
		}
	case <-ctx.Done():
		t.Fatal("合法事件未派发")
	}
	// 稳定非空内容只读两次；被过滤的两个事件若触发读盘，计数必然大于 2。
	if got := calls.Load(); got != 2 {
		t.Errorf("读盘次数 = %d，期望 2（过滤分支未生效）", got)
	}

	cancel()
	for range ch {
	}
}

func TestWatchValue_SkipsReadError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	transient := errors.New("transient")
	prev := readFile
	t.Cleanup(func() { readFile = prev })
	readFile = func(p string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return nil, transient
		}
		return os.ReadFile(p)
	}

	s, ev, _ := syntheticSource(path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// 无缓冲通道保证事件按序落到循环：首个事件读失败被跳过，第二个才派发。
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}

	select {
	case data := <-ch:
		if string(data) != "good" {
			t.Errorf("派发内容 = %q，期望 good", data)
		}
	case <-ctx.Done():
		t.Fatal("读失败后的事件没有重新读取")
	}

	cancel()
	for range ch {
	}
}

func TestWatchValue_SkipsUnsettled(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("good"), 0o644); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	unsettled := make(chan struct{})
	prev := readFile
	t.Cleanup(func() { readFile = prev })
	readFile = func(p string) ([]byte, error) {
		n := calls.Add(1)
		if n <= int32(settleAttempts)+1 {
			if n == int32(settleAttempts)+1 {
				close(unsettled)
			}
			return []byte{'v', byte('a' + n)}, nil
		}
		return os.ReadFile(p)
	}

	s, ev, _ := syntheticSource(path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	<-unsettled // 已用满 settleAttempts 次仍不一致
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}

	select {
	case data := <-ch:
		if string(data) != "good" {
			t.Errorf("派发内容 = %q，期望 good", data)
		}
	case <-ctx.Done():
		t.Fatal("不稳定内容被跳过后没有在下一个事件派发")
	}

	cancel()
	for range ch {
	}
}

func TestWatchValue_CtxDoneWhileDispatching(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.yaml")
	if err := os.WriteFile(path, []byte("v"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, ev, _ := syntheticSource(path)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// 首个事件填满容量 1 的输出缓冲且不消费；第二个事件落定后阻塞在发送上。
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	ev <- fsnotify.Event{Name: path, Op: fsnotify.Write}
	time.Sleep(30 * time.Millisecond)
	cancel()

	got := 0
	for range ch {
		got++
	}
	if got != 1 {
		t.Errorf("阻塞期间取消后收到 %d 次派发，期望缓冲中仅 1 次", got)
	}
}

func TestWatchValue_EventsClosed(t *testing.T) {
	ev := make(chan fsnotify.Event)
	close(ev)
	s := &source{
		options: &options{ctx: context.Background(), path: "/tmp/cfg.yaml"},
		watcher: &fsnotify.Watcher{Events: ev, Errors: make(chan error)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Events 关闭后不应再有派发")
		}
	case <-ctx.Done():
		t.Fatal("Events 关闭后输出通道未关闭")
	}
}

func TestWatchValue_ErrorsClosed(t *testing.T) {
	er := make(chan error)
	close(er)
	s := &source{
		options: &options{ctx: context.Background(), path: "/tmp/cfg.yaml"},
		watcher: &fsnotify.Watcher{Events: make(chan fsnotify.Event), Errors: er},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("Errors 关闭后不应再有派发")
		}
	case <-ctx.Done():
		t.Fatal("Errors 关闭后输出通道未关闭")
	}
}

func TestWatchValue_ErrorValue(t *testing.T) {
	s, _, er := syntheticSource("/tmp/cfg.yaml")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	er <- errors.New("watcher broken")
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("watcher 报错后不应再有派发")
		}
	case <-ctx.Done():
		t.Fatal("watcher 报错后输出通道未关闭")
	}
}

type readResult struct {
	data []byte
	err  error
}

// stubReadFile 按顺序返回脚本化结果；多调一次即报错，防止分支悄悄多读。
func stubReadFile(t *testing.T, results []readResult) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	prev := readFile
	t.Cleanup(func() { readFile = prev })
	readFile = func(string) ([]byte, error) {
		n := int(calls.Add(1)) - 1
		if n >= len(results) {
			return nil, errors.New("unexpected read")
		}
		return results[n].data, results[n].err
	}
	return &calls
}

func TestReadSettled_Scripted(t *testing.T) {
	boom := errors.New("boom")
	a := []byte("a")

	tests := []struct {
		name        string
		results     []readResult
		ctx         func() context.Context
		wantData    string
		wantSettled bool
		wantErr     error
		wantCalls   int32
	}{
		{
			name:      "首次读取失败",
			results:   []readResult{{err: boom}},
			wantErr:   boom,
			wantCalls: 1,
		},
		{
			name:      "落定期间第二次读取失败",
			results:   []readResult{{data: a}, {err: boom}},
			wantErr:   boom,
			wantCalls: 2,
		},
		{
			name: "始终不一致",
			results: []readResult{
				{data: []byte("v1")}, {data: []byte("v2")}, {data: []byte("v3")},
				{data: []byte("v4")}, {data: []byte("v5")},
			},
			wantData:  "v5",
			wantCalls: int32(settleAttempts) + 1,
		},
		{
			name:        "稳定非空",
			results:     []readResult{{data: a}, {data: a}},
			wantData:    "a",
			wantSettled: true,
			wantCalls:   2,
		},
		{
			name:      "上下文已取消",
			results:   []readResult{{data: a}},
			ctx:       canceledCtx,
			wantErr:   context.Canceled,
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := stubReadFile(t, tt.results)
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}
			data, settled, err := readSettled(ctx, "unused")
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v，期望 %v", err, tt.wantErr)
			}
			if string(data) != tt.wantData {
				t.Errorf("data = %q，期望 %q", data, tt.wantData)
			}
			if settled != tt.wantSettled {
				t.Errorf("settled = %v，期望 %v", settled, tt.wantSettled)
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Errorf("读取次数 = %d，期望 %d", got, tt.wantCalls)
			}
		})
	}
}

func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestConfirmEmpty_Paths(t *testing.T) {
	boom := errors.New("boom")

	t.Run("确认窗内全空按清空派发", func(t *testing.T) {
		results := make([]readResult, 2+emptyConfirmAttempt)
		calls := stubReadFile(t, results)
		data, settled, err := readSettled(context.Background(), "unused")
		if err != nil || !settled || len(data) != 0 {
			t.Fatalf("data=%q settled=%v err=%v，期望空内容 settled", data, settled, err)
		}
		if got := calls.Load(); got != int32(2+emptyConfirmAttempt) {
			t.Errorf("读取次数 = %d，期望 %d", got, 2+emptyConfirmAttempt)
		}
	})

	t.Run("空窗后读到内容", func(t *testing.T) {
		stubReadFile(t, []readResult{{}, {}, {data: []byte("late")}})
		data, settled, err := readSettled(context.Background(), "unused")
		if err != nil || !settled || string(data) != "late" {
			t.Fatalf("data=%q settled=%v err=%v，期望 late settled", data, settled, err)
		}
	})

	t.Run("确认窗读取失败", func(t *testing.T) {
		stubReadFile(t, []readResult{{}, {}, {err: boom}})
		_, settled, err := readSettled(context.Background(), "unused")
		if !errors.Is(err, boom) || settled {
			t.Fatalf("settled=%v err=%v，期望读取错误", settled, err)
		}
	})

	t.Run("确认窗内取消", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var calls atomic.Int32
		prev := readFile
		t.Cleanup(func() { readFile = prev })
		readFile = func(string) ([]byte, error) {
			if calls.Add(1) == 2 {
				cancel()
			}
			return nil, nil
		}

		_, settled, err := readSettled(ctx, "unused")
		if !errors.Is(err, context.Canceled) || settled {
			t.Fatalf("settled=%v err=%v，期望取消错误", settled, err)
		}
	})
}
