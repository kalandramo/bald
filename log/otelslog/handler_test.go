package otelslog

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
)

// lazyValue 实现 LogValuer，验证 convert 的 resolve 分支。
type lazyValue struct{}

func (lazyValue) LogValue() slog.Value { return slog.StringValue("resolved") }

// fakeLogger 是 log.Logger 的测试替身：捕获 Emit 的 record 与 ctx，
// Enabled 行为可注入。
type fakeLogger struct {
	log.Logger

	mu       sync.Mutex
	enabledF func(context.Context, log.EnabledParameters) bool
	emitted  []capturedRecord
}

type capturedRecord struct {
	ctx context.Context
	rec log.Record
}

func (f *fakeLogger) Emit(ctx context.Context, r log.Record) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emitted = append(f.emitted, capturedRecord{ctx: ctx, rec: r.Clone()})
}

func (f *fakeLogger) Enabled(ctx context.Context, p log.EnabledParameters) bool {
	if f.enabledF == nil {
		return true
	}
	return f.enabledF(ctx, p)
}

// fakeProvider 对任意 Logger 名返回同一 fakeLogger（测试只关心一条流）。
type fakeProvider struct {
	log.LoggerProvider
	logger *fakeLogger
}

func (p *fakeProvider) Logger(name string, options ...log.LoggerOption) log.Logger {
	return p.logger
}

func newTestHandler(t *testing.T, opts ...Option) (*Handler, *fakeLogger) {
	t.Helper()
	logger := &fakeLogger{}
	p := &fakeProvider{logger: logger}
	h := NewHandler("bald/test", append([]Option{WithLoggerProvider(p)}, opts...)...)
	return h, logger
}

// findAttr 遍历 record 属性取首个匹配键的值。
func findAttr(r *log.Record, key string) (attribute.Value, bool) {
	var found attribute.Value
	var ok bool
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == key {
			found, ok = kv.Value, true
			return false
		}
		return true
	})
	return found, ok
}

func TestConvertRecordBasics(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h)

	now := time.Now()
	sl.Info("hello", "count", 3, "ratio", 0.5, "name", "bald")

	if len(logger.emitted) != 1 {
		t.Fatalf("emitted: want 1, got %d", len(logger.emitted))
	}
	rec := logger.emitted[0].rec
	if rec.Body().AsString() != "hello" {
		t.Errorf("body: want hello, got %q", rec.Body().AsString())
	}
	if rec.Severity() != log.SeverityInfo {
		t.Errorf("severity: want %v, got %v", log.SeverityInfo, rec.Severity())
	}
	if rec.SeverityText() != "INFO" {
		t.Errorf("severity text: want INFO, got %q", rec.SeverityText())
	}
	if rec.Timestamp().IsZero() || rec.Timestamp().After(now.Add(time.Minute)) {
		t.Errorf("timestamp not set sanely: %v", rec.Timestamp())
	}
	if v, ok := findAttr(&rec, "count"); !ok || v.AsInt64() != 3 {
		t.Errorf("count attr: got %v (found=%v)", v, ok)
	}
	if v, ok := findAttr(&rec, "ratio"); !ok || v.AsFloat64() != 0.5 {
		t.Errorf("ratio attr: got %v (found=%v)", v, ok)
	}
	if v, ok := findAttr(&rec, "name"); !ok || v.AsString() != "bald" {
		t.Errorf("name attr: got %v (found=%v)", v, ok)
	}
}

func TestSeverityMapping(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h)

	cases := []struct {
		emit func(string, ...any)
		want log.Severity
		text string
	}{
		{sl.Debug, log.SeverityDebug, "DEBUG"},
		{sl.Info, log.SeverityInfo, "INFO"},
		{sl.Warn, log.SeverityWarn, "WARN"},
		{sl.Error, log.SeverityError, "ERROR"},
	}
	for _, tc := range cases {
		tc.emit("msg")
		rec := logger.emitted[len(logger.emitted)-1].rec
		if rec.Severity() != tc.want {
			t.Errorf("severity: want %v, got %v", tc.want, rec.Severity())
		}
		if rec.SeverityText() != tc.text {
			t.Errorf("severity text: want %s, got %s", tc.text, rec.SeverityText())
		}
	}

	// 自定义级别同样按固定偏移映射（sevOffset = SeverityDebug - LevelDebug）。
	sl.Log(context.Background(), slog.LevelWarn+1, "custom")
	rec := logger.emitted[len(logger.emitted)-1].rec
	const sevOffset = slog.Level(log.SeverityDebug) - slog.LevelDebug
	if want, got := log.Severity(slog.LevelWarn+1+sevOffset), rec.Severity(); got != want {
		t.Errorf("custom level: want %v, got %v", want, got)
	}
}

