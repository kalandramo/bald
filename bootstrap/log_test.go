package bootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	bootstrapv1 "github.com/kalandramo/bald/bconf/gen/go/bootstrap/v1"
	log "github.com/kalandramo/bald/log"
)

// stubLogProvider 返回固定行为的 LoggerProvider。
func stubLogProvider(l log.Logger, cleanup func(), err error) LoggerProvider {
	return func(context.Context, *bootstrapv1.Logger) (log.Logger, func(), error) {
		return l, cleanup, err
	}
}

// stubLogger 最小 Logger 实现。
type stubLogger struct{ enabled bool }

func (s *stubLogger) Debug(context.Context, string, ...any) {}
func (s *stubLogger) Info(context.Context, string, ...any)  {}
func (s *stubLogger) Warn(context.Context, string, ...any)  {}
func (s *stubLogger) Error(context.Context, string, ...any) {}
func (s *stubLogger) Enabled(log.Level) bool                { return s.enabled }
func (s *stubLogger) With(...any) log.Logger                { return s }

func TestLogRegistry_Register(t *testing.T) {
	r := NewLogRegistry()
	if err := r.Register("slog", stubLogProvider(&stubLogger{}, nil, nil)); err != nil {
		t.Fatalf("Register(first) = %v, want nil", err)
	}
	if err := r.Register("slog", stubLogProvider(&stubLogger{}, nil, nil)); err == nil {
		t.Fatal("Register(duplicate) = nil, want error")
	}
	if err := r.Register("", stubLogProvider(&stubLogger{}, nil, nil)); err == nil {
		t.Fatal("Register(empty name) = nil, want error")
	}
	if err := r.Register("nilp", nil); err == nil {
		t.Fatal("Register(nil provider) = nil, want error")
	}
}

func TestBuildLogger_OK(t *testing.T) {
	r := NewLogRegistry()
	want := &stubLogger{enabled: true}
	called := false
	r.MustRegister("slog", func(context.Context, *bootstrapv1.Logger) (log.Logger, func(), error) {
		called = true
		return want, func() { called = false }, nil
	})

	l, cleanup, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "slog"})
	if err != nil || l != want {
		t.Fatalf("BuildLogger() = (%v, %v), want (%v, nil)", l, err, want)
	}
	if !called {
		t.Fatal("provider should be invoked")
	}
	cleanup()
	if called {
		t.Fatal("cleanup should run")
	}
}

func TestBuildLogger_Errors(t *testing.T) {
	r := NewLogRegistry()
	r.MustRegister("slog", stubLogProvider(&stubLogger{}, nil, nil))

	if _, _, err := r.BuildLogger(context.Background(), nil); err == nil {
		t.Fatal("nil config should error")
	}
	if _, _, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{}); err == nil {
		t.Fatal("empty type should error")
	}
	_, _, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "zap"})
	if err == nil || !strings.Contains(err.Error(), `not registered`) || !strings.Contains(err.Error(), "slog") {
		t.Fatalf("unregistered type should error with candidates, got: %v", err)
	}

	// provider 返回 nil Logger 视为错误。
	r2 := NewLogRegistry()
	r2.MustRegister("slog", stubLogProvider(nil, nil, nil))
	if _, _, err := r2.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "slog"}); err == nil {
		t.Fatal("nil logger from provider should error")
	}

	// provider 出错短路并包装。
	r3 := NewLogRegistry()
	r3.MustRegister("slog", stubLogProvider(nil, nil, errors.New("boom")))
	if _, _, err := r3.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "slog"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("provider error should be wrapped, got: %v", err)
	}
}

func TestBslogLoggerProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	cfg := &bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{
			Level:      "debug",
			Format:     "json",
			OutputPath: path,
		},
	}

	l, cleanup, err := BslogLoggerProvider()(context.Background(), cfg)
	if err != nil {
		t.Fatalf("BslogLoggerProvider() = %v, want nil", err)
	}
	if l == nil {
		t.Fatal("logger should not be nil")
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	// level=debug 生效。
	if !l.Enabled(log.LevelDebug) {
		t.Fatal("debug should be enabled per contract")
	}
	l.Info(context.Background(), "contract-log", "k", "v")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("log file should be written: %v", err)
	}
	if !strings.Contains(string(data), "contract-log") || !strings.Contains(string(data), `"k":"v"`) {
		t.Fatalf("unexpected log content: %s", data)
	}
}

func TestBslogLoggerProvider_DefaultFallback(t *testing.T) {
	// type=slog 但 Slog 段缺失 → 默认配置（stdout + info），不报错。
	l, cleanup, err := BslogLoggerProvider()(context.Background(), &bootstrapv1.Logger{Type: "slog"})
	if err != nil || l == nil {
		t.Fatalf("missing slog section should fall back to defaults, got (%v, %v)", l, err)
	}
	if cleanup != nil {
		cleanup()
	}
	if l.Enabled(log.LevelDebug) {
		t.Fatal("default level should be info (debug disabled)")
	}
}

// TestBuildLogger_Integration 契约 → 装配 → 全局表 的端到端验证。
func TestBuildLogger_Integration(t *testing.T) {
	defer log.SetLogger(nil)

	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")

	lr := NewLogRegistry()
	lr.MustRegister("slog", BslogLoggerProvider())

	l, cleanup, err := lr.BuildLogger(context.Background(), &bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{Format: "text", OutputPath: path},
	})
	if err != nil {
		t.Fatalf("BuildLogger() = %v, want nil", err)
	}
	defer cleanup()

	log.SetLogger(l)
	log.Info(context.Background(), "e2e", "tenant", "t1")

	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "e2e") {
		t.Fatalf("global logger should write through provider backend: (%s, %v)", data, err)
	}
}

