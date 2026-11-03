package file

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kalandramo/bald/bconfig"
)

func TestNew_EmptyPath(t *testing.T) {
	_, err := New()
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	content := []byte("name: test\nport: 8080\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(WithPath(path))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	data, err := s.Load(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(content) {
		t.Errorf("expected %q, got %q", content, data)
	}
}

func TestLoad_KeyOverride(t *testing.T) {
	dir := t.TempDir()
	path1 := filepath.Join(dir, "a.yaml")
	path2 := filepath.Join(dir, "b.yaml")

	os.WriteFile(path1, []byte("a"), 0o644)
	os.WriteFile(path2, []byte("b"), 0o644)

	s, err := New(WithPath(path1))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	// Default key
	data, _ := s.Load(context.Background(), "")
	if string(data) != "a" {
		t.Errorf("expected 'a', got %q", data)
	}

	// Override key
	data, _ = s.Load(context.Background(), path2)
	if string(data) != "b" {
		t.Errorf("expected 'b', got %q", data)
	}
}

func TestWatchValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch.yaml")

	os.WriteFile(path, []byte("v1"), 0o644)

	s, err := New(WithPath(path), WithWatch(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	// Modify the file after a short delay.
	go func() {
		time.Sleep(200 * time.Millisecond)
		os.WriteFile(path, []byte("v2"), 0o644)
	}()

	select {
	case data := <-ch:
		if string(data) != "v2" {
			t.Errorf("expected 'v2', got %q", data)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for watch event")
	}
}

// TestWatchValue_NoTornEmptySnapshot 钉住三条与事件时序无关的不变量：
//  1. 一次都不派发空载荷——轮间隔（50ms）远大于落定窗口（最坏 5ms×4），撕裂中间态没有
//     借口混进来；派发出去的一定是某一时刻的**完整**值。
//  2. 派发序按重写轮次单调不减——同一个 source 只有一个事件循环，读的一定是当时的
//     最新内容，出现更旧的值只能是把缓存当成了现读。
//  3. 末次派发是最后一个真实值，且至少派发过一次。
//
// 这里刻意**不**断言「每个重写过的值都要被派发」：`ValueWatcher` 的字面契约就是
// 「连续变更允许合并……不保证每个中间值都送达」，而每个事件只做一次落定读，一个被
// 调度延后的事件必然读到更新的内容（-race 与共享 runner 上因此会偶发漏掉某个中间值）。
// 拿比契约还强的断言当门禁，红的是测试不是产品；真正不可接受的是「最后一次写是撕裂的
// 就永久缺项」（不变量 3）与「空快照被当成一层没有 key」（不变量 1）。
func TestWatchValue_NoTornEmptySnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rapid.yaml")
	if err := os.WriteFile(path, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(WithPath(path), WithWatch(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	emitted := make(chan []byte, 1024)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for data := range ch {
			select {
			case emitted <- data:
			default:
			}
		}
	}()

	// 每轮之间留出远大于 settle 窗口的间隔，保证每轮都是一个能独立落定的事件。
	const rounds = 50
	for i := range rounds {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("v%d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second)
	cancel()
	<-drained
	close(emitted)

	empties, total := 0, 0
	last, prev := -1, -1
	for data := range emitted {
		v := string(bytes.TrimSpace(data))
		if v == "" {
			empties++
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(v, "v"))
		if err != nil {
			t.Errorf("派发了不属于重写序列的内容 %q", v)
			continue
		}
		total++
		if n < prev {
			t.Errorf("派发序回退：第 %d 轮出现在第 %d 轮之后（读到更旧的值）", n, prev)
		}
		prev, last = n, n
	}
	if empties > 0 {
		t.Errorf("%d 次派发出空内容（撕裂写中间态漏给了消费者）", empties)
	}
	if total == 0 {
		t.Fatal("50 轮重写一次都没有派发（派发路径整体停摆）")
	}
	if last != rounds-1 {
		t.Errorf("末次派发第 %d 轮，期望第 %d 轮（共 %d 次非空派发）", last, rounds-1, total)
	}
}

// TestWatchValue_StormSettlesFinal 断的是「伤害」而不是次数：200 轮 2ms 间隔的重写
// 比 settle 窗口还密，期间允许合并与丢弃事件，但**末次派发必须是最后一个真实值**——
// 空载荷落在中间会被下一轮纠正，落在最后才是配置永久缺项（正是 CI 首次红的那种）。
//
// 为什么不顺手把「风暴期间 0 次空」也一起断言（那是会骗人的强度）：实测把空确认窗
// 从 50ms×4 放宽到 100ms×4，Linux 容器的泄漏次数没变（前者 8 次/30 轮、后者 3 次/30
// 轮，每轮 200 次重写），且泄漏点里 3/8 次在确认窗读满后的立即复读里就读到了内容——
// 那是写入方被调度停摆 >200ms 造成的真实空窗，读侧任何时序方案都区分不了它和「文件
// 真被清空」。见 [readSettled] 的「残留」。
func TestWatchValue_StormSettlesFinal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "storm.yaml")
	if err := os.WriteFile(path, []byte("v0"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(WithPath(path), WithWatch(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	emitted := make(chan []byte, 1024)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		for data := range ch {
			select {
			case emitted <- data:
			default:
			}
		}
	}()

	const rounds = 200
	for i := range rounds {
		if err := os.WriteFile(path, []byte(fmt.Sprintf("v%d", i)), 0o644); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(time.Second)
	cancel()
	<-drained
	close(emitted)

	empties, total := 0, 0
	last := ""
	for data := range emitted {
		total++
		if len(data) == 0 {
			empties++
			continue
		}
		last = string(data)
	}
	want := fmt.Sprintf("v%d", rounds-1)
	if total == 0 {
		t.Fatal("风暴期间一次都没有派发")
	}
	if last != want {
		t.Errorf("末次派发 %q，期望 %q（共 %d 次派发，其中 %d 次空载荷）", last, want, total, empties)
	}
}

// TestWatchValue_ClearedFileStillDispatches 盯住反面：防空快照的手段不能顺手把
// 「运维真的把配置清空」也吞掉——空载荷必须仍然派发，只是多等确认窗口。
func TestWatchValue_ClearedFileStillDispatches(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clear.yaml")
	if err := os.WriteFile(path, []byte("server: 0.0.0.0:8080"), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := New(WithPath(path), WithWatch(true))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ch, err := s.WatchValue(ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	writeErr := make(chan error, 1)
	go func() {
		time.Sleep(200 * time.Millisecond)
		writeErr <- os.WriteFile(path, nil, 0o644)
	}()

	select {
	case data := <-ch:
		if len(data) != 0 {
			t.Errorf("清空后期望派发空内容，得到 %q", data)
		}
	case <-ctx.Done():
		t.Fatal("清空配置文件后没有收到任何派发")
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("清空文件失败：%v", err)
	}
}

func TestClose_NilWatcher(t *testing.T) {
	s := &source{options: &options{path: "/tmp/test"}}
	if err := s.Close(); err != nil {
		t.Errorf("expected nil error, got %v", err)
	}
}

func TestInterfaceCompliance(t *testing.T) {
	var _ bconfig.Reader = (*source)(nil)
	var _ bconfig.ValueWatcher = (*source)(nil)
	var _ bconfig.Closer = (*source)(nil)
}