func TestErrorPromotion(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h)

	sentinel := errors.New("boom")
	sl.Error("failed", slog.Any("err", sentinel))

	rec := logger.emitted[0].rec
	if rec.Err() == nil || rec.Err().Error() != "boom" {
		t.Errorf("error promotion: want boom, got %v", rec.Err())
	}
	// 错误晋升后不再占属性位。
	if _, ok := findAttr(&rec, "err"); ok {
		t.Error("promoted error must not remain an attribute")
	}

	// 组内错误的不同语义：WithGroup 链上的 error 仍晋升记录错误（上游
	// WithGroupWithAttrs 钉死）；记录内 slog.Group 值里的 error 不晋升，
	// 以字符串留在组内（组 Map 没有错误槽）。
	sl2 := slog.New(h.WithGroup("g"))
	sl2.Error("failed with group", slog.Any("err", sentinel))
	rec2 := logger.emitted[1].rec
	if rec2.Err() == nil || rec2.Err().Error() != "boom" {
		t.Errorf("WithGroup error promotion: want boom, got %v", rec2.Err())
	}

	sl3 := slog.New(h)
	sl3.Error("failed in group value", slog.Group("grp", slog.Any("err", sentinel), slog.String("keep", "yes")))
	rec3 := logger.emitted[2].rec
	if rec3.Err() != nil {
		t.Errorf("in-record group error must not promote: got %v", rec3.Err())
	}
	var grp attribute.Value
	var grpFound bool
	rec3.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == "grp" {
			grp, grpFound = kv.Value, true
			return false
		}
		return true
	})
	if !grpFound || grp.Type() != attribute.MAP {
		t.Fatalf("grp attr: found=%v type=%v", grpFound, grp.Type())
	}
	var errStr, keep string
	for _, kv := range grp.AsMap() {
		switch string(kv.Key) {
		case "err":
			errStr = kv.Value.AsString()
		case "keep":
			keep = kv.Value.AsString()
		}
	}
	if errStr != "boom" || keep != "yes" {
		t.Errorf("grp contents: err=%q keep=%q", errStr, keep)
	}
}

func TestGroupNesting(t *testing.T) {
	h, logger := newTestHandler(t)
	// 非空外层组包裹内层组（上游 independent-WithGroup 用例钉死的结构：
	// G 非空时 G 包 H，记录属性落叶子组）。
	l1 := slog.New(h.WithGroup("g").WithAttrs([]slog.Attr{slog.String("a", "1")}))
	l2 := l1.WithGroup("h")

	l2.Info("msg", "k", "v")

	rec := logger.emitted[0].rec
	var gVal attribute.Value
	var found bool
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == "g" {
			gVal, found = kv.Value, true
			return false
		}
		return true
	})
	if !found || gVal.Type() != attribute.MAP {
		t.Fatalf("outer group g: found=%v", found)
	}
	var aAttr string
	var hVal attribute.Value
	for _, kv := range gVal.AsMap() {
		switch string(kv.Key) {
		case "a":
			aAttr = kv.Value.AsString()
		case "h":
			hVal = kv.Value
		}
	}
	if aAttr != "1" {
		t.Errorf("g.a: want 1, got %q", aAttr)
	}
	if hVal.Type() != attribute.MAP {
		t.Fatalf("inner group h: type=%v", hVal.Type())
	}
	var leaf string
	for _, kv := range hVal.AsMap() {
		if string(kv.Key) == "k" {
			leaf = kv.Value.AsString()
		}
	}
	if leaf != "v" {
		t.Errorf("leaf k: want v, got %q", leaf)
	}

	// 空外层组被剔除：WithGroup("g").WithGroup("h") 且 g 无属性时，
	// 记录属性直接落在 h（上游空组省略语义）。
	h2, logger2 := newTestHandler(t)
	emptyOuter := slog.New(h2.WithGroup("g").WithGroup("h"))
	emptyOuter.Info("msg", "k", "v")
	rec2 := logger2.emitted[0].rec
	var keys []string
	rec2.WalkAttributes(func(kv attribute.KeyValue) bool {
		keys = append(keys, string(kv.Key))
		return true
	})
	for _, k := range keys {
		if k == "g" {
			t.Error("empty outer group must be elided")
		}
	}
	var hFound bool
	rec2.WalkAttributes(func(kv attribute.KeyValue) bool {
		if string(kv.Key) == "h" {
			hFound = true
			return false
		}
		return true
	})
	if !hFound {
		t.Error("inner group h must be kept")
	}
}

