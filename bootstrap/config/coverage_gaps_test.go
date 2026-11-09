package config

// coverage_gaps_test.go —— 78.4% 缺口补齐：错误路径、热更新重连、格式/标量转换
// 与 kratos 适配器的退出分支。均为包内用例，必要时直接构造 Store 覆盖
// Load 已经用不变量排除的防御分支。

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	kratosconfig "github.com/go-kratos/kratos/v3/config"
)

// --- Load 装配错误路径 ---

func TestLoad_NameRequired(t *testing.T) {
	if _, err := Load(Options{}); err == nil {
		t.Fatal("Load without Name should fail")
	}
	// DeferEnv 下 Name 可空（仅文件发现职责）。
	s, err := Load(Options{DeferEnv: true})
	if err != nil {
		t.Fatalf("Load(DeferEnv, empty Name): %v", err)
	}
	_ = s.Close()
}

// failingRemote 让远程桥 Read 失败。
type failingRemote struct{}

func (failingRemote) Read(context.Context) ([]byte, string, error) {
	return nil, "", errors.New("remote down")
}

func (failingRemote) Watch(context.Context, func([]byte, string)) error {
	return errors.New("watch down")
}

func TestLoad_RemoteAssembleError(t *testing.T) {
	_, err := Load(Options{Name: "x", Remote: failingRemote{}})
	if err == nil {
		t.Fatal("Load should fail when remote read fails")
	}
}

// errLoadReader 装配期 Load 失败。
type errLoadReader struct{}

func (errLoadReader) Load(context.Context, string) ([]byte, error) {
	return nil, errors.New("layer down")
}

func TestLoad_LayerAssembleError(t *testing.T) {
	_, err := Load(Options{Name: "x", Layers: []Layer{{Name: "bad", Reader: errLoadReader{}}}})
	if err == nil {
		t.Fatal("Load should fail when layer load fails")
	}
}