// TestNopLoggerProvider nop 后端工厂：静默、零配置段、cleanup noop。
func TestNopLoggerProvider(t *testing.T) {
	l, cleanup, err := NopLoggerProvider()(context.Background(), &bootstrapv1.Logger{Type: "nop"})
	if err != nil || l == nil {
		t.Fatalf("NopLoggerProvider() = (%v, %v), want non-nil logger", l, err)
	}
	if cleanup != nil {
		cleanup() // noop，不 panic 即可。
	}
	if l.Enabled(log.LevelError) {
		t.Fatal("nop logger should never be enabled")
	}
	l.Info(context.Background(), "silent", "k", "v") // 不输出、不 panic。
}

// TestBuiltinLogRegistry 内置全量注册表：7 名齐备、查表构造、未实现 type fail-fast。
func TestBuiltinLogRegistry(t *testing.T) {
	r := NewBuiltinLogRegistry()

	want := []string{"aliyun", "charm", "loki", "nop", "sentry", "slog", "tencent"}
	if got := r.names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("builtin names = %v, want %v", got, want)
	}

	// type=nop：静默后端。
	l, cleanup, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "nop"})
	if err != nil || l == nil {
		t.Fatalf("type=nop: (%v, %v)", l, err)
	}
	cleanup()
	if l.Enabled(log.LevelDebug) {
		t.Fatal("nop backend should never be enabled")
	}

	// type=loki：假 endpoint 构造成功（构造期零网络；零日志写入，cleanup 空缓冲不推送）。
	ll, lcleanup, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{
		Type: "loki",
		Loki: &bootstrapv1.Logger_Loki{Endpoint: "http://127.0.0.1:1/loki/api/v1/push"},
	})
	if err != nil || ll == nil {
		t.Fatalf("type=loki: (%v, %v)", ll, err)
	}
	lcleanup()

	// 未实现的 type：fail-fast 并列出可用项。
	if _, _, err := r.BuildLogger(context.Background(), &bootstrapv1.Logger{Type: "zap"}); err == nil ||
		!strings.Contains(err.Error(), "registered") {
		t.Fatalf("type=zap should fail-fast listing available, got: %v", err)
	}
}

// TestRegisterBuiltinLogProviders 组合语义：自定义 + 内置可混注；重复内置 fail-fast。
func TestRegisterBuiltinLogProviders(t *testing.T) {
	r := NewLogRegistry()
	r.MustRegister("mylog", stubLogProvider(&stubLogger{}, nil, nil))
	if err := RegisterBuiltinLogProviders(r); err != nil {
		t.Fatalf("builtin after custom: %v", err)
	}
	// 再次内置：重名 fail-fast。
	if err := RegisterBuiltinLogProviders(r); err == nil {
		t.Fatal("duplicate builtin registration should fail")
	}
}

// TestLogOptions_MultiOutputAndRotate 契约多输出 + 轮转段 → Options 映射：
// output_paths 优先于 output_path；rotate 零值字段回退 bslog 默认（100/7/30/gzip）。
func TestLogOptions_MultiOutputAndRotate(t *testing.T) {
	o := LogOptions(&bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{
			OutputPath:  "/should/be/ignored.log",
			OutputPaths: []string{"stdout", "/var/log/app.log"},
			Rotate: &bootstrapv1.Logger_Slog_Rotate{
				Enabled:  true,
				MaxSize:  50,
				Compress: true,
				// MaxBackups/MaxAge 零值 → 回退默认 7/30。
			},
		},
	})
	if want := []string{"stdout", "/var/log/app.log"}; !reflect.DeepEqual(o.OutputPaths, want) {
		t.Fatalf("OutputPaths = %v, want %v（output_paths 应优先于单值）", o.OutputPaths, want)
	}
	if !o.Rotate.Enabled || o.Rotate.MaxSize != 50 || !o.Rotate.Compress {
		t.Fatalf("rotate 显式字段映射: %+v", o.Rotate)
	}
	if o.Rotate.MaxBackups != 7 || o.Rotate.MaxAge != 30 {
		t.Fatalf("rotate 零值应回退默认（7 份/30 天）, got %+v", o.Rotate)
	}
}

// TestLogOptions_OutputPathBackwardCompat 单值 output_path 仍映射为单元素，
// 既有配置零迁移。
func TestLogOptions_OutputPathBackwardCompat(t *testing.T) {
	o := LogOptions(&bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{OutputPath: "stderr"},
	})
	if !reflect.DeepEqual(o.OutputPaths, []string{"stderr"}) {
		t.Fatalf("OutputPaths = %v, want [stderr]", o.OutputPaths)
	}
}

// TestBslogLoggerProvider_MultiOutputRotate 端到端：契约多输出 + 轮转 →
// 文件目标收到全量日志流（复制分流），lumberjack 轮转开启时正常写入。
func TestBslogLoggerProvider_MultiOutputRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	cfg := &bootstrapv1.Logger{
		Type: "slog",
		Slog: &bootstrapv1.Logger_Slog{
			Level:       "info",
			Format:      "json",
			OutputPaths: []string{path, "stderr"},
			Rotate:      &bootstrapv1.Logger_Slog_Rotate{Enabled: true, MaxSize: 1},
		},
	}

	l, cleanup, err := BslogLoggerProvider()(context.Background(), cfg)
	if err != nil || l == nil {
		t.Fatalf("BslogLoggerProvider() = (%v, %v), want non-nil logger", l, err)
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	l.Info(context.Background(), "multi-output", "k", "v")

	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "multi-output") {
		t.Fatalf("文件目标应收到全量日志流: (%s, %v)", data, err)
	}
}