func TestEmptyGroupSkipped(t *testing.T) {
	h, logger := newTestHandler(t)
	// 空组 g、有属性的组 h：输出应跳过 g 直接挂 h（NextNonEmpty 语义）。
	sl := slog.New(h.WithGroup("g").WithGroup("h").WithAttrs([]slog.Attr{slog.String("a", "b")}))
	sl.Info("msg")

	rec := logger.emitted[0].rec
	var keys []string
	rec.WalkAttributes(func(kv attribute.KeyValue) bool {
		keys = append(keys, string(kv.Key))
		return true
	})
	for _, k := range keys {
		if k == "g" {
			t.Error("empty outer group must be skipped")
		}
	}
	found := false
	for _, k := range keys {
		if k == "h" {
			found = true
		}
	}
	if !found {
		t.Error("non-empty group h must be kept")
	}

	// 记录无属性、所有组都空：不输出任何组。
	sl3 := slog.New(h.WithGroup("x").WithGroup("y"))
	sl3.Info("bare")
	rec3 := logger.emitted[1].rec
	var n int
	rec3.WalkAttributes(func(attribute.KeyValue) bool { n++; return true })
	if n != 0 {
		t.Errorf("no groups expected on bare record, got %d attrs", n)
	}
}

func TestEmptyKeyGroupFlattened(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h)

	sl.Info("msg", slog.Group("", slog.String("a", "1"), slog.String("b", "2")))

	rec := logger.emitted[0].rec
	// 空键组的成员被摊平成顶层属性。
	if v, ok := findAttr(&rec, "a"); !ok || v.AsString() != "1" {
		t.Errorf("flattened a: got %v (found=%v)", v, ok)
	}
	if v, ok := findAttr(&rec, "b"); !ok || v.AsString() != "2" {
		t.Errorf("flattened b: got %v (found=%v)", v, ok)
	}
}

func TestEnabledDelegatesToLogger(t *testing.T) {
	// 默认（级别闸门=Debug，形同虚设）：Enabled 逐级委托给 logger。
	logger := &fakeLogger{}
	p := &fakeProvider{logger: logger}
	h := NewHandler("bald/test", WithLoggerProvider(p))

	var gotSlog slog.Level
	var gotSev log.Severity
	logger.enabledF = func(_ context.Context, p log.EnabledParameters) bool {
		gotSev = p.Severity
		return true
	}
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("default handler must delegate Debug to logger")
	}
	gotSlog = slog.Level(gotSev) - slog.Level(log.SeverityDebug) + slog.LevelDebug
	if gotSlog != slog.LevelDebug {
		t.Errorf("delegated severity: want Debug(%d), got %d", slog.LevelDebug, gotSlog)
	}

	// logger 说不可用 → false。
	logger.enabledF = func(context.Context, log.EnabledParameters) bool { return false }
	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("must propagate logger's disabled verdict")
	}

	// WithLevel 显式收窄：本地闸门在委托之前。
	h2 := NewHandler("bald/test", WithLoggerProvider(p), WithLevel(slog.LevelWarn))
	logger.enabledF = func(context.Context, log.EnabledParameters) bool { return true }
	if h2.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("WithLevel(Warn) must gate Debug locally")
	}
	if !h2.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("WithLevel(Warn) must pass Warn through")
	}
}