func TestLoad_LayerDefaultName(t *testing.T) {
	s, err := Load(Options{
		Name:   "x",
		Layers: []Layer{{Reader: newStubLayerReader("a: 1")}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()
	if got := s.layerM[0].spec.Name; got != "layer-0" {
		t.Fatalf("default layer name = %q, want layer-0", got)
	}
}

// TestLoad_LocalDecodeError 本地文件解码失败：装配报错且自建源被关闭。
func TestLoad_LocalDecodeError(t *testing.T) {
	p := writeTemp(t, "broken.yaml", "a: [")
	_, err := Load(Options{Name: "x", ConfigFile: p})
	if err == nil {
		t.Fatal("Load should fail on undecodable local file")
	}
}

// TestLoad_LocalOpenError 相对路径 + 已删除工作目录 → filepath.Abs 失败。
// darwin 的 Getwd 会解析已删除目录，该分支仅 Linux 可达（CI 覆盖）。
func TestLoad_LocalOpenError(t *testing.T) {
	orig, err := os.Getwd()
	if err != nil {
		t.Skipf("Getwd: %v", err)
	}
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Skipf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(orig) }()
	if err := os.RemoveAll(tmp); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("cwd 删除后仍可解析（darwin），该分支仅在 Linux 可达")
	}
	if _, err := Load(Options{Name: "x", ConfigFile: "rel.yaml"}); err == nil {
		t.Fatal("Load should fail when cwd cannot be resolved")
	}
}

// watchErrReader 声明 Watch 但 WatchValue 返回错误。
type watchErrReader struct{}

func (watchErrReader) Load(context.Context, string) ([]byte, error) {
	return []byte("a: 1"), nil
}

func (watchErrReader) WatchValue(context.Context, string) (<-chan []byte, error) {
	return nil, errors.New("watch refused")
}

func TestLoad_WatchSetupError(t *testing.T) {
	_, err := Load(Options{
		Name:     "x",
		Layers:   []Layer{{Name: "w", Reader: watchErrReader{}, Watch: true}},
		OnChange: func(map[string]any) {},
	})
	if err == nil {
		t.Fatal("Load should fail when WatchValue errors")
	}
}

// TestSetupWatch_SkipsStaticLayer 直接构造 Store 覆盖「Watch=true 但 Reader 不是
// ValueWatcher」的防御分支（Load 已用不变量排除，setupWatch 仍须容忍）。
func TestSetupWatch_SkipsStaticLayer(t *testing.T) {
	s := &Store{onChange: func(map[string]any) {}}
	s.layerM = []layerState{{
		spec: Layer{Name: "static", Reader: &staticReader{data: []byte("a: 1")}, Watch: true},
		m:    map[string]any{"a": "1"},
	}}
	if err := s.setupWatch(Options{}); err != nil {
		t.Fatalf("setupWatch: %v", err)
	}
	_ = s.Close()
}

// --- 热更新重连路径 ---

// reconnectReader 首次 WatchValue 返回 first；后续按 next 脚本返回。
type reconnectReader struct {
	mu      sync.Mutex
	data    []byte
	calls   int
	first   chan []byte
	next    chan []byte
	nextErr error
}

func (r *reconnectReader) Load(context.Context, string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.data, nil
}

func (r *reconnectReader) WatchValue(context.Context, string) (<-chan []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.calls == 1 {
		return r.first, nil
	}
	if r.nextErr != nil {
		return nil, r.nextErr
	}
	return r.next, nil
}

func (r *reconnectReader) watchCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// TestWatch_ReconnectSuccess 通道关闭 → 退避重连 → 主动全量拉取并通知。
func TestWatch_ReconnectSuccess(t *testing.T) {
	r := &reconnectReader{
		data:  []byte("v: 1"),
		first: make(chan []byte),
		next:  make(chan []byte),
	}
	updated := make(chan string, 1)
	s, err := Load(Options{
		Name:     "reconnect",
		Layers:   []Layer{{Name: "flaky", Reader: r, Watch: true}},
		OnChange: func(m map[string]any) { updated <- getStr(m, "v") },
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	r.mu.Lock()
	r.data = []byte("v: 2")
	r.mu.Unlock()
	close(r.first)

	select {
	case got := <-updated:
		if got != "2" {
			t.Fatalf("reconnect snapshot v = %q, want 2", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for reconnect reload")
	}
}

// TestWatch_ReconnectError 重连失败继续重试（不退出监听）。
func TestWatch_ReconnectError(t *testing.T) {
	r := &reconnectReader{
		data:    []byte("v: 1"),
		first:   make(chan []byte),
		nextErr: errors.New("still down"),
	}
	s, err := Load(Options{
		Name:     "reconnect-err",
		Layers:   []Layer{{Name: "flaky", Reader: r, Watch: true}},
		OnChange: func(map[string]any) {},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	defer s.Close()

	close(r.first)
	waitFor(t, "reconnect attempt", func() bool { return r.watchCalls() >= 2 })
}

// TestWatch_CancelDuringBackoff Close 落在退避窗口内：watcher 走 ctx.Done 退出。
func TestWatch_CancelDuringBackoff(t *testing.T) {
	r := &reconnectReader{
		data:  []byte("v: 1"),
		first: make(chan []byte),
		next:  make(chan []byte),
	}
	s, err := Load(Options{
		Name:     "cancel-backoff",
		Layers:   []Layer{{Name: "flaky", Reader: r, Watch: true}},
		OnChange: func(map[string]any) {},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	close(r.first)
	time.Sleep(100 * time.Millisecond) // 确保 goroutine 已进入退避窗口
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// --- Close 资源错误 ---

type errCloser struct{ err error }

func (c errCloser) Close() error { return c.err }

func TestStore_Close_SelfCloserError(t *testing.T) {
	sentinel := errors.New("close failed")
	s := &Store{selfClosers: []io.Closer{errCloser{err: sentinel}}}
	if err := s.Close(); !errors.Is(err, sentinel) {
		t.Fatalf("Close = %v, want sentinel", err)
	}
}

// --- 读取/转换边界 ---

func TestStore_GetStringSlice(t *testing.T) {
	s := &Store{merged: map[string]any{
		"s":    "x",
		"list": []any{"a", 1, "", " b "},
	}}
	if got := s.GetStringSlice("missing"); got != nil {
		t.Fatalf("missing = %v, want nil", got)
	}
	got := s.GetStringSlice("list")
	want := []string{"a", "1", "b"}
	if len(got) != len(want) {
		t.Fatalf("list = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("list = %v, want %v", got, want)
		}
	}
}

func TestStore_GetStringMissing(t *testing.T) {
	s := &Store{merged: map[string]any{}}
	if got := s.GetString("nope"); got != "" {
		t.Fatalf("GetString(missing) = %q, want empty", got)
	}
	if got := String(nil); got != "" {
		t.Fatalf("String(nil) = %q, want empty", got)
	}
}

func TestAt_Edges(t *testing.T) {
	m := map[string]any{"a": 1, "nested": map[string]any{"b": "x"}}
	if _, ok := At(nil, "a"); ok {
		t.Fatal("At(nil) should miss")
	}
	if _, ok := At(m, ""); ok {
		t.Fatal("At(empty key) should miss")
	}
	if _, ok := At(m, "a.b"); ok {
		t.Fatal("At should miss when traversing a scalar")
	}
	if v, ok := At(m, "nested.b"); !ok || v != "x" {
		t.Fatalf("At(nested.b) = (%v,%v), want (x,true)", v, ok)
	}
}

func TestString_Scalars(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, ""},
		{"s", "s"},
		{true, "true"},
		{false, "false"},
		{7, "7"},
		{int64(8), "8"},
		{1.5, "1.5"},
		{struct{}{}, "{}"},
	}
	for _, tc := range cases {
		if got := String(tc.in); got != tc.want {
			t.Fatalf("String(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStringList_Shapes(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want []string
	}{
		{"nil", nil, nil},
		{"strings", []string{" a ", "", "b"}, []string{"a", "b"}},
		{"blank string", "   ", nil},
		{"csv", " a , b ,, c", []string{"a", "b", "c"}},
		{"any", []any{" x ", 1, "", true}, []string{"x", "1", "true"}},
		{"unsupported", 5, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := StringList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("StringList = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("StringList = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// --- merge.go 边界 ---

func TestEnvironMap_EmptySuffixIgnored(t *testing.T) {
	t.Setenv("BALDTESTENVEMPTY_", "x")
	if got := environMap("baldtestenvempty"); got != nil {
		t.Fatalf("environMap = %v, want nil (empty suffix ignored)", got)
	}
}

func TestDecodeDocument_Errors(t *testing.T) {
	if _, err := decodeDocument([]byte("a: ["), "yaml"); err == nil {
		t.Fatal("malformed yaml should error")
	}
	if _, err := decodeDocument([]byte("{"), "json"); err == nil {
		t.Fatal("malformed json should error")
	}
	if _, err := decodeDocument([]byte("a = 1"), "toml"); err == nil {
		t.Fatal("unsupported format should error")
	}
	if m, err := decodeDocument(nil, "yaml"); err != nil || m != nil {
		t.Fatalf("empty document = (%v,%v), want (nil,nil)", m, err)
	}
}

func TestFormatOf(t *testing.T) {
	cases := map[string]string{
		"a.yaml": "yaml",
		"a.yml":  "yml",
		"a.JSON": "json",
		"a.toml": "",
		"a":      "",
	}
	for path, want := range cases {
		if got := FormatOf(path); got != want {
			t.Fatalf("FormatOf(%q) = %q, want %q", path, got, want)
		}
	}
}

// --- source.go：远程桥与 kratos 适配器 ---

func TestRemoteLayer_Errors(t *testing.T) {
	l := newRemoteLayer(failingRemote{})
	if _, err := l.Load(context.Background(), ""); err == nil {
		t.Fatal("Load should surface Read error")
	}
	if _, err := l.WatchValue(context.Background(), ""); err == nil {
		t.Fatal("WatchValue should surface Watch error")
	}
}

// burstRemote 同步回调两次，第二次在测试取消外层 ctx 之后发生：
// 通道已满 + ctx.Done 已就绪 → 回调走 ctx.Done 丢弃更新（不阻塞 WatchValue）。
type burstRemote struct {
	firstSent   chan struct{}
	allowSecond chan struct{}
}

func (b *burstRemote) Read(context.Context) ([]byte, string, error) {
	return []byte("a: 1"), "yaml", nil
}

func (b *burstRemote) Watch(_ context.Context, onChange func([]byte, string)) error {
	onChange([]byte("a: 1"), "yaml") // 填满容量 1 的缓冲
	close(b.firstSent)
	<-b.allowSecond
	onChange([]byte("a: 2"), "yaml") // 外层 ctx 已取消 → 立即选 ctx.Done 返回
	return nil
}

func TestRemoteLayer_WatchCancelDropsUpdate(t *testing.T) {
	b := &burstRemote{firstSent: make(chan struct{}), allowSecond: make(chan struct{})}
	l := newRemoteLayer(b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		ch  <-chan []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		ch, err := l.WatchValue(ctx, "")
		done <- result{ch: ch, err: err}
	}()

	<-b.firstSent
	cancel()
	close(b.allowSecond)
	var res result
	select {
	case res = <-done:
	case <-time.After(time.Second):
		t.Fatal("WatchValue blocked after cancel")
	}
	if res.err != nil {
		t.Fatalf("WatchValue: %v", res.err)
	}
	if got := <-res.ch; string(got) != "a: 1" {
		t.Fatalf("first update = %q, want a: 1", got)
	}
	if l.currentFormat() != "yaml" {
		t.Fatalf("format = %q, want yaml", l.currentFormat())
	}
}

// errKratosSource 可脚本化 Load/Watch 错误。
type errKratosSource struct {
	loadErr  error
	kvs      []*kratosconfig.KeyValue
	watchErr error
	watcher  kratosconfig.Watcher
}

func (s *errKratosSource) Load() ([]*kratosconfig.KeyValue, error) {
	return s.kvs, s.loadErr
}

func (s *errKratosSource) Watch() (kratosconfig.Watcher, error) {
	return s.watcher, s.watchErr
}

func TestKratosAdapter_LoadErrors(t *testing.T) {
	a := &kratosSourceAdapter{src: &errKratosSource{loadErr: errors.New("boom")}}
	if _, _, err := a.Read(context.Background()); err == nil {
		t.Fatal("Read should wrap Load error")
	}
	a = &kratosSourceAdapter{src: &errKratosSource{}}
	if _, _, err := a.Read(context.Background()); err == nil {
		t.Fatal("Read should fail on empty kv set")
	}
}

// scriptedWatcher 按脚本返回 Next；耗尽后持续返回 context.Canceled。
type scriptedWatcher struct {
	mu      sync.Mutex
	seq     []nextResult
	i       int
	stopped chan struct{}
}

type nextResult struct {
	kvs []*kratosconfig.KeyValue
	err error
}

func (w *scriptedWatcher) Next() ([]*kratosconfig.KeyValue, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.i >= len(w.seq) {
		return nil, context.Canceled
	}
	r := w.seq[w.i]
	w.i++
	return r.kvs, r.err
}

func (w *scriptedWatcher) Stop() error {
	select {
	case w.stopped <- struct{}{}:
	default:
	}
	return nil
}

func TestKratosAdapter_WatchInitError(t *testing.T) {
	a := &kratosSourceAdapter{src: &errKratosSource{watchErr: errors.New("no watch")}}
	if err := a.Watch(context.Background(), func([]byte, string) {}); err == nil {
		t.Fatal("Watch should surface Watch() init error")
	}
}

// TestKratosAdapter_WatchCanceled watcher 返回 context.Canceled → 退出并 Stop。
func TestKratosAdapter_WatchCanceled(t *testing.T) {
	w := &scriptedWatcher{stopped: make(chan struct{}, 1)}
	a := &kratosSourceAdapter{src: &errKratosSource{watcher: w}}
	if err := a.Watch(context.Background(), func([]byte, string) {}); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	select {
	case <-w.stopped:
	case <-time.After(time.Second):
		t.Fatal("watcher not stopped after Canceled")
	}
}

// TestKratosAdapter_WatchRetryThenCancel 普通错误 → 退避中被 ctx 取消退出。
func TestKratosAdapter_WatchRetryThenCancel(t *testing.T) {
	w := &scriptedWatcher{
		seq:     []nextResult{{err: errors.New("transient")}},
		stopped: make(chan struct{}, 1),
	}
	a := &kratosSourceAdapter{src: &errKratosSource{watcher: w}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := a.Watch(ctx, func([]byte, string) {}); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	time.Sleep(50 * time.Millisecond) // 让第一次 Next 的错误进入退避
	cancel()
	select {
	case <-w.stopped:
	case <-time.After(time.Second):
		t.Fatal("watcher not stopped after ctx cancel during backoff")
	}
}

// TestKratosAdapter_WatchRetryAfterBackoff 退避期满后重试，下一次 Canceled 退出。
func TestKratosAdapter_WatchRetryAfterBackoff(t *testing.T) {
	w := &scriptedWatcher{
		seq: []nextResult{
			{err: errors.New("transient")},
			{kvs: []*kratosconfig.KeyValue{{Key: "k", Value: []byte("a: 1"), Format: "yaml"}}},
			{err: context.Canceled},
		},
		stopped: make(chan struct{}, 1),
	}
	got := make(chan string, 1)
	a := &kratosSourceAdapter{src: &errKratosSource{watcher: w}}
	if err := a.Watch(context.Background(), func(data []byte, _ string) {
		select {
		case got <- string(data):
		default:
		}
	}); err != nil {
		t.Fatalf("Watch: %v", err)
	}
	select {
	case v := <-got:
		if v != "a: 1" {
			t.Fatalf("delivered %q, want a: 1", v)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for kv delivery")
	}
	select {
	case <-w.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("watcher not stopped after Canceled")
	}
}

// 结构性零覆盖（登记备查）：
//   - config.go:233-234 —— 本地源构造失败（filepath.Abs 报错）：仅当工作目录
//     不可解析时可达，darwin 的 Getwd 会解析已删除目录；Linux（CI）由
//     TestLoad_LocalOpenError 覆盖，本机跳过。
//   - config.go:393-395 —— 「通道关闭且 ctx 恰在读取 !ok 后取消」的竞态窗口：
//     只有 select 选中 ch 分支后、ctx.Err() 读取前的纳秒级窗口才可达，无法确定性
//     复现；用概率测试换取偶发红不值得。
//   - config.go:536 —— getAtPath 尾部兜底 return：key == "" 已在函数首行拦截，
//     strings.Split 对非空串必返回 ≥1 段，循环内必然返回，保留为防御性代码。