func TestLevelStringOption(t *testing.T) {
	cases := map[string]slog.Level{
		"debug": slog.LevelDebug,
		"INFO":  slog.LevelInfo,
		" warn": slog.LevelWarn,
		"ERROR": slog.LevelError,
		"WARNING": slog.LevelWarn,
		"junk":  slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLevelString(in); got != want {
			t.Errorf("parseLevelString(%q): want %v, got %v", in, want, got)
		}
	}
}

func TestSourceAttribution(t *testing.T) {
	h, logger := newTestHandler(t, WithSource(true))
	sl := slog.New(h)
	sl.Info("with source")

	rec := logger.emitted[0].rec
	file, fok := findAttr(&rec, string(semconvCodeFilePath))
	fn, nok := findAttr(&rec, string(semconvCodeFunctionName))
	line, lok := findAttr(&rec, string(semconvCodeLineNumber))
	if !fok || !nok || !lok {
		t.Fatalf("source attrs missing: file=%v func=%v line=%v", fok, nok, lok)
	}
	if !strings.HasSuffix(file.AsString(), "handler_test.go") {
		t.Errorf("code.filepath: got %q", file.AsString())
	}
	if !strings.Contains(fn.AsString(), "otelslog") {
		t.Errorf("code.function: got %q", fn.AsString())
	}
	if line.AsInt64() <= 0 {
		t.Errorf("code.line.number: got %v", line.AsInt64())
	}
}

func TestDefaultHandlerNoLocalGate(t *testing.T) {
	// 上游零值 Info 的教训：默认必须放行 Debug（委托语义不被本地闸门截断）。
	logger := &fakeLogger{enabledF: func(context.Context, log.EnabledParameters) bool { return true }}
	p := &fakeProvider{logger: logger}
	h := NewHandler("bald/test", WithLoggerProvider(p))
	if !h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("default handler must not gate Debug locally")
	}
}

// 上游 semconv v1.43.0 的 code.* 键名钉在测试里（改编一致性护栏）。
const (
	semconvCodeFilePath     = "code.file.path"
	semconvCodeFunctionName = "code.function.name"
	semconvCodeLineNumber   = "code.line.number"
)

func TestSlogKindConversions(t *testing.T) {
	h, logger := newTestHandler(t)
	sl := slog.New(h)

	sl.Info("kinds",
		slog.Duration("dur", 1500*time.Millisecond),
		slog.Time("at", time.Unix(1700000000, 0)),
		slog.Uint64("u", 1<<40),
		slog.Any("big", uint64(math.MaxInt64)+1),
		slog.Any("lazy", lazyValue{}),
		slog.Group("grp", slog.String("in", "g")),
	)

	rec := logger.emitted[0].rec
	checks := []struct {
		key   string
		typ   attribute.Type
		check func(attribute.Value) bool
	}{
		{"dur", attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 1_500_000_000 }},
		{"at", attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == time.Unix(1700000000, 0).UnixNano() }},
		{"u", attribute.INT64, func(v attribute.Value) bool { return v.AsInt64() == 1<<40 }},
		{"big", attribute.FLOAT64, func(v attribute.Value) bool { return v.Type() == attribute.FLOAT64 }},
		{"lazy", attribute.STRING, func(v attribute.Value) bool { return v.AsString() == "resolved" }},
	}
	for _, c := range checks {
		v, ok := findAttr(&rec, c.key)
		if !ok || v.Type() != c.typ || !c.check(v) {
			t.Errorf("attr %s: want %v, got %v (found=%v)", c.key, c.typ, v, ok)
		}
	}
	// 记录内 slog.Group → MapValue。
	grp, ok := findAttr(&rec, "grp")
	if !ok || grp.Type() != attribute.MAP {
		t.Fatalf("grp: want MAP, got %v (found=%v)", grp.Type(), ok)
	}
	var in string
	for _, kv := range grp.AsMap() {
		if string(kv.Key) == "in" {
			in = kv.Value.AsString()
		}
	}
	if in != "g" {
		t.Errorf("grp.in: want g, got %q", in)
	}

	// 空属性（零值 Attr）被丢弃。
	sl.Info("drop", slog.Attr{})
	rec2 := logger.emitted[1].rec
	if n := rec2.AttributesLen(); n != 0 {
		t.Errorf("empty attr must be dropped, got %d attrs", n)
	}
}
